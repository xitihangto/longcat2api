// Package config 负责加载与生成 longcat2api 的配置。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是网关的全部配置，字段与 config.json 一一对应。
type Config struct {
	Listen       string     `json:"listen"`
	APIKey       string     `json:"api_key"`
	AccountsFile string     `json:"accounts_file"`
	StateFile    string     `json:"state_file"`
	Upstream     Upstream   `json:"upstream"`
	LongcatWeb   LongcatWeb `json:"longcat_web"`
	Cooldown     Cooldown   `json:"cooldown"`
	Models       Models     `json:"models"`
	Logging      Logging    `json:"logging"`
}

// Upstream 描述妙手（CatPaw）上游的地址与协议常量。
// 这些值来自客户端逆向，见 docs/PROTOCOL.md。
type Upstream struct {
	// ChatBase 是会话编排服务（/api/chat/*），生产为 https://nocode.cn
	ChatBase string `json:"chat_base"`
	// StreamBase 是 Agent 直连事件流服务（/api/agent/stream/connect），
	// 对外个人版生产为 https://ai.catpaw.meituan.com
	StreamBase string `json:"stream_base"`
	// GatewayBase 是网关 REST（/api/gateway/*），鉴权头为 X-Auth-Token。
	// 生产为 https://catx.nocode.cn
	GatewayBase string `json:"gateway_base"`
	// Model 是下发给上游的模型名；服务端实际会回填真实模型（LongCat-2.0）。
	Model string `json:"model"`
	// SourcePlatform / TechStackTemplateID 是上游协议字段，保持与官方桌面端一致。
	SourcePlatform      string `json:"source_platform"`
	TechStackTemplateID string `json:"tech_stack_template_id"`
	// ChatSource 是 /api/chat/create 的 source 字段。
	ChatSource string `json:"chat_source"`
	// TimeoutSeconds 单轮对话整体超时。
	TimeoutSeconds int `json:"timeout_seconds"`
	// FirstByteSeconds 首帧超时（超过则认为该账号异常）。
	FirstByteSeconds int `json:"first_byte_seconds"`
	// ConnectRetries 事件流中断但未收到终态帧时的重连次数。
	ConnectRetries int `json:"connect_retries"`
	// UserAgent 出站 UA，留空用默认。
	UserAgent string `json:"user_agent"`
}

// LongcatWeb 是 longcat.chat 网页通道（免费 LongCat-2.5）。
//
// 协议见 docs/PROTOCOL.md「longcat.chat 网页通道」。
type LongcatWeb struct {
	// Enabled 打开该通道（默认 true：免登录即可用）
	Enabled bool `json:"enabled"`
	// Base 站点地址
	Base string `json:"base"`
	// AppKey 前端固定 appkey，缺省即官方值
	AppKey string `json:"appkey"`
	// Mode 通道工作方式：
	//   guest —— 免登录游客接口（chat-completion-oversea-V2），默认
	//   auth  —— 带 Cookie 的登录态接口（session-create + chat-completion-V2）
	// 历史别名会自动归一（见 NormalizeMode），老配置无需改动
	Mode string `json:"mode"`
	// Cookie 仅 auth 模式需要：面板「扫码登录」自动获取，或手工粘贴
	Cookie string `json:"cookie"`
	// LoginURL 面板「扫码/邮箱登录」打开的页面。
	// 国际站：https://longcat.ai/chat（passport.mykeeta.com，支持邮箱）
	// 国内站：https://longcat.chat（passport.meituan.com，微信/手机扫码）
	LoginURL string `json:"login_url"`
	// AgentID 上游 agentId，默认 "1"
	AgentID string `json:"agent_id"`
	// ReasonEnabled 让上游输出思考内容（会通过 reasoning_content 回给客户端）
	ReasonEnabled bool `json:"reason_enabled"`
	// SearchEnabled 上游联网搜索
	SearchEnabled bool `json:"search_enabled"`
	// MinIntervalMS 两次请求的最小间隔（毫秒）。上游对同一来源限流很凶，
	// 串行节流是避免 429 的最有效手段。
	MinIntervalMS int `json:"min_interval_ms"`
	// Cooldown 命中 429 后的冷却时长
	Cooldown string `json:"cooldown"`
	// FallbackToCatpaw 该通道失败时是否自动回落到妙手 2.0
	FallbackToCatpaw bool `json:"fallback_to_catpaw"`
	// UserAgent 出站 UA
	UserAgent string `json:"user_agent"`
}

// Cooldown 账号冷却策略。
type Cooldown struct {
	// SoftRate 429/限流类错误的冷却时长，按次指数退避。
	SoftRate string `json:"soft_rate"`
	// SoftRateMax 软冷却上限。
	SoftRateMax string `json:"soft_rate_max"`
	// AuthFail 鉴权失败（401）后的冷却时长。
	AuthFail string `json:"auth_fail"`
}

// Models 对外暴露的模型定义。
type Models struct {
	// Catalog 是 /v1/models 列表；每项 name 对外，upstream 传给上游。
	Catalog []ModelEntry `json:"catalog"`
	// Alias 允许客户端用别名请求，例如 longcat → LongCat-2.0。
	Alias map[string]string `json:"alias"`
	// Hidden 是不对外暴露的模型名（请求到达时直接 400）。
	Hidden []string `json:"hidden"`
}

// ModelEntry 一个对外模型条目。
type ModelEntry struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
	// Provider 决定这条模型走哪条通道：catpaw（妙手账号池）| longcat-web（longcat.chat）
	Provider string `json:"provider"`
	Desc     string `json:"desc"`
}

// Logging 日志开关。
type Logging struct {
	LogRequests  bool `json:"log_requests"`
	LogFrameSize bool `json:"log_frame_debug"`
}

// NormalizeMode 把历史别名收敛成 guest / auth。
func NormalizeMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", "guest", "oversea", "free", "anon":
		return "guest"
	case "auth", "cn", "cookie", "login":
		return "auth"
	default:
		return "guest"
	}
}

// SoftRate / SoftRateMax / AuthFail 的解析值。
func (c Cooldown) SoftRateDur() time.Duration { return parseDur(c.SoftRate, 600*time.Second) }
func (c Cooldown) SoftRateMaxDur() time.Duration {
	return parseDur(c.SoftRateMax, 2*time.Hour)
}
func (c Cooldown) AuthFailDur() time.Duration { return parseDur(c.AuthFail, 30*time.Minute) }

func parseDur(s string, def time.Duration) time.Duration {
	if strings.TrimSpace(s) == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// Default 返回推荐配置。
func Default() *Config {
	return &Config{
		Listen:       ":8090",
		APIKey:       "",
		AccountsFile: "./data/accounts.json",
		StateFile:    "./data/state.json",
		Upstream: Upstream{
			ChatBase:            "https://nocode.cn",
			StreamBase:          "https://ai.catpaw.meituan.com",
			GatewayBase:         "https://catx.nocode.cn",
			Model:               "LongCat-2.0",
			SourcePlatform:      "web",
			TechStackTemplateID: "default",
			ChatSource:          "catdesk",
			TimeoutSeconds:      300,
			FirstByteSeconds:    45,
			ConnectRetries:      3,
			UserAgent:           "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) CatPaw/2026.1110.1503 Chrome/140.0.0.0 Safari/537.36",
		},
		LongcatWeb: LongcatWeb{
			Enabled:          true,
			Base:             "https://longcat.ai",
			LoginURL:         "https://longcat.ai/chat",
			AppKey:           "fe_com.sankuai.friday.fe.longcat",
			Mode:             "guest",
			AgentID:          "1",
			MinIntervalMS:    1500,
			Cooldown:         "90s",
			FallbackToCatpaw: true,
			UserAgent:        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
		Cooldown: Cooldown{SoftRate: "600s", SoftRateMax: "2h", AuthFail: "30m"},
		Models: Models{
			Catalog: []ModelEntry{
				{Name: "LongCat-2.5", Upstream: "LongCat-2.5", Provider: "longcat-web", Desc: "美团 LongCat 2.5（longcat.chat 免费通道，免登录）"},
				{Name: "LongCat-2.0", Upstream: "LongCat-2.0", Provider: "catpaw", Desc: "美团 LongCat 2.0（妙手 CatPaw 账号池）"},
			},
			Alias: map[string]string{
				"longcat":     "LongCat-2.0",
				"longcat-2.0": "LongCat-2.0",
				"longcat2.0":  "LongCat-2.0",
				"catpaw":      "LongCat-2.0",
				"longcat-2.5": "LongCat-2.5",
				"longcat2.5":  "LongCat-2.5",
				"2.5":         "LongCat-2.5",
				"web":         "LongCat-2.5",
			},
			Hidden: []string{},
		},
		Logging: Logging{LogRequests: true},
	}
}

// Load 读取配置文件；文件不存在时写入推荐配置并返回它。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg := Default()
			// 写盘失败不致命：容器挂载目录可能只读，此时用环境变量 + 内置默认值运行。
			if err := Save(path, cfg); err != nil {
				log.Printf("⚠️  无法写入默认配置 %s（%v），本次使用内置默认值运行", path, err)
			}
			cfg.applyEnv()
			return cfg, nil
		}
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("配置解析失败 (%s): %w", path, err)
	}
	cfg.fill()
	cfg.applyEnv()
	return cfg, nil
}

// applyEnv 用环境变量覆盖配置，容器部署时无需改文件。
//
// 支持：LONGCAT_LISTEN / LONGCAT_API_KEY / LONGCAT_MODEL /
// LONGCAT_CHAT_BASE / LONGCAT_STREAM_BASE / LONGCAT_GATEWAY_BASE /
// LONGCAT_ACCOUNTS_FILE / LONGCAT_STATE_FILE /
// LONGCAT_TIMEOUT_SECONDS / LONGCAT_FIRST_BYTE_SECONDS / LONGCAT_LOG_REQUESTS
func (c *Config) applyEnv() {
	str := func(key string, dst *string) {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			*dst = v
		}
	}
	str("LONGCAT_LISTEN", &c.Listen)
	str("LONGCAT_API_KEY", &c.APIKey)
	str("LONGCAT_MODEL", &c.Upstream.Model)
	str("LONGCAT_CHAT_BASE", &c.Upstream.ChatBase)
	str("LONGCAT_STREAM_BASE", &c.Upstream.StreamBase)
	str("LONGCAT_GATEWAY_BASE", &c.Upstream.GatewayBase)
	str("LONGCAT_ACCOUNTS_FILE", &c.AccountsFile)
	str("LONGCAT_STATE_FILE", &c.StateFile)
	str("LONGCAT_USER_AGENT", &c.Upstream.UserAgent)
	if v := strings.TrimSpace(os.Getenv("LONGCAT_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Upstream.TimeoutSeconds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_FIRST_BYTE_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Upstream.FirstByteSeconds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_WEB_ENABLED")); v != "" {
		c.LongcatWeb.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_WEB_BASE")); v != "" {
		c.LongcatWeb.Base = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_WEB_MODE")); v != "" {
		c.LongcatWeb.Mode = NormalizeMode(v)
	}
	if v := os.Getenv("LONGCAT_WEB_COOKIE"); strings.TrimSpace(v) != "" {
		c.LongcatWeb.Cookie = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_WEB_LOGIN_URL")); v != "" {
		c.LongcatWeb.LoginURL = v
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_WEB_MIN_INTERVAL_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.LongcatWeb.MinIntervalMS = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LONGCAT_LOG_REQUESTS")); v != "" {
		c.Logging.LogRequests = v == "1" || strings.EqualFold(v, "true")
	}
	c.Upstream.ChatBase = strings.TrimRight(c.Upstream.ChatBase, "/")
	c.Upstream.StreamBase = strings.TrimRight(c.Upstream.StreamBase, "/")
	c.Upstream.GatewayBase = strings.TrimRight(c.Upstream.GatewayBase, "/")
}

// TokenFromEnv 返回环境变量里预置的 token（容器首次启动用）。
func TokenFromEnv() string { return strings.TrimSpace(os.Getenv("LONGCAT_TOKEN")) }

// Save 原子写配置。
func Save(path string, cfg *Config) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fill 补齐空字段，保证手工改坏的配置也能跑。
func (c *Config) fill() {
	d := Default()
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.AccountsFile == "" {
		c.AccountsFile = d.AccountsFile
	}
	if c.StateFile == "" {
		c.StateFile = d.StateFile
	}
	if c.Upstream.ChatBase == "" {
		c.Upstream.ChatBase = d.Upstream.ChatBase
	}
	if c.Upstream.StreamBase == "" {
		c.Upstream.StreamBase = d.Upstream.StreamBase
	}
	if c.Upstream.GatewayBase == "" {
		c.Upstream.GatewayBase = d.Upstream.GatewayBase
	}
	if c.Upstream.Model == "" {
		c.Upstream.Model = d.Upstream.Model
	}
	if c.Upstream.SourcePlatform == "" {
		c.Upstream.SourcePlatform = d.Upstream.SourcePlatform
	}
	if c.Upstream.TechStackTemplateID == "" {
		c.Upstream.TechStackTemplateID = d.Upstream.TechStackTemplateID
	}
	if c.Upstream.ChatSource == "" {
		c.Upstream.ChatSource = d.Upstream.ChatSource
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = d.Upstream.TimeoutSeconds
	}
	if c.Upstream.FirstByteSeconds <= 0 {
		c.Upstream.FirstByteSeconds = d.Upstream.FirstByteSeconds
	}
	if c.Upstream.ConnectRetries <= 0 {
		c.Upstream.ConnectRetries = d.Upstream.ConnectRetries
	}
	lw := &c.LongcatWeb
	if lw.Base == "" {
		lw.Base = d.LongcatWeb.Base
	}
	if lw.AppKey == "" {
		lw.AppKey = d.LongcatWeb.AppKey
	}
	lw.Mode = NormalizeMode(lw.Mode)
	if lw.AgentID == "" {
		lw.AgentID = d.LongcatWeb.AgentID
	}
	if lw.MinIntervalMS <= 0 {
		lw.MinIntervalMS = d.LongcatWeb.MinIntervalMS
	}
	if lw.Cooldown == "" {
		lw.Cooldown = d.LongcatWeb.Cooldown
	}
	if lw.LoginURL == "" {
		lw.LoginURL = strings.TrimRight(lw.Base, "/") + "/chat"
	}
	if lw.UserAgent == "" {
		lw.UserAgent = d.LongcatWeb.UserAgent
	}
	lw.Base = strings.TrimRight(lw.Base, "/")
	if len(c.Models.Catalog) == 0 {
		c.Models.Catalog = d.Models.Catalog
	}
	if c.Models.Alias == nil {
		c.Models.Alias = d.Models.Alias
	}
	c.Upstream.ChatBase = strings.TrimRight(c.Upstream.ChatBase, "/")
	c.Upstream.StreamBase = strings.TrimRight(c.Upstream.StreamBase, "/")
	c.Upstream.GatewayBase = strings.TrimRight(c.Upstream.GatewayBase, "/")
}
