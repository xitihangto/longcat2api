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
	"strings"
	"syscall"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/pool"
	"github.com/linquin/longcat2api/internal/server"
	"github.com/linquin/longcat2api/internal/upstream"
)

const appVersion = "0.1.0"

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
	srv := server.New(cfg, p, up)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("longcat2api %s 启动 → http://127.0.0.1%s  (账号 %d 个)", appVersion, cfg.Listen, store.Enabled())
		log.Printf("上游: chat=%s stream=%s model=%s", cfg.Upstream.ChatBase, cfg.Upstream.StreamBase, cfg.Upstream.Model)
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
