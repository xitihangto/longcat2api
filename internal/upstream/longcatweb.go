// longcat.chat 网页通道（免费 LongCat-2.5）
//
// 协议要点（详见 docs/PROTOCOL.md）：
//
//	guest 模式（默认，免登录）：POST {base}/api/v1/chat-completion-oversea-V2
//	auth  模式（需登录 Cookie）：POST {base}/api/v1/session-create → POST {base}/api/v1/chat-completion-V2
//	SSE 帧：event.type = create | content | finish，正文增量在 event.content，
//	末帧带 tokenInfo（真实 token 数）。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linquin/longcat2api/internal/config"
)

// LongcatWeb 是 longcat.chat 网页通道客户端。
type LongcatWeb struct {
	cfg *config.Config
	hc  *http.Client

	mu        sync.Mutex
	lastSent  time.Time
	coolUntil time.Time
	sessionID string // auth 模式复用会话
}

// NewLongcatWeb 构建网页通道客户端。
func NewLongcatWeb(cfg *config.Config) *LongcatWeb {
	return &LongcatWeb{
		cfg: cfg,
		hc: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          16,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

// lcFrame 是上游 SSE 的 data 行。
type lcFrame struct {
	Code           *int    `json:"code"`
	Message        string  `json:"message"`
	ConversationID string  `json:"conversationId"`
	Model          string  `json:"model"`
	LastOne        bool    `json:"lastOne"`
	Content        *string `json:"content"`
	ReasonContent  *string `json:"reasonContent"`
	TokenInfo      *struct {
		PromptTokens     int `json:"promptTokens"`
		CompletionTokens int `json:"completionTokens"`
		TotalTokens      int `json:"totalTokens"`
	} `json:"tokenInfo"`
	Event *struct {
		Type          string `json:"type"`
		Content       string `json:"content"`
		Status        string `json:"status"`
		FinalContent  string `json:"finalContentX"`
		SystemPrompt  string `json:"systemPrompt"`
		ReasonContent string `json:"reasonContent"`
		ToolName      string `json:"toolName"`
		ToolCallID    string `json:"toolCallId"`
	} `json:"event"`
}

// Chat 跑一轮网页通道对话。onDelta 收到正文增量，onReason 收到思考增量（可为 nil）。
func (c *LongcatWeb) Chat(ctx context.Context, prompt, model string, onDelta, onReason func(string) error) (*Result, error) {
	lw := c.cfg.LongcatWeb
	if !lw.Enabled {
		return nil, errOf(KindBadRequest, 400, 0, "longcat-web 通道未启用")
	}
	if err := c.gate(ctx); err != nil {
		return nil, err
	}
	res := &Result{Model: model, Status: "completed"}

	var err error
	if config.NormalizeMode(lw.Mode) == "auth" {
		err = c.chatCN(ctx, prompt, res, onDelta, onReason)
	} else {
		err = c.chatOversea(ctx, prompt, res, onDelta, onReason)
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

// SetCookie 运行期更新 Cookie（面板登录后热生效），空串表示清空。
func (c *LongcatWeb) SetCookie(cookie string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.LongcatWeb.Cookie = strings.TrimSpace(cookie)
	if c.cfg.LongcatWeb.Cookie == "" {
		c.sessionID = ""
		return
	}
	// 有 cookie 就走登录态模式
	c.cfg.LongcatWeb.Mode = "auth"
	c.sessionID = "" // 换凭据后会话失效
}

// CookieInfo 返回当前凭据状态（不回传 cookie 本身）。
func (c *LongcatWeb) CookieInfo() (mode string, hasCookie bool, cooling int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := int64(0)
	if d := time.Until(c.coolUntil); d > 0 {
		left = int64(d.Seconds())
	}
	return c.cfg.LongcatWeb.Mode, c.cfg.LongcatWeb.Cookie != "", left
}

// CheckCookie 校验 cookie 对应的 longcat.chat 登录态，返回 uid/昵称/loginStatus。
func (c *LongcatWeb) CheckCookie(ctx context.Context, cookie string) (uid, name string, login int, err error) {
	lw := c.cfg.LongcatWeb
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lw.Base+"/api/v1/user-current", nil)
	if err != nil {
		return "", "", 0, err
	}
	for k, v := range c.headers(false) {
		req.Header.Set(k, v)
	}
	if strings.TrimSpace(cookie) != "" {
		req.Header.Set("cookie", cookie)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", 0, errOf(KindNetwork, 0, 0, "%s", err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code int `json:"code"`
		Data struct {
			UID         any `json:"uid"`
			UserID      any `json:"userId"`
			Name        any `json:"name"`
			LoginStatus int `json:"loginStatus"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", 0, errOf(KindUpstream, resp.StatusCode, 0, "响应解析失败: %s", snippet(raw))
	}
	if out.Code != 0 {
		return "", "", 0, errOf(KindUpstream, resp.StatusCode, out.Code, "%s", out.Message)
	}
	return anyToID(out.Data.UID), anyToName(out.Data.Name), out.Data.LoginStatus, nil
}

// Status 返回通道运行态，供 /healthz 展示。
func (c *LongcatWeb) Status() (enabled bool, mode string, cooldownLeftSec int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	left := int64(0)
	if d := time.Until(c.coolUntil); d > 0 {
		left = int64(d.Seconds())
	}
	return c.cfg.LongcatWeb.Enabled, c.cfg.LongcatWeb.Mode, left
}

// gate 串行节流 + 冷却检查。上游对同一来源限流很凶，这是避免 429 的主要手段。
func (c *LongcatWeb) gate(ctx context.Context) error {
	c.mu.Lock()
	// 冷却中
	if now := time.Now(); now.Before(c.coolUntil) {
		left := time.Until(c.coolUntil).Round(time.Second)
		c.mu.Unlock()
		return errOf(KindRateLimit, 429, 0, "longcat-web 冷却中，还需 %s（上游限流）", left)
	}
	// 最小间隔
	wait := time.Duration(c.cfg.LongcatWeb.MinIntervalMS)*time.Millisecond - time.Since(c.lastSent)
	c.mu.Unlock()
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	c.mu.Lock()
	c.lastSent = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *LongcatWeb) markRateLimited() {
	d := 90 * time.Second
	if s := c.cfg.LongcatWeb.Cooldown; s != "" {
		if v, err := time.ParseDuration(s); err == nil && v > 0 {
			d = v
		}
	}
	c.mu.Lock()
	c.coolUntil = time.Now().Add(d)
	c.mu.Unlock()
}

func (c *LongcatWeb) headers(cookie bool) map[string]string {
	lw := c.cfg.LongcatWeb
	h := map[string]string{
		"accept":            "text/event-stream,application/json",
		"accept-language":   "zh-CN,zh;q=0.9,en;q=0.8",
		"content-type":      "application/json",
		"m-appkey":          lw.AppKey,
		"m-traceid":         fmt.Sprintf("%d%d", time.Now().UnixMilli(), rand.Intn(1_000_000)),
		"origin":            lw.Base,
		"referer":           lw.Base + "/t",
		"user-agent":        lw.UserAgent,
		"x-client-language": "zh",
		"x-requested-with":  "XMLHttpRequest",
	}
	if cookie && lw.Cookie != "" {
		h["cookie"] = lw.Cookie
	}
	return h
}

func lcMsgID() int64 { return 10_000_000 + rand.Int63n(89_999_999) }

// lcMessages 按上游要求拼 messages（首条 user 载正文，末条 assistant 占位等待生成）。
func lcMessages(prompt string) []map[string]any {
	return []map[string]any{
		{
			"role": "user",
			"events": []map[string]any{
				{"type": "userMsg", "content": prompt, "status": "FINISHED"},
			},
			"chatStatus": "FINISHED",
			"messageId":  lcMsgID(),
			"idType":     "custom",
		},
		{
			"role":       "assistant",
			"events":     []map[string]any{},
			"chatStatus": "LOADING",
			"messageId":  lcMsgID(),
			"idType":     "custom",
		},
	}
}

func (c *LongcatWeb) chatOversea(ctx context.Context, prompt string, res *Result, onDelta, onReason func(string) error) error {
	lw := c.cfg.LongcatWeb
	body := map[string]any{
		"content":       prompt,
		"agentId":       lw.AgentID,
		"messages":      lcMessages(prompt),
		"reasonEnabled": boolToInt(lw.ReasonEnabled),
		"searchEnabled": boolToInt(lw.SearchEnabled),
		"regenerate":    0,
	}
	return c.doStream(ctx, lw.Base+"/api/v1/chat-completion-oversea-V2", body, false, res, onDelta, onReason)
}

func (c *LongcatWeb) chatCN(ctx context.Context, prompt string, res *Result, onDelta, onReason func(string) error) error {
	lw := c.cfg.LongcatWeb
	if lw.Cookie == "" {
		return errOf(KindAuth, 401, 0, "longcat-web auth 模式需要 cookie（面板点『扫码登录』或粘贴 Cookie）")
	}
	convID, err := c.ensureSession(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"content":         prompt,
		"conversationId":  convID,
		"agentId":         lw.AgentID,
		"reasonEnabled":   boolToInt(lw.ReasonEnabled),
		"searchEnabled":   boolToInt(lw.SearchEnabled),
		"regenerate":      0,
		"parentMessageId": 0,
		"files":           []any{},
	}
	res.ConversationID = convID
	return c.doStream(ctx, lw.Base+"/api/v1/chat-completion-V2", body, true, res, onDelta, onReason)
}

// ensureSession 为 auth 模式创建（并缓存）会话。
func (c *LongcatWeb) ensureSession(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.sessionID != "" {
		id := c.sessionID
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	lw := c.cfg.LongcatWeb
	payload, _ := json.Marshal(map[string]any{"model": "", "agentId": lw.AgentID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lw.Base+"/api/v1/session-create", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	for k, v := range c.headers(true) {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", errOf(KindNetwork, 0, 0, "session-create 失败: %s", err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Data struct {
			ConversationID string `json:"conversationId"`
		} `json:"data"`
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Data.ConversationID == "" {
		if out.Code != nil && *out.Code == 429 {
			c.markRateLimited()
			return "", errOf(KindRateLimit, 429, 429, "session-create 限流: %s", out.Message)
		}
		return "", errOf(KindUpstream, resp.StatusCode, 0, "session-create 未返回 conversationId: %s", snippet(raw))
	}
	c.mu.Lock()
	c.sessionID = out.Data.ConversationID
	c.mu.Unlock()
	return out.Data.ConversationID, nil
}

func (c *LongcatWeb) doStream(ctx context.Context, url string, body map[string]any, withCookie bool, res *Result, onDelta, onReason func(string) error) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for k, v := range c.headers(withCookie) {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errOf(KindNetwork, 0, 0, "%s", err.Error())
	}
	defer resp.Body.Close()

	// 上游即使限流也常返回 HTTP 200 + JSON body，需要看 content-type / 首字节
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return c.classifyBody(resp.StatusCode, raw)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.classifyBody(resp.StatusCode, nil)
	}

	err = readLines(resp.Body, func(line string) error {
		if !strings.HasPrefix(line, "data:") {
			return nil
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if p == "" || p == "[DONE]" {
			return nil
		}
		var f lcFrame
		if err := json.Unmarshal([]byte(p), &f); err != nil {
			return nil
		}
		// 错误体（限流等）直出
		if f.Code != nil && *f.Code != 0 && *f.Code != 200 {
			if *f.Code == 429 {
				c.markRateLimited()
				return errOf(KindRateLimit, 429, 429, "%s", f.Message)
			}
			return errOf(KindUpstream, 200, *f.Code, "%s", f.Message)
		}
		if f.Model != "" {
			res.Model = f.Model
		}
		if f.ConversationID != "" {
			res.ConversationID = f.ConversationID
		}
		if f.TokenInfo != nil {
			res.PromptTokens = f.TokenInfo.PromptTokens
			res.CompletionTokens = f.TokenInfo.CompletionTokens
		}
		ev := f.Event
		if ev != nil {
			switch ev.Type {
			case "content":
				if ev.Content != "" {
					res.Text += ev.Content
					if onDelta != nil {
						if err := onDelta(ev.Content); err != nil {
							return errStopRead
						}
					}
				}
			case "reason", "reasonContent":
				if ev.Content != "" && onReason != nil {
					if err := onReason(ev.Content); err != nil {
						return errStopRead
					}
				}
			case "finish":
				if ev.FinalContent != "" && res.Text == "" {
					// 只有增量没收到时用最终正文兜底
					res.Text = ev.FinalContent
					if onDelta != nil {
						_ = onDelta(ev.FinalContent)
					}
				}
				res.Status = "completed"
				if f.LastOne {
					return errStopRead
				}
			case "error":
				return errOf(KindUpstream, 200, 0, "上游错误: %s", ev.Content)
			}
		}
		if f.LastOne {
			res.Status = "completed"
			return errStopRead
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopRead) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errOf(KindNetwork, 0, 0, "事件流中断: %s", err)
	}
	return nil
}

func (c *LongcatWeb) classifyBody(status int, raw []byte) error {
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &e)
	}
	switch {
	case e.Code == 429 || status == 429:
		c.markRateLimited()
		return errOf(KindRateLimit, status, e.Code, "上游限流: %s", orDefault(e.Message, "操作太快啦"))
	case status == 401 || status == 403 || e.Code == 401:
		return errOf(KindAuth, status, e.Code, "鉴权失败: %s", orDefault(e.Message, snippet(raw)))
	case status >= 500:
		return errOf(KindUpstream, status, e.Code, "上游异常: %s", orDefault(e.Message, snippet(raw)))
	case status >= 400:
		return errOf(KindBadRequest, status, e.Code, "请求被拒绝: %s", orDefault(e.Message, snippet(raw)))
	}
	return errOf(KindUpstream, status, e.Code, "非预期响应: %s", orDefault(e.Message, snippet(raw)))
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// anyToID 把 JSON number/string 统一成可读 id（避免 float64 变成科学计数法）
func anyToID(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func anyToName(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return anyToID(v)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
