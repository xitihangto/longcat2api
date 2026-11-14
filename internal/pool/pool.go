// Package pool 是账号池：选号、在途计数、熔断与冷却。
package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/upstream"
)

// entryState 是单账号的运行态。
type entryState struct {
	CooldownUntil int64  `json:"cooldown_until"`
	SoftLevel     int    `json:"soft_level"` // 连续软冷却次数，用于指数退避
	Failures      int    `json:"failures"`
	LastUsed      int64  `json:"last_used"`
	InFlight      int    `json:"-"`
	LastError     string `json:"last_error,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
}

// Pool 账号池。
type Pool struct {
	store  *account.Store
	cfg    *config.Config
	mu     sync.Mutex
	states map[string]*entryState
	path   string
}

// New 构建账号池，并从 state.json 恢复冷却状态。
func New(store *account.Store, cfg *config.Config) *Pool {
	p := &Pool{store: store, cfg: cfg, states: map[string]*entryState{}, path: cfg.StateFile}
	p.restore()
	return p
}

// ErrNoAccount 没有可用账号。
var ErrNoAccount = errors.New("没有可用账号（全部冷却或未配置）")

// Acquire 选一个账号并登记在途。调用方必须 Release。
func (p *Pool) Acquire() (*account.Account, error) {
	accs := p.store.List()
	if len(accs) == 0 {
		return nil, ErrNoAccount
	}
	now := time.Now().Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	var best *account.Account
	bestScore := int64(1) << 62
	var earliest int64 = 1 << 62
	for _, a := range accs {
		if !a.Enabled || a.Token == "" {
			continue
		}
		st := p.stateFor(a.ID)
		if st.Disabled || st.CooldownUntil > now {
			if st.CooldownUntil < earliest {
				earliest = st.CooldownUntil
			}
			continue
		}
		// 负载优先：在途数少者优先，其次最久未使用
		score := int64(st.InFlight)*1_000_000 + (now - max64(st.LastUsed, 0))
		if score < bestScore {
			bestScore = score
			best = a
		}
	}
	if best == nil {
		if earliest != 0 && earliest < 1<<61 {
			return nil, fmt.Errorf("%w，最早 %s 恢复", ErrNoAccount, time.Unix(earliest, 0).Format("15:04:05"))
		}
		return nil, ErrNoAccount
	}
	st := p.stateFor(best.ID)
	st.InFlight++
	st.LastUsed = now
	p.store.MarkUsed(best.ID, now)
	return best, nil
}

// Release 释放在途计数。
func (p *Pool) Release(acc *account.Account) {
	if acc == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.stateFor(acc.ID)
	if st.InFlight > 0 {
		st.InFlight--
	}
}

// Report 根据一轮请求的错误更新冷却状态。
func (p *Pool) Report(acc *account.Account, err error) {
	if acc == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.stateFor(acc.ID)
	if err == nil {
		st.SoftLevel = 0
		st.Failures = 0
		st.LastError = ""
		p.persistLocked()
		return
	}
	var ue *upstream.Error
	if errors.As(err, &ue) {
		st.LastError = ue.Msg
	} else {
		st.LastError = err.Error()
	}
	now := time.Now()
	switch {
	case errors.As(err, &ue) && ue.Kind == upstream.KindAuth:
		st.CooldownUntil = now.Add(p.cfg.Cooldown.AuthFailDur()).Unix()
		st.Failures++
	case errors.As(err, &ue) && ue.Kind == upstream.KindRateLimit:
		st.SoftLevel++
		d := time.Duration(st.SoftLevel) * p.cfg.Cooldown.SoftRateDur()
		if d > p.cfg.Cooldown.SoftRateMaxDur() {
			d = p.cfg.Cooldown.SoftRateMaxDur()
		}
		st.CooldownUntil = now.Add(d).Unix()
		st.Failures++
	case errors.As(err, &ue) && ue.Kind == upstream.KindQuota:
		// 额度类：冷却到次日 04:00
		next := now.Truncate(24 * time.Hour).Add(24*time.Hour + 4*time.Hour)
		if next.Before(now) {
			next = now.Add(6 * time.Hour)
		}
		st.CooldownUntil = next.Unix()
		st.Failures++
	default:
		st.Failures++
		// 连续失败 3 次短冷却 60s
		if st.Failures >= 3 {
			st.CooldownUntil = now.Add(60 * time.Second).Unix()
			st.Failures = 0
		}
	}
	p.persistLocked()
}

// Info 是对外面板用的账号视图。
type Info struct {
	account.Account
	CooldownUntil int64  `json:"cooldown_until"`
	CooldownLeft  int64  `json:"cooldown_left"`
	InFlight      int    `json:"in_flight"`
	Failures      int    `json:"failures"`
	LastError     string `json:"last_error,omitempty"`
	Healthy       bool   `json:"healthy"`
}

// Snapshot 返回账号池视图。
func (p *Pool) Snapshot() []Info {
	accs := p.store.List()
	now := time.Now().Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Info, 0, len(accs))
	for _, a := range accs {
		st := p.stateFor(a.ID)
		left := int64(0)
		if st.CooldownUntil > now {
			left = st.CooldownUntil - now
		}
		out = append(out, Info{
			Account:       *a,
			CooldownUntil: st.CooldownUntil,
			CooldownLeft:  left,
			InFlight:      st.InFlight,
			Failures:      st.Failures,
			LastError:     st.LastError,
			Healthy:       a.Enabled && left == 0,
		})
	}
	return out
}

func (p *Pool) stateFor(id string) *entryState {
	st, ok := p.states[id]
	if !ok {
		st = &entryState{}
		p.states[id] = st
	}
	return st
}

func (p *Pool) restore() {
	raw, err := os.ReadFile(p.path)
	if err != nil {
		return
	}
	var disk map[string]*entryState
	if err := json.Unmarshal(raw, &disk); err != nil {
		return
	}
	for k, v := range disk {
		if v != nil {
			p.states[k] = v
		}
	}
}

func (p *Pool) persistLocked() {
	if p.path == "" {
		return
	}
	if dir := filepath.Dir(p.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	raw, err := json.MarshalIndent(p.states, "", "  ")
	if err != nil {
		return
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, p.path)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
