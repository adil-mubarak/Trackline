package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adil/bug-issue-tracker/pkg/health"
	"github.com/adil/bug-issue-tracker/pkg/logging"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	logging.Configure()
	gatewayURL := strings.TrimRight(strings.TrimSpace(os.Getenv("GATEWAY_INTERNAL_URL")), "/")
	if gatewayURL == "" {
		slog.Error("GATEWAY_INTERNAL_URL must be set")
		os.Exit(1)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	mux := http.NewServeMux()
	mux.Handle("/health", health.Handler(nil))
	mux.Handle("/ready", health.Handler(nil))
	mux.Handle("/mcp", newMCPHandler(gatewayURL, client))
	server := &http.Server{Addr: env("MCP_ADDR", "127.0.0.1:8088"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("MCP service listening", "address", server.Addr)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case <-stop.Done():
		ctx, shutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdown()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("MCP service shutdown", "error", err)
		}
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("MCP service", "error", err)
			os.Exit(1)
		}
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func newMCPHandler(gatewayURL string, client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, userErr := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
		if userErr != nil || userID == 0 || !validBearer(r.Header.Get("Authorization")) {
			http.Error(w, "gateway identity and bearer token required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
			return newTrackerServer(gatewayURL, req.Header.Get("Authorization"), req.Header.Get("X-Request-ID"), client)
		}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}).ServeHTTP(w, r)
	})
}

func validBearer(value string) bool {
	parts := strings.Fields(value)
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && len(parts[1]) <= 8192
}
