// Package config 负责加载与生成 longcat2api 的配置。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config 是网关的全部配置，字段与 config.json 一一对应。
type Config struct {
	Listen       string   `json:"listen"`
	APIKey       string   `json:"api_key"`
	AccountsFile string   `json:"accounts_file"`
	StateFile    string   `json:"state_file"`
	Upstream     Upstream `json:"upstream"`
	Cooldown     Cooldown `json:"cooldown"`
	Models       Models   `json:"models"`
	Logging      Logging  `json:"logging"`
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
	Desc     string `json:"desc"`
}

// Logging 日志开关。
type Logging struct {
	LogRequests  bool `json:"log_requests"`
	LogFrameSize bool `json:"log_frame_debug"`
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
		Cooldown: Cooldown{SoftRate: "600s", SoftRateMax: "2h", AuthFail: "30m"},
		Models: Models{
			Catalog: []ModelEntry{
				{Name: "LongCat-2.0", Upstream: "LongCat-2.0", Desc: "美团 LongCat 2.0（妙手默认模型）"},
			},
			Alias: map[string]string{
				"longcat":     "LongCat-2.0",
				"longcat-2.0": "LongCat-2.0",
				"auto":        "LongCat-2.0",
				"catpaw":      "LongCat-2.0",
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
			if err := Save(path, cfg); err != nil {
				return nil, fmt.Errorf("生成默认配置失败: %w", err)
			}
			return cfg, nil
		}
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("配置解析失败 (%s): %w", path, err)
	}
	cfg.fill()
	return cfg, nil
}

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
