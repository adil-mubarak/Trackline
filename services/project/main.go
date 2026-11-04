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
	db, err := sql.Open("mysql", env("PROJECT_DATABASE_URL", ""))
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		slog.Error("connect database", "error", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	h := &projectHandler{db: db, authURL: env("AUTH_SERVICE_URL", "http://auth:8081"), client: client, repoVerifier: &aiRepositoryVerifier{baseURL: os.Getenv("AI_SERVICE_URL"), client: client}}
	mux := http.NewServeMux()
	mux.Handle("/health", health.Handler(nil))
	mux.Handle("/ready", health.Handler(func() error { return db.Ping() }))
	mux.Handle("/projects", h)
	mux.Handle("/projects/", h)
	server := &http.Server{Addr: env("PROJECT_ADDR", ":8082"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("project service listening", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "error", err)
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
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
