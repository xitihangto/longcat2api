package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/upstream"
)

// 面板「登录」成功路径：上游返回 loginStatus=1 → 热更新 + 写入配置文件
func TestChannelLoginSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user-current" {
			t.Errorf("意外请求 %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"uid": 99887766, "name": "tester", "loginStatus": 1},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.APIKey = "sk-test"
	cfg.LongcatWeb.Base = srv.URL
	cfg.LongcatWeb.MinIntervalMS = 0

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("预置配置失败: %v", err)
	}

	s := New(cfg, nil, nil, upstream.NewLongcatWeb(cfg), "test", cfgPath)

	body := `{"cookie":"ssoid=abc; token=def"}`
	req := httptest.NewRequest(http.MethodPost, "/panel/api/channel/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("期望 200，得到 %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ok"] != true || out["mode"] != "auth" || out["persisted"] != true {
		t.Fatalf("返回异常: %s", w.Body.String())
	}
	if out["uid"] != "99887766" {
		t.Errorf("uid 解析错误: %v", out["uid"])
	}

	lw := s.lw

	// 落盘校验：文件里应已有 cookie + auth 模式
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.LongcatWeb.Cookie != "ssoid=abc; token=def" {
		t.Errorf("配置未写入 cookie: %q", saved.LongcatWeb.Cookie)
	}
	if saved.LongcatWeb.Mode != "auth" {
		t.Errorf("配置未切换 auth 模式: %q", saved.LongcatWeb.Mode)
	}

	// 运行态也应为 cn + 有 cookie
	mode, has, _ := lw.CookieInfo()
	if mode != "auth" || !has {
		t.Errorf("运行态未更新: mode=%s has=%v", mode, has)
	}
}

// 未登录：上游 loginStatus=0 → 401，且不能写入任何东西
func TestChannelLoginNotLoggedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"uid": nil, "loginStatus": 0},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.APIKey = ""
	cfg.LongcatWeb.Base = srv.URL
	cfg.LongcatWeb.MinIntervalMS = 0
	s := New(cfg, nil, nil, upstream.NewLongcatWeb(cfg), "test", "")

	req := httptest.NewRequest(http.MethodPost, "/panel/api/channel/login",
		strings.NewReader(`{"cookie":"ssoid=expired"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("期望 401，得到 %d: %s", w.Code, w.Body.String())
	}
	if mode, has, _ := s.lw.CookieInfo(); has || mode != "guest" {
		t.Errorf("失败时不应改动凭据: mode=%s has=%v", mode, has)
	}
}
