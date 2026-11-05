package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adil/bug-issue-tracker/pkg/health"
	"github.com/adil/bug-issue-tracker/pkg/logging"
	"github.com/adil/bug-issue-tracker/pkg/mysqltls"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	logging.Configure()
	if err := mysqltls.RegisterFromEnv(); err != nil {
		slog.Error("configure database TLS", "error", err)
		os.Exit(1)
	}
	dsn := env("AUTH_DATABASE_URL", "")
	if dsn == "" {
		slog.Error("AUTH_DATABASE_URL must be set")
		os.Exit(1)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(10)
	if err = db.Ping(); err != nil {
		slog.Error("connect database", "error", err)
		os.Exit(1)
	}
	secret := []byte(env("JWT_SECRET", ""))
	if len(secret) < 32 {
		slog.Error("JWT_SECRET must contain at least 32 characters")
		os.Exit(1)
	}
	h := NewHandler(db, secret)
	mux := http.NewServeMux()
	mux.Handle("/health", health.Handler(nil))
	mux.Handle("/ready", health.Handler(func() error { return db.Ping() }))
	mux.Handle("/auth/", h)
	mux.Handle("/users", h)
	mux.Handle("/users/", h)
	mux.Handle("/internal/users/", h)
	mux.Handle("/internal/oauth/", h)
	mux.Handle("/.well-known/", h)
	mux.Handle("/oauth/", h)
	mux.Handle("/users/autocomplete", h)
	server := &http.Server{Addr: env("AUTH_ADDR", ":8081"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("auth service listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server", "error", err)
			os.Exit(1)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	ctx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(ctx)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
