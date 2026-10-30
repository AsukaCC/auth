package main

import (
	"authcenter/internal/auth"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	app, err := auth.New(auth.Config{
		DataDir:       env("AUTH_DATA_DIR", "./data"),
		WebDir:        env("AUTH_WEB_DIR", "./web/dist"),
		PublicURL:     env("AUTH_PUBLIC_URL", "http://127.0.0.1:5173"),
		SecureCookie:  os.Getenv("AUTH_SECURE_COOKIE") == "true",
		AdminEmail:    os.Getenv("AUTH_ADMIN_EMAIL"),
		AdminName:     os.Getenv("AUTH_ADMIN_NAME"),
		AdminPassword: os.Getenv("AUTH_ADMIN_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	srv := &http.Server{Addr: env("AUTH_ADDR", "127.0.0.1:8080"), Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Printf("Auth listening on %s", srv.Addr)
	if err = srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loadDotEnv(path string) error {
	if err := godotenv.Load(path); err != nil && !os.IsNotExist(err) {
		// Parser errors may contain the offending line, including credentials.
		return fmt.Errorf("无法读取 .env，请检查文件格式与权限")
	}
	return nil
}
