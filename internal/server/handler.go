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
	"github.com/linquin/longcat2api/internal/paste"
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
	lw      *upstream.LongcatWeb
	mux     *http.ServeMux
	Version string
	cfgPath string
}

// New 构建服务。
func New(cfg *config.Config, p *pool.Pool, up *upstream.Client, lw *upstream.LongcatWeb, version, cfgPath string) *Server {
	s := &Server{cfg: cfg, pool: p, up: up, lw: lw, mux: http.NewServeMux(), Version: version, cfgPath: cfgPath}
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
	s.mux.HandleFunc("/panel/api/accounts/import", s.auth(s.handleAccountImport))
	s.mux.HandleFunc("/panel/api/channel", s.auth(s.handleChannelStatus))
	s.mux.HandleFunc("/panel/api/channel/login", s.auth(s.handleChannelLogin))
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
		"channels":         s.channels(),
		"time":             time.Now().Format(time.RFC3339),
	})
}

// channels 返回两条通道的运行态。
func (s *Server) channels() map[string]any {
	out := map[string]any{}
	out["catpaw"] = map[string]any{
		"enabled": s.pool != nil,
		"models":  s.modelsOf("catpaw"),
	}
	if s.lw != nil {
		en, mode, left := s.lw.Status()
		out["longcat_web"] = map[string]any{
			"enabled":         en,
			"mode":            mode,
			"cooldown_left_s": left,
			"models":          s.modelsOf("longcat-web"),
		}
	}
	return out
}

// modelsOf 返回某条通道承载的模型名。
func (s *Server) modelsOf(provider string) []string {
	var out []string
	for _, e := range s.cfg.Models.Catalog {
		if e.Provider == provider {
			out = append(out, e.Name)
		}
	}
	return out
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
		ID       string `json:"id"`
		Object   string `json:"object"`
		Created  int64  `json:"created"`
		OwnedBy  string `json:"owned_by"`
		Provider string `json:"provider,omitempty"`
	}
	out := make([]m, 0, len(s.cfg.Models.Catalog))
	now := time.Now().Unix()
	for _, e := range s.cfg.Models.Catalog {
		owner := e.Provider
		if owner == "" {
			owner = "catpaw"
		}
		out = append(out, m{ID: e.Name, Object: "model", Created: now, OwnedBy: owner, Provider: e.Provider})
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
		"accounts_hint":  "妙手账号数；longcat 的 Cookie 属于通道凭据，不计入账号池",
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

// ---------------- 妙手账号导入（粘贴 token / cURL） ----------------

// handleAccountImport 接收妙手 access token，校验后写入账号池（热生效，无需重启）。
//
// 接受的粘贴形态：
//  1. 裸 token（AgGF...）
//  2. "access-token: xxx" / "X-Auth-Token: xxx" / "Authorization: Bearer xxx"
//  3. 浏览器/Electron DevTools「Copy as cURL」整段（自动从中抠出上述 header）
//  4. 含 "access_token":"xxx" 的 JSON
func (s *Server) handleAccountImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	var body struct {
		Token string `json:"token"`
		Raw   string `json:"raw"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体解析失败: "+err.Error())
		return
	}
	input := strings.TrimSpace(body.Token)
	if input == "" {
		input = strings.TrimSpace(body.Raw)
	}
	if input == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "没有提供 token")
		return
	}
	token, err := paste.ExtractMiaoshouToken(input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_token", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	uid, uname, err := s.up.CheckToken(ctx, token)
	if err != nil {
		var ue *upstream.Error
		if errors.As(err, &ue) && ue.Kind == upstream.KindAuth {
			writeErr(w, http.StatusUnauthorized, "invalid_token", "token 无效或已过期（妙手返回鉴权失败）："+ue.Msg)
			return
		}
		writeErr(w, http.StatusBadGateway, "check_failed", "校验失败: "+err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = uname
	}
	acc := &account.Account{Name: name, Token: token, UID: uid, Enabled: true, Note: "imported via panel"}
	if err := s.pool.Add(acc); err != nil {
		writeErr(w, http.StatusInternalServerError, "save_failed", "写入账号失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "uid": uid, "name": name,
		"accounts_total": s.cfgAccountCount(),
		"token_length":   len(token),
	})
}

// ---------------- longcat-web 通道凭据（面板登录） ----------------

func (s *Server) handleChannelStatus(w http.ResponseWriter, r *http.Request) {
	mode, hasCookie, cooling := s.lw.CookieInfo()
	mode = config.NormalizeMode(mode)
	en, _, _ := s.lw.Status()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"enabled":         en,
		"mode":            mode,
		"has_cookie":      hasCookie,
		"cooldown_left_s": cooling,
		"login_url":       s.cfg.LongcatWeb.LoginURL,
		"models":          s.modelsOf("longcat-web"),
	})
}

// handleChannelLogin 接收 Cookie（整串或浏览器 Copy as cURL 的原文），
// 校验通过后热更新并落盘，无需重启容器。
func (s *Server) handleChannelLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	var body struct {
		Cookie string `json:"cookie"`
		Curl   string `json:"curl"`
		Raw    string `json:"raw"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体解析失败: "+err.Error())
		return
	}
	input := strings.TrimSpace(body.Cookie)
	if input == "" {
		input = strings.TrimSpace(body.Curl)
	}
	if input == "" {
		input = strings.TrimSpace(body.Raw)
	}
	if input == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "没有提供 Cookie")
		return
	}
	cookie, err := paste.ExtractCookie(input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_cookie", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	uid, name, login, err := s.lw.CheckCookie(ctx, cookie)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "check_failed", "校验失败: "+err.Error())
		return
	}
	if login == 0 {
		writeErr(w, http.StatusUnauthorized, "not_logged_in",
			"Cookie 未登录或已过期（loginStatus=0）。请确认在 longcat.chat 已登录，并复制最新的整串 Cookie。")
		return
	}

	s.lw.SetCookie(cookie)
	s.cfg.LongcatWeb.Cookie = cookie
	s.cfg.LongcatWeb.Mode = "auth"
	saved := true
	if s.cfgPath != "" {
		if err := config.Save(s.cfgPath, s.cfg); err != nil {
			saved = false
			log.Printf("⚠️  Cookie 已生效但写配置失败: %v", err)
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"uid":           uid,
		"name":          name,
		"login_status":  login,
		"mode":          "auth",
		"persisted":     saved,
		"cookie_length": len(cookie),
	})
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

// resolveModel 把客户端模型名解析成（上游模型名, 通道）。
// 通道：catpaw（妙手账号池）| longcat-web（longcat.chat 免费 2.5）
func (s *Server) resolveModel(name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return s.cfg.Upstream.Model, "catpaw", nil
	}
	for _, h := range s.cfg.Models.Hidden {
		if strings.EqualFold(h, name) {
			return "", "", fmt.Errorf("模型 %s 不可用", name)
		}
	}
	for _, e := range s.cfg.Models.Catalog {
		if strings.EqualFold(e.Name, name) {
			p := e.Provider
			if p == "" {
				p = "catpaw"
			}
			return e.Upstream, p, nil
		}
	}
	if alias, ok := s.cfg.Models.Alias[strings.ToLower(name)]; ok {
		for _, e := range s.cfg.Models.Catalog {
			if strings.EqualFold(e.Name, alias) {
				p := e.Provider
				if p == "" {
					p = "catpaw"
				}
				return e.Upstream, p, nil
			}
		}
		return alias, "catpaw", nil
	}
	// 未知模型：按 catpaw 透传（妙手会忽略模型名，回它自己的）
	return name, "catpaw", nil
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
	upModel, provider, err := s.resolveModel(req.Model)
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

	var sw *streamWriter
	if req.Stream {
		sw = newStreamWriter(w, upModel)
	}
	var usedChannel string
	onDelta := func(delta string) error {
		if sw == nil {
			return nil
		}
		return sw.write(delta)
	}
	onReason := func(delta string) error {
		if sw == nil {
			return nil
		}
		return sw.writeReason(delta)
	}

	res, err := s.runProvider(ctx, provider, prompt, upModel, &usedChannel, onDelta, onReason)
	if usedChannel != "" {
		w.Header().Set("X-Longcat-Channel", usedChannel)
	}

	if err != nil && (res == nil || res.Text == "") && (sw == nil || !sw.started) {
		status := http.StatusBadGateway
		var ue *upstream.Error
		if errors.As(err, &ue) {
			switch ue.Kind {
			case upstream.KindRateLimit, upstream.KindQuota:
				status = http.StatusTooManyRequests
			case upstream.KindBadRequest:
				status = http.StatusBadRequest
			case upstream.KindAuth:
				status = http.StatusUnauthorized
			}
		}
		if errors.Is(err, pool.ErrNoAccount) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, "upstream_error", err.Error())
		return
	}

	outModel := upModel
	text := ""
	pTok, cTok := 0, 0
	if res != nil {
		if res.Model != "" {
			outModel = res.Model
		}
		text = res.Text
		pTok, cTok = res.PromptTokens, res.CompletionTokens
	}
	if pTok == 0 {
		pTok = estimateTokens(prompt)
	}
	if cTok == 0 {
		cTok = estimateTokens(text)
	}

	if req.Stream {
		sw.model = outModel
		if !sw.started {
			if werr := sw.write(text); werr != nil {
				return
			}
		}
		sw.finishUsage(pTok, cTok)
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
			"prompt_tokens":     pTok,
			"completion_tokens": cTok,
			"total_tokens":      pTok + cTok,
		},
	})
}

// runProvider 按通道分派；longcat-web 失败时可按配置回落妙手。
func (s *Server) runProvider(ctx context.Context, provider, prompt, model string, used *string, onDelta, onReason func(string) error) (*upstream.Result, error) {
	switch provider {
	case "longcat-web":
		res, err := s.lw.Chat(ctx, prompt, model, onDelta, onReason)
		if err == nil {
			*used = "longcat-web"
			s.logChat("longcat-web", model, res, nil)
			return res, nil
		}
		s.logChat("longcat-web", model, res, err)
		if s.cfg.LongcatWeb.FallbackToCatpaw && (res == nil || res.Text == "") && ctx.Err() == nil {
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.KindRateLimit {
				log.Printf("⚠️  longcat-web 限流，已回落到妙手 2.0（客户端请求的是 %s，实际由 CatPaw 生成）", model)
				*used = "catpaw(fallback)"
				return s.runCatpaw(ctx, prompt, s.cfg.Upstream.Model, onDelta)
			}
		}
		return res, err
	default:
		*used = "catpaw"
		return s.runCatpaw(ctx, prompt, model, onDelta)
	}
}

// runCatpaw 走妙手账号池（原逻辑）。
func (s *Server) runCatpaw(ctx context.Context, prompt, model string, onDelta func(string) error) (*upstream.Result, error) {
	var res *upstream.Result
	var used *account.Account
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		acc, err := s.pool.Acquire()
		if err != nil {
			if used == nil {
				return nil, err
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
			log.Printf("chat channel=catpaw acc=%s model=%s dur=%s status=%s", acc.ID, model, time.Since(start).Round(time.Millisecond), status)
		}
		if lastErr == nil {
			break
		}
		var ue *upstream.Error
		if errors.As(lastErr, &ue) && ue.Kind == upstream.KindBadRequest {
			break
		}
		if ctx.Err() != nil || (res != nil && res.Text != "") {
			break
		}
	}
	return res, lastErr
}

func (s *Server) logChat(channel, model string, res *upstream.Result, err error) {
	if !s.cfg.Logging.LogRequests {
		return
	}
	status := "ok"
	if err != nil {
		status = "err: " + err.Error()
	}
	tok := 0
	if res != nil {
		tok = res.PromptTokens + res.CompletionTokens
	}
	log.Printf("chat channel=%s model=%s tokens=%d status=%s", channel, model, tok, status)
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

// writeReason 输出思考内容（reasoning_content），支持该字段的客户端会自动展示
func (sw *streamWriter) writeReason(delta string) error {
	if delta == "" {
		return nil
	}
	sw.ensure()
	sw.send(map[string]any{"reasoning_content": delta}, nil)
	return nil
}

func (sw *streamWriter) finishUsage(pTok, cTok int) {
	sw.finish2(pTok, cTok)
}

func (sw *streamWriter) finish2(pTok, cTok int) {
	sw.ensure()
	sw.send(map[string]any{}, "stop")
	usage := map[string]any{
		"prompt_tokens":     pTok,
		"completion_tokens": cTok,
		"total_tokens":      pTok + cTok,
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
