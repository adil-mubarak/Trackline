package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adil/bug-issue-tracker/pkg/requestid"
)

func TestConnectorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, path    string
		status        int
		flush, logged bool
	}{
		{"token failure", "/oauth/token", 400, false, true},
		{"mcp streaming", "/mcp", 200, true, true},
		{"prefixed mcp", "/api/mcp", 401, false, true},
		{"login excluded", "/auth/login", 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if d, ok := w.(interface{ SetOAuthError(string) }); ok {
					d.SetOAuthError("invalid_grant")
				}
				w.Header().Set("Location", "https://example.com/?code=secret-location")
				w.WriteHeader(tc.status)
				if tc.flush {
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Fatal(err)
					}
				}
				_, _ = w.Write([]byte("secret-response"))
			})
			r := httptest.NewRequest(http.MethodPost, tc.path+"?code=secret-query", strings.NewReader("password=secret-body"))
			r.Header.Set("Authorization", "Bearer secret-token")
			r.Header.Set("Cookie", "secret-cookie")
			r.Header.Set("X-Request-ID", "diagnostic-test")
			w := httptest.NewRecorder()
			requestid.Middleware(ConnectorDiagnostics("test", next)).ServeHTTP(w, r)
			if w.Code != tc.status || w.Body.String() != "secret-response" {
				t.Fatal("response changed")
			}
			if tc.flush && !w.Flushed {
				t.Fatal("flush not forwarded")
			}
			got := logs.String()
			if strings.Contains(got, "secret-") {
				t.Fatalf("sensitive data logged: %s", got)
			}
			if tc.logged {
				for _, want := range []string{"connector request completed", `"request_id":"diagnostic-test"`, `"oauth_error":"invalid_grant"`} {
					if !strings.Contains(got, want) {
						t.Fatalf("missing %s in %s", want, got)
					}
				}
			} else if got != "" {
				t.Fatalf("unexpected log: %s", got)
			}
		})
	}
}
