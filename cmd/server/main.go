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
	if store.Enabled() == 0 {
		log.Printf("⚠️  没有可用账号，请先执行 `go run ./cmd/login` 导入妙手登录态（或手工写 %s）", cfg.AccountsFile)
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
