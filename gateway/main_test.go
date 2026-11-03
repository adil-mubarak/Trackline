package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adil/bug-issue-tracker/pkg/auth"
	"github.com/adil/bug-issue-tracker/pkg/ratelimit"
)

func TestNormalizeAPIPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "frontend api prefix", path: "/api/auth/login", want: "/auth/login"},
		{name: "unprefixed gateway route", path: "/auth/login", want: "/auth/login"},
		{name: "api prefix is segment-aware", path: "/apix/auth/login", want: "/apix/auth/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAPIPath(tt.path); got != tt.want {
				t.Errorf("normalizeAPIPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestGatewayAuthenticationAndRouting(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if r.URL.Path != "/projects" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		if r.Header.Get("X-User-ID") != "55" || r.Header.Get("X-User-Role") != "member" {
			t.Errorf("identity headers missing: %v", r.Header)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	authService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer authService.Close()
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	handler := newGatewayHandler(secret, map[string]string{"auth": authService.URL, "projects": upstream.URL}, ratelimit.New(10, 1), &http.Transport{ResponseHeaderTimeout: time.Second})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/projects", nil)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || upstreamHits != 0 {
		t.Fatalf("unauthorized status=%d upstreamHits=%d", w.Code, upstreamHits)
	}

	token, err := auth.IssueToken(55, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/projects", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Request-ID", "gateway-test-id")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || upstreamHits != 1 {
		t.Fatalf("authorized status=%d upstreamHits=%d body=%s", w.Code, upstreamHits, w.Body.String())
	}
	if w.Header().Get("X-Request-ID") != "gateway-test-id" {
		t.Fatalf("request ID not returned: %q", w.Header().Get("X-Request-ID"))
	}
}

func TestGatewayForwardsPublicAuthAndRejectsUnknownRoute(t *testing.T) {
	var called bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/auth/login" {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	handler := newGatewayHandler([]byte("gateway-test-secret-at-least-32-bytes"), map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
	if w.Code != http.StatusOK || !called {
		t.Fatalf("public auth response %d called=%v", w.Code, called)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects-evil", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown route status=%d", w.Code)
	}
}

func TestGatewayProtectsAndRoutesUsernameAutocomplete(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var seenUser, seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		seenUser, seenPath = r.Header.Get("X-User-ID"), r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(72, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users/autocomplete?q=ab", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || seenPath != "/users/autocomplete" || seenUser != "72" {
		t.Fatalf("status=%d upstream=(%q,%q)", w.Code, seenPath, seenUser)
	}
}

func TestGatewayProtectsAndRoutesActiveUserDirectory(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var seenUser, seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		seenUser, seenPath = r.Header.Get("X-User-ID"), r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[],"next_after_id":null}`))
	}))
	defer upstream.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(72, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/users/directory?limit=25", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusOK || seenPath != "/users/directory" || seenUser != "72" {
		t.Fatalf("status=%d upstream=(%q,%q)", w.Code, seenPath, seenUser)
	}
}

func TestGatewayProtectsAndRoutesBatchUsernameLookup(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var sawLookup bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		sawLookup = r.URL.Path == "/users/names" && r.Method == http.MethodPost && r.Header.Get("X-User-ID") == "72"
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":72,"username":"alex"}]`))
	}))
	defer upstream.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(72, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/users/names", strings.NewReader(`{"user_ids":[72]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusOK || !sawLookup || !strings.Contains(w.Body.String(), `"username":"alex"`) {
		t.Fatalf("lookup status=%d sawLookup=%v body=%s", w.Code, sawLookup, w.Body.String())
	}
}

func TestGatewayProtectsAndRoutesUserStatusChanges(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var forwarded bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		forwarded = r.URL.Path == "/users/18/status" && r.Method == http.MethodPatch && r.Header.Get("X-User-ID") == "1" && r.Header.Get("X-User-Role") == "admin"
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(1, "admin", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/users/18/status", strings.NewReader(`{"active":false}`))
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != http.StatusOK || !forwarded {
		t.Fatalf("status=%d forwarded=%v", w.Code, forwarded)
	}
}

func TestGatewayRestrictsPasswordResetSessionsToPasswordChange(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var seenPath, seenUser, seenReset string
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			hits++
			_, _ = w.Write([]byte(`{"password_reset_required":true,"password_reset_version":1}`))
			return
		}
		hits++
		seenPath, seenUser, seenReset = r.URL.Path, r.Header.Get("X-User-ID"), r.Header.Get("X-Must-Change-Password")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": upstream.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueTokenWithResetState(77, "member", true, 1, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/users", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || hits != 1 {
		t.Fatalf("protected request status=%d upstream hits=%d", w.Code, hits)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/auth/password", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || hits != 3 || seenPath != "/auth/password" || seenUser != "77" || seenReset != "true" {
		t.Fatalf("password route status=%d hits=%d path=%q user=%q reset=%q", w.Code, hits, seenPath, seenUser, seenReset)
	}
}

func TestGatewayRejectsPreviouslyIssuedTokenAfterAdminPasswordReset(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	resetRequired := false
	var projectHits int
	authService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/session" {
			http.NotFound(w, r)
			return
		}
		if resetRequired {
			_, _ = w.Write([]byte(`{"password_reset_required":true,"password_reset_version":1}`))
		} else {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
		}
	}))
	defer authService.Close()
	projects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer projects.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": authService.URL, "projects": projects.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	oldToken, err := auth.IssueToken(91, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	callProtected := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/projects", nil)
		r.Header.Set("Authorization", "Bearer "+oldToken)
		handler.ServeHTTP(w, r)
		return w
	}
	if w := callProtected(); w.Code != http.StatusOK || projectHits != 1 {
		t.Fatalf("normal session status=%d project hits=%d", w.Code, projectHits)
	}
	resetRequired = true
	if w := callProtected(); w.Code != http.StatusUnauthorized || projectHits != 1 {
		t.Fatalf("pre-reset token status=%d project hits=%d", w.Code, projectHits)
	}
}

func TestGatewayRoutesProjectScopedIssueMovesToIssueService(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var seenPath, seenUser string
	issueService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenUser = r.URL.Path, r.Header.Get("X-User-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer issueService.Close()
	projectService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("move request was incorrectly sent to project service")
		w.WriteHeader(http.StatusOK)
	}))
	defer projectService.Close()
	authService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer authService.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": authService.URL, "issues": issueService.URL, "projects": projectService.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(55, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/projects/4/issues/12/move", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || seenPath != "/projects/4/issues/12/move" || seenUser != "55" {
		t.Fatalf("status=%d path=%q user=%q", w.Code, seenPath, seenUser)
	}
}

func TestGatewayReadinessChecksEveryUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	if err := checkUpstreams(map[string]string{"auth": upstream.URL, "issue": upstream.URL}, http.DefaultTransport); err != nil {
		t.Fatal(err)
	}
	if err := checkUpstreams(map[string]string{"missing": "http://127.0.0.1:1"}, http.DefaultTransport); err == nil {
		t.Fatal("expected unavailable upstream error")
	}
	if err := checkUpstreams(map[string]string{"ai": "http://127.0.0.1:1"}, http.DefaultTransport); err != nil {
		t.Fatalf("optional AI upstream should not block tracker readiness: %v", err)
	}
}

func TestGatewayProtectsAndForwardsAIChat(t *testing.T) {
	secret := []byte("gateway-test-secret-at-least-32-bytes")
	var seenPath, seenUser string
	aiService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenUser = r.URL.Path, r.Header.Get("X-User-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":"ok","model":"nvidia/nemotron-3-ultra-550b-a55b:free"}`))
	}))
	defer aiService.Close()
	authService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/session" {
			_, _ = w.Write([]byte(`{"password_reset_required":false,"password_reset_version":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer authService.Close()
	handler := newGatewayHandler(secret, map[string]string{"auth": authService.URL, "ai": aiService.URL}, ratelimit.New(10, 1), http.DefaultTransport)
	token, err := auth.IssueToken(87, "member", secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"hello"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || seenPath != "/ai/chat" || seenUser != "87" {
		t.Fatalf("status=%d path=%q user=%q body=%s", w.Code, seenPath, seenUser, w.Body.String())
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"hello"}`))
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestCORSPreflightRequiresExactAllowedOrigin(t *testing.T) {
	var nextCalled bool
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })
	handler := corsMiddleware(parseAllowedOrigins(" https://trackline.example,https://staging.trackline.example "), next)

	tests := []struct {
		name       string
		origin     string
		wantStatus int
		wantAllow  string
	}{
		{name: "allowed origin", origin: "https://trackline.example", wantStatus: http.StatusNoContent, wantAllow: "https://trackline.example"},
		{name: "unlisted origin", origin: "https://evil.example", wantStatus: http.StatusForbidden},
		{name: "missing origin", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllow {
				t.Errorf("allow origin=%q, want %q", got, tt.wantAllow)
			}
			if tt.wantStatus == http.StatusNoContent && w.Header().Get("Access-Control-Allow-Headers") == "" {
				t.Error("allowed preflight has no allowed request headers")
			}
		})
	}
	if nextCalled {
		t.Fatal("preflight request unexpectedly reached downstream handler")
	}
}

func TestCORSAddsOriginHeaderOnlyForAllowedOrigins(t *testing.T) {
	handler := corsMiddleware([]string{"https://trackline.example"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tt := range []struct {
		origin string
		want   string
	}{
		{origin: "https://trackline.example", want: "https://trackline.example"},
		{origin: "https://other.example", want: ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
		req.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.want {
			t.Errorf("origin %q: allow-origin=%q, want %q", tt.origin, got, tt.want)
		}
	}
}
