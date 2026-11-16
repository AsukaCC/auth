package main

import (
	"authcenter/internal/deployagent"
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", "/run/auth-deploy/agent.sock")
		}}}
		res, err := client.Get("http://deploy-agent/healthz")
		if err != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			os.Exit(1)
		}
		return
	}
	repo := os.Getenv("AUTH_UPDATE_REPOSITORY")
	if repo == "" {
		repo = "AsukaCC/auth"
	}
	a, err := deployagent.New(deployagent.Config{Root: os.Getenv("AUTH_INSTALL_DIR"), Socket: "/run/auth-deploy/agent.sock", Repository: repo, TokenFile: os.Getenv("AUTH_GITHUB_TOKEN_FILE"), SocketUID: 10001, Project: os.Getenv("AUTH_PROJECT_NAME")})
	if err != nil {
		log.Fatal("部署代理初始化失败：", err)
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = a.CheckTools(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	l, err := a.Listen()
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second}
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-stopCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Print("部署代理已启动，仅监听本地 Unix socket")
	if err = srv.Serve(l); err != nil && err != http.ErrServerClosed {
		log.Print("部署代理监听已停止")
	}
}
