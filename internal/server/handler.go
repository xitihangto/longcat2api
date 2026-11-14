// Package server 暴露 OpenAI 兼容接口，并把请求转成妙手协议。
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/pool"
	"github.com/linquin/longcat2api/internal/upstream"
)

//go:embed panel.html
var panelHTML []byte

// Server HTTP 服务。
type Server struct {
	cfg     *config.Config
	pool    *pool.Pool
	up      *upstream.Client
	mux     *http.ServeMux
	Version string
}

// New 构建服务。
func New(cfg *config.Config, p *pool.Pool, up *upstream.Client, version string) *Server {
	s := &Server{cfg: cfg, pool: p, up: up, mux: http.NewServeMux(), Version: version}
	s.routes()
	return s
}

// Handler 返回 http.Handler。
func (s *Server) Handler() http.Handler {
	// 每个响应都带版本号：排查「镜像是新版还是旧版」一眼就知道
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Longcat-Version", s.Version)
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/v1/models", s.auth(s.handleModels))
	s.mux.HandleFunc("/v1/chat/completions", s.auth(s.handleChat))
	s.mux.HandleFunc("/panel/api/overview", s.auth(s.handleOverview))
	s.mux.HandleFunc("/panel/api/accounts", s.auth(s.handleAccounts))
	s.mux.HandleFunc("/panel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(panelHTML)
	})
	s.mux.HandleFunc("/", s.handleRoot)
}

// ---------------- 中间件 ----------------

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey != "" {
			tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if tok == "" {
				tok = r.Header.Get("x-api-key")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.APIKey)) != 1 {
				writeErr(w, http.StatusUnauthorized, "invalid_api_key", "API key 无效")
				return
			}
		}
		next(w, r)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": code, "code": code},
	})
}

// ---------------- 基础接口 ----------------

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not_found", "路径不存在")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "longcat2api · 妙手(CatPaw) → OpenAI 兼容网关\n\n")
	fmt.Fprintf(w, "POST /v1/chat/completions\nGET  /v1/models\nGET  /healthz\nGET  /panel\n")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := "ok"
	if s.pool == nil {
		health = "degraded"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":           health,
		"service":          "longcat2api",
		"version":          s.Version,
		"accounts":         s.cfgAccountCount(),
		"api_key_required": s.cfg.APIKey != "",
		"panel_key_hint":   "/panel?key=<api_key>",
		"time":             time.Now().Format(time.RFC3339),
	})
}

func (s *Server) cfgAccountCount() int {
	if s.pool == nil {
		return 0
	}
	n := 0
	for _, a := range s.pool.Snapshot() {
		if a.Healthy {
			n++
		}
	}
	return n
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type m struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	out := make([]m, 0, len(s.cfg.Models.Catalog))
	now := time.Now().Unix()
	for _, e := range s.cfg.Models.Catalog {
		out = append(out, m{ID: e.Name, Object: "model", Created: now, OwnedBy: "catpaw"})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": out})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	accs := s.pool.Snapshot()
	healthy := 0
	for _, a := range accs {
		if a.Healthy {
			healthy++
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service":        "longcat2api",
		"version":        s.Version,
		"model":          s.cfg.Upstream.Model,
		"chat_base":      s.cfg.Upstream.ChatBase,
		"stream_base":    s.cfg.Upstream.StreamBase,
		"accounts_total": len(accs),
		"accounts_ok":    healthy,
		"models":         s.cfg.Models.Catalog,
	})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	type view struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		UID      string `json:"uid"`
		Enabled  bool   `json:"enabled"`
		Healthy  bool   `json:"healthy"`
		Cooldown int64  `json:"cooldown_left"`
		InFlight int    `json:"in_flight"`
		LastErr  string `json:"last_error,omitempty"`
	}
	out := []view{}
	for _, a := range s.pool.Snapshot() {
		out = append(out, view{
			ID: a.ID, Name: a.Name, UID: a.UID, Enabled: a.Enabled,
			Healthy: a.Healthy, Cooldown: a.CooldownLeft, InFlight: a.InFlight, LastErr: a.LastError,
		})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// ---------------- Chat Completions ----------------

type chatRequest struct {
	Model         string      `json:"model"`
	Messages      []chatMsgIn `json:"messages"`
	Stream        bool        `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

type chatMsgIn struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (m chatMsgIn) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func (s *Server) resolveModel(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return s.cfg.Upstream.Model, nil
	}
	for _, h := range s.cfg.Models.Hidden {
		if strings.EqualFold(h, name) {
			return "", fmt.Errorf("模型 %s 不可用", name)
		}
	}
	for _, e := range s.cfg.Models.Catalog {
		if strings.EqualFold(e.Name, name) {
			return e.Upstream, nil
		}
	}
	if up, ok := s.cfg.Models.Alias[strings.ToLower(name)]; ok {
		return up, nil
	}
	// 未知模型：直接透传（上游可能支持新模型）
	return name, nil
}

// buildPrompt 把 OpenAI messages 压成一个上游可接受的正文。
//
// 妙手 Agent 协议单轮只接受一条 user 文本，因此这里把 system 与历史对话
// 拼进同一条消息里；单轮纯提问时正文与用户输入完全一致。
func buildPrompt(msgs []chatMsgIn) (string, error) {
	var system []string
	type turn struct{ role, text string }
	var turns []turn
	for _, m := range msgs {
		t := strings.TrimSpace(m.text())
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if t != "" {
				system = append(system, t)
			}
		case "user", "assistant":
			if t != "" {
				turns = append(turns, turn{role: strings.ToLower(m.Role), text: t})
			}
		}
	}
	if len(turns) == 0 {
		return "", errors.New("messages 中没有可用的 user 内容")
	}
	if len(turns) == 1 && turns[0].role == "user" && len(system) == 0 {
		return turns[0].text, nil
	}
	var b strings.Builder
	if len(system) > 0 {
		b.WriteString("以下是系统设定，请在后续回答中始终遵守：\n")
		b.WriteString(strings.Join(system, "\n\n"))
		b.WriteString("\n\n")
	}
	last := turns[len(turns)-1]
	if len(turns) > 1 {
		b.WriteString("以下是此前的对话记录：\n")
		for _, t := range turns[:len(turns)-1] {
			if t.role == "user" {
				b.WriteString("用户：")
			} else {
				b.WriteString("助手：")
			}
			b.WriteString(t.text)
			b.WriteString("\n")
		}
		b.WriteString("\n请回答用户最后一条消息：\n")
	}
	b.WriteString(last.text)
	return b.String(), nil
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// estimateTokens 粗估 token 数（中英混排），仅用于给客户端一个量级参考。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		if r > 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	n := cjk + other/4
	if n < 1 {
		n = 1
	}
	return n
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	var req chatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体解析失败: "+err.Error())
		return
	}
	model, err := s.resolveModel(req.Model)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "model_not_found", err.Error())
		return
	}
	prompt, err := buildPrompt(req.Messages)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	timeout := time.Duration(s.cfg.Upstream.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// 选号 + 最多重试其它账号一次
	var sw *streamWriter
	var onDelta func(string) error
	if req.Stream {
		sw = newStreamWriter(w, model)
		onDelta = sw.write
	}
	var res *upstream.Result
	var used *account.Account
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		acc, err := s.pool.Acquire()
		if err != nil {
			if used == nil {
				writeErr(w, http.StatusServiceUnavailable, "no_account", err.Error())
				return
			}
			lastErr = err
			break
		}
		used = acc
		start := time.Now()
		res, lastErr = s.up.Chat(ctx, acc, prompt, model, onDelta)
		s.pool.Release(acc)
		s.pool.Report(acc, lastErr)
		if s.cfg.Logging.LogRequests {
			status := "ok"
			if lastErr != nil {
				status = "err: " + lastErr.Error()
			}
			log.Printf("chat acc=%s model=%s dur=%s status=%s", acc.ID, model, time.Since(start).Round(time.Millisecond), status)
		}
		if lastErr == nil {
			break
		}
		var ue *upstream.Error
		if errors.As(lastErr, &ue) && ue.Kind == upstream.KindBadRequest {
			break // 请求本身有问题，换号无用
		}
		if ctx.Err() != nil || (sw != nil && sw.started) {
			break // 已经开始向客户端输出，不再换号重试
		}
	}
	if lastErr != nil && (res == nil || res.Text == "") && (sw == nil || !sw.started) {
		status := http.StatusBadGateway
		var ue *upstream.Error
		if errors.As(lastErr, &ue) {
			switch ue.Kind {
			case upstream.KindRateLimit, upstream.KindQuota:
				status = http.StatusTooManyRequests
			case upstream.KindBadRequest:
				status = http.StatusBadRequest
			}
		}
		if errors.Is(lastErr, pool.ErrNoAccount) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, "upstream_error", lastErr.Error())
		return
	}

	outModel := model
	if res != nil && res.Model != "" {
		outModel = res.Model
	}
	text := ""
	if res != nil {
		text = res.Text
	}
	if req.Stream {
		sw.model = outModel
		if !sw.started {
			if err := sw.write(text); err != nil {
				return
			}
		}
		sw.finish(prompt, text)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      newID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   outModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     estimateTokens(prompt),
			"completion_tokens": estimateTokens(text),
			"total_tokens":      estimateTokens(prompt) + estimateTokens(text),
		},
	})
}

// streamWriter 把上游增量实时转成 OpenAI SSE chunk。
// 首帧到达前不写响应头，这样早期错误还能返回标准 JSON 错误体。
type streamWriter struct {
	h       http.ResponseWriter
	flusher http.Flusher
	model   string
	id      string
	created int64
	started bool
}

func newStreamWriter(w http.ResponseWriter, model string) *streamWriter {
	f, _ := w.(http.Flusher)
	return &streamWriter{h: w, flusher: f, model: model, id: newID(), created: time.Now().Unix()}
}

func (sw *streamWriter) ensure() {
	if sw.started {
		return
	}
	h := sw.h.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	sw.h.WriteHeader(http.StatusOK)
	sw.started = true
	sw.send(map[string]any{"role": "assistant", "content": ""}, nil)
}

func (sw *streamWriter) send(delta map[string]any, finish any) {
	chunk := map[string]any{
		"id": sw.id, "object": "chat.completion.chunk", "created": sw.created, "model": sw.model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(sw.h, "data: %s\n\n", b)
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
}

func (sw *streamWriter) write(delta string) error {
	if delta == "" {
		return nil
	}
	sw.ensure()
	sw.send(map[string]any{"content": delta}, nil)
	return nil
}

func (sw *streamWriter) finish(prompt, text string) {
	sw.ensure()
	sw.send(map[string]any{}, "stop")
	usage := map[string]any{
		"prompt_tokens":     estimateTokens(prompt),
		"completion_tokens": estimateTokens(text),
		"total_tokens":      estimateTokens(prompt) + estimateTokens(text),
	}
	b, _ := json.Marshal(map[string]any{
		"id": sw.id, "object": "chat.completion.chunk", "created": sw.created, "model": sw.model,
		"choices": []map[string]any{}, "usage": usage,
	})
	fmt.Fprintf(sw.h, "data: %s\n\n", b)
	fmt.Fprint(sw.h, "data: [DONE]\n\n")
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
}
