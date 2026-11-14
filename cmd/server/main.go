// longcat2api 入口：加载配置、构建账号池与 HTTP 服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/pool"
	"github.com/linquin/longcat2api/internal/server"
	"github.com/linquin/longcat2api/internal/upstream"
)

const appVersion = "0.3.1"

// cfgPathForSave 返回配置文件的绝对路径，供面板热写回。
func cfgPathForSave(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径（不存在时自动生成推荐配置）")
	token := flag.String("token", "", "临时指定妙手 token（等价于 LONGCAT_TOKEN 环境变量），启动时写入账号池")
	version := flag.Bool("version", false, "打印版本号")
	flag.Parse()
	if *version {
		fmt.Println("longcat2api", appVersion)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	store, err := account.Open(cfg.AccountsFile)
	if err != nil {
		log.Fatalf("加载账号失败: %v", err)
	}
	// 容器场景：允许用 -token / LONGCAT_TOKEN 直接注入账号（首次启动即用）。
	if store.Enabled() == 0 {
		tk := strings.TrimSpace(*token)
		if tk == "" {
			tk = config.TokenFromEnv()
		}
		if tk != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			uid, uname, err := upstream.New(cfg).CheckToken(ctx, tk)
			cancel()
			if err != nil {
				log.Fatalf("注入 token 校验失败: %v", err)
			}
			if err := store.Add(&account.Account{Name: uname, Token: tk, UID: uid, Enabled: true, Note: "injected by token"}); err != nil {
				log.Fatalf("写入账号失败: %v", err)
			}
			log.Printf("已注入账号 uid=%s user=%s", uid, uname)
		}
	}
	if store.Enabled() == 0 {
		log.Printf("⚠️  没有可用账号：请执行 `go run ./cmd/login`（本机客户端）或 `-token <妙手token>` 导入，然后重启")
	}

	p := pool.New(store, cfg)
	up := upstream.New(cfg)
	lw := upstream.NewLongcatWeb(cfg)
	srv := server.New(cfg, p, up, lw, appVersion, cfgPathForSave(*cfgPath))

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("longcat2api %s 启动 → http://127.0.0.1%s  (账号 %d 个)", appVersion, cfg.Listen, store.Enabled())
		log.Printf("通道1 妙手: chat=%s stream=%s model=%s", cfg.Upstream.ChatBase, cfg.Upstream.StreamBase, cfg.Upstream.Model)
		if cfg.LongcatWeb.Enabled {
			log.Printf("通道2 longcat-web: %s mode=%s model=LongCat-2.5（免登录免费，限流节流 %dms）",
				cfg.LongcatWeb.Base, cfg.LongcatWeb.Mode, cfg.LongcatWeb.MinIntervalMS)
		}
		if cfg.APIKey != "" {
			log.Printf("面板/接口已启用 API Key 保护；浏览器首次进入面板可直接用：http://127.0.0.1%s/panel?key=<你的 key>", cfg.Listen)
		} else {
			log.Printf("⚠️  未设置 api_key：/v1 与 /panel 对内网开放，请勿暴露到公网")
		}
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("收到退出信号，正在优雅关闭…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
