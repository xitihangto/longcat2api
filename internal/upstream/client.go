// Package upstream 实现妙手（CatPaw）客户端协议的反向调用：
// 创建会话 → 发起一轮对话 → 订阅 Agent 事件流。
//
// 协议细节见 docs/PROTOCOL.md。
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
)

// ErrorKind 上游错误分类，决定冷却策略。
type ErrorKind string

const (
	KindAuth       ErrorKind = "auth"       // 401 / token 失效
	KindRateLimit  ErrorKind = "rate_limit" // 429 / 限流
	KindQuota      ErrorKind = "quota"      // 余额/额度不足
	KindUpstream   ErrorKind = "upstream"   // 5xx / 协议异常
	KindNetwork    ErrorKind = "network"    // 连接失败 / 超时
	KindBadRequest ErrorKind = "bad_request"
)

// Error 上游错误。
type Error struct {
	Kind   ErrorKind
	Status int
	Code   int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream %s (http=%d code=%d): %s", e.Kind, e.Status, e.Code, e.Msg)
}

func errOf(kind ErrorKind, status, code int, format string, args ...any) *Error {
	return &Error{Kind: kind, Status: status, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Client 是妙手上游客户端。
type Client struct {
	cfg *config.Config
	hc  *http.Client
}

// New 构建客户端。
func New(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		hc: &http.Client{
			Timeout: 0, // 由 ctx 控制
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          64,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: time.Duration(cfg.Upstream.FirstByteSeconds) * time.Second,
			},
		},
	}
}

// Result 是一轮对话的结果。
type Result struct {
	ConversationID string
	ChatID         string
	Model          string
	Text           string
	Status         string // completed / canceled / error
	// PromptTokens / CompletionTokens 上游给的真实用量；为 0 表示未知（妙手通道不给）
	PromptTokens     int
	CompletionTokens int
}

// Chat 执行一次完整对话。
// prompt 是发给上游的正文；onDelta 在收到增量文本时回调（可为 nil）。
func (c *Client) Chat(ctx context.Context, acc *account.Account, prompt, model string, onDelta func(string) error) (*Result, error) {
	if acc == nil || acc.Token == "" {
		return nil, errOf(KindAuth, 401, 0, "账号无可用 token")
	}
	if model == "" {
		model = c.cfg.Upstream.Model
	}
	chatID := NewChatID()
	res := &Result{ChatID: chatID, Model: model}

	if err := c.createChat(ctx, acc, chatID, prompt); err != nil {
		return nil, err
	}
	convID, err := c.startTurn(ctx, acc, chatID, prompt, model)
	if err != nil {
		return nil, err
	}
	res.ConversationID = convID
	res.Model = model
	if err := c.connectStream(ctx, acc, convID, res, onDelta); err != nil {
		return res, err
	}
	return res, nil
}

// NewChatID 生成与官方客户端同格式的会话 id：desk-<8hex><8hex>。
func NewChatID() string {
	return "desk-" + randHex(8) + randHex(8)
}

func randHex(n int) string {
	const hexs = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexs[rand.Intn(len(hexs))]
	}
	return string(b)
}

// ---------------- 上游三步 ----------------

type createChatBody struct {
	ChatID              string `json:"chatId"`
	Source              string `json:"source"`
	Prompt              string `json:"prompt"`
	Images              []any  `json:"images"`
	Files               []any  `json:"files"`
	Type                string `json:"type"`
	TechStackTemplateID string `json:"techStackTemplateId"`
	IsAgent             bool   `json:"isAgent"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type chatMessage struct {
	Role       string        `json:"role"`
	Content    []contentPart `json:"content"`
	CreateTime string        `json:"createTime"`
}

type agentStreamBody struct {
	ChatID              string        `json:"chatId"`
	Messages            []chatMessage `json:"messages"`
	Files               []any         `json:"files"`
	Urls                []any         `json:"urls"`
	Model               string        `json:"model"`
	ManualToolName      string        `json:"manualToolName"`
	ToolMessageID       string        `json:"toolMessageId"`
	IsAgent             bool          `json:"isAgent"`
	CurrentPath         string        `json:"currentPath"`
	TechStackTemplateID string        `json:"techStackTemplateId"`
	SourcePlatform      string        `json:"sourcePlatform"`
	IsImportProject     bool          `json:"isImportProject"`
	FrontCreateTime     int64         `json:"frontCreateTime"`
}

type envelope struct {
	Code  int             `json:"code"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"errorMessage"`
}

func (c *Client) createChat(ctx context.Context, acc *account.Account, chatID, prompt string) error {
	body := createChatBody{
		ChatID:              chatID,
		Source:              c.cfg.Upstream.ChatSource,
		Prompt:              truncateRunes(prompt, 200),
		Images:              []any{},
		Files:               []any{},
		Type:                "COMMON",
		TechStackTemplateID: c.cfg.Upstream.TechStackTemplateID,
		IsAgent:             true,
	}
	_, err := c.postJSON(ctx, c.cfg.Upstream.ChatBase+"/api/chat/create", acc, body, nil)
	return err
}

func (c *Client) startTurn(ctx context.Context, acc *account.Account, chatID, prompt, model string) (string, error) {
	body := agentStreamBody{
		ChatID: chatID,
		Messages: []chatMessage{{
			Role:       "user",
			Content:    []contentPart{{Type: "text", Text: prompt}},
			CreateTime: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}},
		Files:               []any{},
		Urls:                []any{},
		Model:               model,
		IsAgent:             true,
		CurrentPath:         "/",
		TechStackTemplateID: c.cfg.Upstream.TechStackTemplateID,
		SourcePlatform:      c.cfg.Upstream.SourcePlatform,
		FrontCreateTime:     time.Now().UnixMilli(),
	}
	var data struct {
		ConversationID  string `json:"conversationId"`
		StreamMessageID string `json:"streamMessageId"`
		Success         bool   `json:"success"`
		ErrorMessage    string `json:"errorMessage"`
	}
	if _, err := c.postJSON(ctx, c.cfg.Upstream.ChatBase+"/api/chat/agent-stream", acc, body, &data); err != nil {
		return "", err
	}
	if data.ConversationID == "" {
		msg := data.ErrorMessage
		if msg == "" {
			msg = "agent-stream 未返回 conversationId"
		}
		return "", errOf(KindUpstream, 502, 0, "%s", msg)
	}
	return data.ConversationID, nil
}

// postJSON 发一个需要 access-token 的 JSON 请求并解包 envelope。
func (c *Client) postJSON(ctx context.Context, url string, acc *account.Account, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	c.decorateUserAgent(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("access-token", acc.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, errOf(KindNetwork, 0, 0, "%s", err.Error())
	}
	defer resp.Body.Close()
	respRaw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, classifyHTTP(resp.StatusCode, respRaw, url)
	}
	var env envelope
	if err := json.Unmarshal(respRaw, &env); err != nil {
		return resp.StatusCode, errOf(KindUpstream, resp.StatusCode, 0, "响应不是 JSON: %s", snippet(respRaw))
	}
	if env.Code != 0 && env.Code != 200 {
		return resp.StatusCode, classifyCode(env.Code, env.Msg, resp.StatusCode)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return resp.StatusCode, errOf(KindUpstream, resp.StatusCode, env.Code, "data 解析失败: %s", err)
		}
	}
	return resp.StatusCode, nil
}

func classifyHTTP(status int, body []byte, url string) *Error {
	switch {
	case status == 401 || status == 403:
		return errOf(KindAuth, status, 0, "鉴权失败: %s", snippet(body))
	case status == 429:
		return errOf(KindRateLimit, status, 0, "限流: %s", snippet(body))
	case status == 400 || status == 422:
		return errOf(KindBadRequest, status, 0, "请求被拒绝: %s", snippet(body))
	case status >= 500:
		return errOf(KindUpstream, status, 0, "上游异常: %s", snippet(body))
	}
	return errOf(KindUpstream, status, 0, "HTTP %d: %s", status, snippet(body))
}

func classifyCode(code int, msg string, status int) *Error {
	lower := strings.ToLower(msg)
	switch {
	case code == 4010 || code == 401 || strings.Contains(lower, "未登录") || strings.Contains(lower, "token is required"):
		return errOf(KindAuth, status, code, "%s", msg)
	case code == 429 || strings.Contains(lower, "频繁") || strings.Contains(lower, "限流"):
		return errOf(KindRateLimit, status, code, "%s", msg)
	case strings.Contains(lower, "积分") || strings.Contains(lower, "额度") || strings.Contains(lower, "余额"):
		return errOf(KindQuota, status, code, "%s", msg)
	}
	return errOf(KindUpstream, status, code, "%s", msg)
}

func (c *Client) decorateUserAgent(req *http.Request) {
	if c.cfg.Upstream.UserAgent != "" {
		req.Header.Set("User-Agent", c.cfg.Upstream.UserAgent)
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// readerCloser 让编译期确认 io.ReadCloser 用法（避免误用）。
var _ io.ReadCloser = (io.ReadCloser)(nil)

// readLines 逐行扫描，兼容超长行（SSE 帧可能很大）。
func readLines(r io.Reader, fn func(line string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if e := fn(strings.TrimRight(line, "\r\n")); e != nil {
				return e
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
