package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 老版本（只有 2.0、没有 provider 字段）的配置升级后必须自动补上 2.5，
// 且不能把内置默认项的 provider 串到用户项上（json 解到预填切片的经典坑）。
func TestLoadMergesCatalogForOldConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	old := `{
	  "listen": ":9999",
	  "models": {
	    "catalog": [{"name": "LongCat-2.0", "upstream": "LongCat-2.0", "desc": "旧配置"}],
	    "alias": {"longcat": "LongCat-2.0"}
	  }
	}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]ModelEntry{}
	for _, e := range cfg.Models.Catalog {
		byName[e.Name] = e
	}
	if len(byName) != 2 {
		t.Fatalf("期望 2 条模型，得到 %d: %+v", len(byName), cfg.Models.Catalog)
	}
	if got := byName["LongCat-2.0"].Provider; got != "catpaw" {
		t.Errorf("老项 provider 应补成 catpaw，得到 %q", got)
	}
	if got := byName["LongCat-2.5"].Provider; got != "longcat-web" {
		t.Errorf("新增项 provider 应为 longcat-web，得到 %q", got)
	}
	if got := byName["LongCat-2.0"].Desc; got != "旧配置" {
		t.Errorf("用户自定义字段被覆盖: %q", got)
	}
	// 别名：文件里的保留 + 内置的补齐
	if cfg.Models.Alias["longcat"] != "LongCat-2.0" {
		t.Errorf("文件别名丢失")
	}
	if cfg.Models.Alias["2.5"] != "LongCat-2.5" {
		t.Errorf("内置别名未补齐，2.5 → %q", cfg.Models.Alias["2.5"])
	}
	// 其它默认值仍要生效
	if cfg.Upstream.ChatBase == "" || cfg.LongcatWeb.Base == "" || cfg.LongcatWeb.Mode != "guest" {
		t.Errorf("默认值未补齐: upstream=%+v longcat_web=%+v", cfg.Upstream, cfg.LongcatWeb)
	}
	if cfg.Listen != ":9999" {
		t.Errorf("文件值未生效: %q", cfg.Listen)
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"": "guest", "guest": "guest", "oversea": "guest", "anon": "guest",
		"auth": "auth", "cn": "auth", "cookie": "auth", "LOGIN": "auth", "怪值": "guest",
	}
	for in, want := range cases {
		if got := NormalizeMode(in); got != want {
			t.Errorf("NormalizeMode(%q) = %q，期望 %q", in, got, want)
		}
	}
}
