package logging

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/adil/bug-issue-tracker/pkg/requestid"
)

// ConnectorDiagnostics logs only fixed connector routes and response metadata.
// It never reads bodies, credentials, URL queries, or response headers.
func ConnectorDiagnostics(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if len(path) > 4 && path[:4] == "/api" {
			path = path[4:]
		}
		switch path {
		case "/oauth/register", "/oauth/authorize", "/oauth/token", "/oauth/revoke", "/mcp", "/internal/oauth/introspect":
		default:
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		rw := &connectorWriter{ResponseWriter: w}
		slog.Info("connector request started", "service", service, "route", path, "request_id", requestid.FromContext(r.Context()))
		next.ServeHTTP(rw, r)
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.Info("connector request completed", "service", service, "route", path, "status", status, "oauth_error", rw.oauthError, "duration_ms", time.Since(started).Milliseconds(), "request_id", requestid.FromContext(r.Context()))
	})
}

type connectorWriter struct {
	http.ResponseWriter
	status     int
	oauthError string
}

func (w *connectorWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *connectorWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap keeps ResponseController features available through this wrapper.
func (w *connectorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush preserves Streamable HTTP/SSE behavior for the reverse proxy.
func (w *connectorWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// SetOAuthError accepts only protocol error categories, never descriptions.
func (w *connectorWriter) SetOAuthError(code string) {
	switch code {
	case "invalid_request", "invalid_client", "invalid_grant", "invalid_target", "unsupported_grant_type", "server_error", "invalid_client_metadata", "access_denied":
		w.oauthError = code
	default:
		w.oauthError = "other"
	}
}
