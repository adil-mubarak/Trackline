package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamableHTTPToolsAndCallerScopedRequest(t *testing.T) {
	var seenAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/projects/42" {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"name":"Example"}`))
	}))
	defer gateway.Close()
	httpServer := httptest.NewServer(newMCPHandler(gateway.URL, http.DefaultClient))
	defer httpServer.Close()
	ctx := context.Background()
	hc := &http.Client{Transport: authTransport{http.DefaultTransport}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: hc, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 7 {
		t.Fatalf("tools=%d, want 7", len(listed.Tools))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_project", Arguments: map[string]any{"project_id": float64(42)}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if seenAuth != "Bearer caller-test-token" {
		t.Fatalf("forwarded auth=%q", seenAuth)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !json.Valid([]byte(text)) || text != `{"id":42,"name":"Example"}` {
		t.Fatalf("tool result=%q", text)
	}
}

func TestMCPRejectsRequestWithoutGatewayIdentityAndBearer(t *testing.T) {
	handler := newMCPHandler("http://127.0.0.1:1", http.DefaultClient)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		for _, headers := range []map[string]string{{}, {"X-User-ID": "12"}, {"Authorization": "Bearer token"}} {
			r := httptest.NewRequest(method, "/mcp", nil)
			for k, v := range headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("headers=%v status=%d", headers, w.Code)
			}
		}
	}
}

type authTransport struct{ http.RoundTripper }

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-User-ID", "17")
	r.Header.Set("Authorization", "Bearer caller-test-token")
	return t.RoundTripper.RoundTrip(r)
}

func TestExecuteRejectsUnknownArgumentsAndWrites(t *testing.T) {
	if _, err := execute(context.Background(), "http://example.invalid", "Bearer x", "", "get_project", []byte(`{"project_id":1,"admin":true}`), http.DefaultClient); err == nil {
		t.Fatal("unknown argument accepted")
	}
	if _, err := execute(context.Background(), "http://example.invalid", "Bearer x", "", "create_issue", []byte(`{}`), http.DefaultClient); err == nil {
		t.Fatal("write tool accepted")
	}
}

func TestRecentActivityResolvesActorNamesWithoutReturningActorIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/activity":
			_, _ = w.Write([]byte(`[{"id":1,"project_id":3,"issue_id":9,"actor_id":777,"event_type":"IssueCreated","payload":{},"occurred_at":"2026-11-04T10:00:00Z"},{"id":2,"project_id":3,"issue_id":10,"actor_id":888,"event_type":"IssueUpdated","payload":{},"occurred_at":"2026-11-04T10:01:00Z"}]`))
		case "/users/names":
			_, _ = w.Write([]byte(`[{"id":777,"username":"alex"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := execute(context.Background(), server.URL, "Bearer test-token", "request-test", "recent_project_activity", []byte(`{"project_id":3,"limit":2}`), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	if !json.Valid(result) || strings.Contains(text, "actor_id") || strings.Contains(text, "777") || strings.Contains(text, "888") || !strings.Contains(text, `"actor_name":"alex"`) || !strings.Contains(text, "Unknown user") {
		t.Fatalf("activity result leaked actor IDs or missed names: %s", text)
	}
}
