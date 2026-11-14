// Package account 保存妙手账号凭证（Passport token）与持久化。
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Account 是一个妙手（CatPaw）账号。
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Token    string `json:"token"` // Passport access token（同时作为 X-Auth-Token / access-token）
	UID      string `json:"uid"`   // 上游 userId（misId 数字部分）
	Enabled  bool   `json:"enabled"`
	Note     string `json:"note,omitempty"`
	AddedAt  string `json:"added_at,omitempty"`
	LastUsed int64  `json:"last_used,omitempty"`
}

// Store 是 accounts.json 的读写门面。
type Store struct {
	path string
	mu   sync.RWMutex
	accs []*Account
}

// Open 加载账号文件，不存在则视为空列表。
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.accs); err != nil {
		// 兼容 {"accounts":[...]} 形态
		var wrap struct {
			Accounts []*Account `json:"accounts"`
		}
		if err2 := json.Unmarshal(raw, &wrap); err2 != nil {
			return nil, fmt.Errorf("账号文件解析失败 (%s): %w", path, err)
		}
		s.accs = wrap.Accounts
	}
	for i, a := range s.accs {
		if a.ID == "" {
			a.ID = fmt.Sprintf("acc-%d", i+1)
		}
	}
	return s, nil
}

// List 返回账号快照（浅拷贝，可安全读取）。
func (s *Store) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.accs))
	for _, a := range s.accs {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// Enabled 返回启用账号数量。
func (s *Store) Enabled() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, a := range s.accs {
		if a.Enabled && a.Token != "" {
			n++
		}
	}
	return n
}

// Add 追加/更新账号（按 token 去重），并落盘。
func (s *Store) Add(a *Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID == "" {
		a.ID = fmt.Sprintf("acc-%d", len(s.accs)+1)
	}
	if a.AddedAt == "" {
		a.AddedAt = time.Now().Format(time.RFC3339)
	}
	for i, old := range s.accs {
		if old.Token == a.Token || (a.UID != "" && old.UID == a.UID) {
			// 更新已有账号
			a.ID = old.ID
			s.accs[i] = a
			return s.saveLocked()
		}
	}
	s.accs = append(s.accs, a)
	return s.saveLocked()
}

// MarkUsed 记录最后使用时间（不落盘，避免频繁 IO）。
func (s *Store) MarkUsed(id string, ts int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accs {
		if a.ID == id {
			a.LastUsed = ts
			return
		}
	}
}

func (s *Store) saveLocked() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(s.accs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
