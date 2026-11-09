package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adil/bug-issue-tracker/pkg/auth"
	"github.com/adil/bug-issue-tracker/pkg/health"
	"github.com/adil/bug-issue-tracker/pkg/logging"
	"github.com/adil/bug-issue-tracker/pkg/ratelimit"
	"github.com/adil/bug-issue-tracker/pkg/requestid"
)

func main() {
	logging.Configure()
	secret := []byte(env("JWT_SECRET", ""))
	if len(secret) < 32 {
		slog.Error("JWT_SECRET must contain at least 32 characters")
		os.Exit(1)
	}
	bucket := ratelimit.New(120, 2)
	transport := &http.Transport{ResponseHeaderTimeout: 5 * time.Second}
	routes := map[string]string{
		"auth":                env("AUTH_SERVICE_URL", "http://auth:8081"),
		"projects":            env("PROJECT_SERVICE_URL", "http://project:8082"),
		"issues":              env("ISSUE_SERVICE_URL", "http://issue:8083"),
		"activity":            env("ACTIVITY_SERVICE_URL", "http://activity:8084"),
		"ai":                  strings.TrimRight(os.Getenv("AI_SERVICE_URL"), "/"),
		"mcp":                 strings.TrimRight(os.Getenv("MCP_SERVICE_URL"), "/"),
		"mcp_oauth_issuer":    strings.TrimRight(env("MCP_OAUTH_ISSUER_URL", "http://localhost:8082"), "/"),
		"mcp_resource":        strings.TrimSpace(env("MCP_RESOURCE_URL", strings.TrimRight(env("MCP_OAUTH_ISSUER_URL", "http://localhost:8082"), "/")+"/mcp")),
		"mcp_oauth_secret":    strings.TrimSpace(os.Getenv("MCP_OAUTH_INTERNAL_SECRET")),
		"mcp_username_secret": strings.TrimSpace(os.Getenv("MCP_USERNAME_LOOKUP_SECRET")),
	}
	handler := newGatewayHandler(secret, routes, bucket, transport)
	handler = corsMiddleware(parseAllowedOrigins(env("CORS_ALLOWED_ORIGINS", "")), handler)
	server := &http.Server{Addr: env("GATEWAY_ADDR", ":8082"), Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	slog.Info("gateway listening", "address", server.Addr)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	select {
	case <-stop.Done():
		ctx, shutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdown()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("gateway shutdown", "error", err)
		}
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("gateway", "error", err)
			os.Exit(1)
		}
	}
}

func parseAllowedOrigins(value string) []string {
	parts := strings.Split(value, ",")
	origins := make([]string, 0, len(parts))
	for _, origin := range parts {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}

func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		_, isAllowed := allowed[origin]
		if isAllowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			if !isAllowed {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newGatewayHandler(secret []byte, routes map[string]string, bucket *ratelimit.Bucket, transport http.RoundTripper) http.Handler {
	mux := http.NewServeMux()
	aiTransport := transport
	mcpTransport := transport
	if baseTransport, ok := transport.(*http.Transport); ok {
		clonedTransport := baseTransport.Clone()
		clonedTransport.ResponseHeaderTimeout = 175 * time.Second
		aiTransport = clonedTransport
		mcpClonedTransport := baseTransport.Clone()
		mcpClonedTransport.ResponseHeaderTimeout = 12 * time.Second
		mcpTransport = mcpClonedTransport
	}
	mux.Handle("/health", health.Handler(nil))
	mux.Handle("/ready", health.Handler(func() error { return checkUpstreams(routes, transport) }))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Accept both direct gateway paths (/auth/...) and paths forwarded by a
		// proxy that did not strip the frontend's /api prefix (/api/auth/...).
		r.URL.Path = normalizeAPIPath(r.URL.Path)
		if !bucket.Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		target, protected, ok := gatewayRoute(routes, r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if protected {
			parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				if r.URL.Path == "/mcp" {
					writeMCPAuthChallenge(w, routes, "")
				} else {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
				}
				return
			}
			claims, err := auth.VerifyTokenClaims(parts[1], secret)
			if err != nil {
				if r.URL.Path == "/mcp" {
					writeMCPAuthChallenge(w, routes, "invalid_token")
				} else {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
				}
				return
			}
			userID, role, mustChangePassword, resetVersion := claims.UserID, claims.Role, claims.MustChangePassword, claims.PasswordResetVersion
			if claims.TokenUse == "oauth_access" {
				if claims.Audience != routes["mcp_resource"] || !hasOAuthScope(claims.Scope, "mcp:read") || !oauthReadOnlyRoute(r.Method, r.URL.Path) {
					http.Error(w, "insufficient scope", http.StatusForbidden)
					return
				}
				if r.URL.Path == "/users/names" && (len(routes["mcp_username_secret"]) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-MCP-Internal-Service-Token")), []byte(routes["mcp_username_secret"])) != 1) {
					http.Error(w, "insufficient scope", http.StatusForbidden)
					return
				}
				r.Header.Del("X-MCP-Internal-Service-Token")
				identity, active, err := introspectMCPAccessToken(r.Context(), routes["auth"], routes["mcp_oauth_secret"], parts[1], transport)
				if err != nil {
					slog.Warn("MCP OAuth token introspection failed", "error", err)
					http.Error(w, "authorization service unavailable", http.StatusBadGateway)
					return
				}
				if !active || identity.UserID != userID || identity.Role != role || identity.Scope != claims.Scope || identity.Audience != claims.Audience || identity.PasswordResetVersion != resetVersion {
					if r.URL.Path == "/mcp" {
						writeMCPAuthChallenge(w, routes, "invalid_token")
					} else {
						http.Error(w, "unauthorized", http.StatusUnauthorized)
					}
					return
				}
				if mustChangePassword {
					http.Error(w, "password change required", http.StatusForbidden)
					return
				}
			} else {
				if claims.TokenUse != "" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				resetRequired, currentVersion, err := checkAuthSession(ctx, routes["auth"], transport, userID, role, resetVersion)
				if err != nil {
					slog.Warn("auth session validation failed", "error", err)
					var statusErr *upstreamStatusError
					if errors.As(err, &statusErr) && statusErr.status == http.StatusUnauthorized {
						http.Error(w, "unauthorized", http.StatusUnauthorized)
					} else {
						http.Error(w, "auth service unavailable", http.StatusBadGateway)
					}
					return
				}
				if resetVersion != currentVersion || (mustChangePassword && !resetRequired) {
					http.Error(w, "session no longer valid", http.StatusUnauthorized)
					return
				}
				if resetRequired && r.URL.Path != "/auth/password" {
					http.Error(w, "password change required", http.StatusForbidden)
					return
				}
			}
			r.Header.Del("X-MCP-Internal-Service-Token")
			r.Header.Set("X-User-ID", strconv.FormatUint(userID, 10))
			r.Header.Set("X-User-Role", role)
			r.Header.Set("X-Must-Change-Password", strconv.FormatBool(mustChangePassword))
			r.Header.Set("X-Password-Reset-Version", strconv.FormatUint(resetVersion, 10))
		}
		destination, err := url.Parse(target)
		if err != nil {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(destination)
		proxy.Transport = transport
		requestTimeout := 10 * time.Second
		if r.URL.Path == "/mcp" {
			// The SDK validates Host on loopback listeners. Use the trusted
			// upstream authority, not the public Host forwarded by the caller.
			director := proxy.Director
			proxy.Director = func(req *http.Request) {
				director(req)
				req.Host = destination.Host
			}
			proxy.Transport = mcpTransport
			requestTimeout = 35 * time.Second
		}
		if r.URL.Path == "/ai/chat" {
			proxy.Transport = aiTransport
			requestTimeout = 185 * time.Second
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("upstream request failed", "error", err)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		r.Header.Set("X-Request-ID", requestid.FromContext(r.Context()))
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
	return requestid.Middleware(logging.ConnectorDiagnostics("gateway", mux))
}

func checkUpstreams(routes map[string]string, transport http.RoundTripper) error {
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	// Only core services affect readiness. Routes also contains optional AI/MCP
	// endpoints and MCP metadata and secrets, which must never be probed.
	for _, name := range []string{"auth", "projects", "issues", "activity"} {
		target := routes[name]
		if target == "" {
			continue
		}
		u, err := url.Parse(strings.TrimRight(target, "/") + "/ready")
		if err != nil {
			return err
		}
		resp, err := client.Get(u.String())
		if err != nil {
			return fmt.Errorf("%s readiness: %w", name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s readiness returned %s", name, resp.Status)
		}
	}
	return nil
}

type upstreamStatusError struct {
	status int
}

func (e *upstreamStatusError) Error() string {
	return fmt.Sprintf("auth session endpoint returned %s", http.StatusText(e.status))
}

func checkAuthSession(ctx context.Context, authURL string, transport http.RoundTripper, userID uint64, role string, resetVersion uint64) (bool, uint64, error) {
	endpoint := strings.TrimRight(authURL, "/") + "/auth/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, 0, fmt.Errorf("create auth session request: %w", err)
	}
	req.Header.Set("X-User-ID", strconv.FormatUint(userID, 10))
	req.Header.Set("X-User-Role", role)
	req.Header.Set("X-Password-Reset-Version", strconv.FormatUint(resetVersion, 10))
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("request auth session status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, 0, &upstreamStatusError{status: resp.StatusCode}
	}
	var result struct {
		PasswordResetRequired bool   `json:"password_reset_required"`
		PasswordResetVersion  uint64 `json:"password_reset_version"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4096))
	if err := dec.Decode(&result); err != nil {
		return false, 0, fmt.Errorf("decode auth session status: %w", err)
	}
	return result.PasswordResetRequired, result.PasswordResetVersion, nil
}

type mcpOAuthIdentity struct {
	UserID               uint64 `json:"user_id"`
	Role                 string `json:"role"`
	PasswordResetVersion uint64 `json:"password_reset_version"`
	Scope                string `json:"scope"`
	Audience             string `json:"audience"`
}

func introspectMCPAccessToken(ctx context.Context, authURL, internalSecret, token string, transport http.RoundTripper) (mcpOAuthIdentity, bool, error) {
	if len(internalSecret) < 32 || authURL == "" {
		return mcpOAuthIdentity{}, false, errors.New("MCP OAuth introspection is not configured")
	}
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return mcpOAuthIdentity{}, false, fmt.Errorf("encode OAuth introspection: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(authURL, "/")+"/internal/oauth/introspect", strings.NewReader(string(body)))
	if err != nil {
		return mcpOAuthIdentity{}, false, fmt.Errorf("create OAuth introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Service-Token", internalSecret)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return mcpOAuthIdentity{}, false, fmt.Errorf("request OAuth introspection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mcpOAuthIdentity{}, false, fmt.Errorf("OAuth introspection returned %s", resp.Status)
	}
	var result struct {
		Active bool `json:"active"`
		mcpOAuthIdentity
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		return mcpOAuthIdentity{}, false, fmt.Errorf("decode OAuth introspection: %w", err)
	}
	return result.mcpOAuthIdentity, result.Active, nil
}

func writeMCPAuthChallenge(w http.ResponseWriter, routes map[string]string, reason string) {
	issuer := strings.TrimRight(routes["mcp_oauth_issuer"], "/")
	if issuer == "" {
		issuer = "http://localhost:8082"
	}
	challenge := `Bearer resource_metadata="` + issuer + `/.well-known/oauth-protected-resource/mcp", scope="mcp:read offline_access"`
	if reason != "" {
		challenge += `, error="` + reason + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
}

func hasOAuthScope(scope, required string) bool {
	for _, value := range strings.Fields(scope) {
		if value == required {
			return true
		}
	}
	return false
}

func oauthReadOnlyRoute(method, path string) bool {
	if path == "/mcp" {
		return method == http.MethodPost
	}
	if path == "/users/names" {
		return method == http.MethodPost
	}
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/projects", "/issues", "/issues/analytics", "/activity":
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && (parts[0] == "projects" || parts[0] == "issues") {
		id, err := strconv.ParseUint(parts[1], 10, 64)
		return err == nil && id > 0
	}
	if len(parts) == 3 && parts[0] == "projects" && parts[2] == "members" {
		id, err := strconv.ParseUint(parts[1], 10, 64)
		return err == nil && id > 0
	}
	return false
}

func gatewayRoute(routes map[string]string, path string) (target string, protected, ok bool) {
	switch {
	case path == "/.well-known/oauth-protected-resource/mcp" || path == "/.well-known/oauth-authorization-server" || path == "/oauth/register" || path == "/oauth/authorize" || path == "/oauth/token" || path == "/oauth/revoke":
		return routes["auth"], false, routes["auth"] != ""
	case path == "/auth/password":
		return routes["auth"], true, routes["auth"] != ""
	case path == "/auth/session":
		return routes["auth"], true, routes["auth"] != ""
	case path == "/auth/oauth/github/link" || path == "/auth/oauth/google/link":
		return routes["auth"], true, routes["auth"] != ""
	case strings.HasPrefix(path, "/auth/"):
		return routes["auth"], false, routes["auth"] != ""
	case path == "/users/autocomplete":
		return routes["auth"], true, routes["auth"] != ""
	case path == "/users/directory":
		return routes["auth"], true, routes["auth"] != ""
	case path == "/users/names":
		return routes["auth"], true, routes["auth"] != ""
	case path == "/users" || isUserPasswordResetPath(path) || isUserStatusPath(path):
		return routes["auth"], true, routes["auth"] != ""
	case isProjectIssueMovePath(path):
		return routes["issues"], true, routes["issues"] != ""
	case path == "/projects" || strings.HasPrefix(path, "/projects/"):
		return routes["projects"], true, routes["projects"] != ""
	case path == "/issues" || strings.HasPrefix(path, "/issues/"):
		return routes["issues"], true, routes["issues"] != ""
	case path == "/activity" || strings.HasPrefix(path, "/activity/"):
		return routes["activity"], true, routes["activity"] != ""
	case path == "/ai/chat":
		return routes["ai"], true, routes["ai"] != ""
	case path == "/mcp":
		return routes["mcp"], true, routes["mcp"] != ""
	case path == "/ai/conversations" || strings.HasPrefix(path, "/ai/conversations/"):
		return routes["ai"], true, routes["ai"] != ""
	case path == "/github/repositories":
		return routes["ai"], true, routes["ai"] != ""
	default:
		return "", false, false
	}
}

func isUserStatusPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "status" {
		return false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	return err == nil && id > 0
}

func isUserPasswordResetPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "password-reset" {
		return false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	return err == nil && id > 0
}

func isProjectIssueMovePath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "projects" || parts[2] != "issues" || parts[4] != "move" {
		return false
	}
	projectID, projectErr := strconv.ParseUint(parts[1], 10, 64)
	issueID, issueErr := strconv.ParseUint(parts[3], 10, 64)
	return projectErr == nil && projectID > 0 && issueErr == nil && issueID > 0
}

func normalizeAPIPath(path string) string {
	if strings.HasPrefix(path, "/api/") {
		return strings.TrimPrefix(path, "/api")
	}
	return path
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
