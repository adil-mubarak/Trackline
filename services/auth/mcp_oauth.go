package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	sharedauth "github.com/adil/bug-issue-tracker/pkg/auth"
	"golang.org/x/crypto/bcrypt"
)

const (
	mcpReadScope     = "mcp:read"
	claudeRedirect   = "https://claude.ai/api/mcp/auth_callback"
	authorizationTTL = 5 * time.Minute
	accessTokenTTL   = 10 * time.Minute
	refreshTokenTTL  = 30 * 24 * time.Hour
	grantTTL         = 30 * 24 * time.Hour
)

var (
	pkceVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	csrfPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{32,64}$`)
)

type MCPOAuthSettings struct {
	Issuer         string
	Resource       string
	InternalSecret []byte
}

func mcpOAuthSettingsFromEnv() MCPOAuthSettings {
	issuer := strings.TrimRight(strings.TrimSpace(env("MCP_OAUTH_ISSUER_URL", "http://localhost:8082")), "/")
	resource := strings.TrimSpace(env("MCP_RESOURCE_URL", issuer+"/mcp"))
	return MCPOAuthSettings{Issuer: issuer, Resource: resource, InternalSecret: []byte(strings.TrimSpace(env("MCP_OAUTH_INTERNAL_SECRET", "")))}
}

func (h *Handler) serveMCPOAuth(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource/mcp":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.protectedResourceMetadata(w)
		return true
	case "/.well-known/oauth-authorization-server":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.authorizationServerMetadata(w)
		return true
	case "/oauth/register":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.registerOAuthClient(w, r)
		return true
	case "/oauth/authorize":
		switch r.Method {
		case http.MethodGet:
			h.authorizeGet(w, r)
		case http.MethodPost:
			h.authorizePost(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	case "/oauth/token":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return true
		}
		h.oauthToken(w, r)
		return true
	case "/oauth/revoke":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return true
		}
		h.revokeOAuthToken(w, r)
		return true
	case "/internal/oauth/introspect":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.introspectOAuthToken(w, r)
		return true
	default:
		return false
	}
}

func (h *Handler) protectedResourceMetadata(w http.ResponseWriter) {
	if !validOAuthURLs(h.mcp) {
		http.Error(w, "OAuth issuer is not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"resource":                 h.mcp.Resource,
		"authorization_servers":    []string{h.mcp.Issuer},
		"scopes_supported":         []string{mcpReadScope, "offline_access"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (h *Handler) authorizationServerMetadata(w http.ResponseWriter) {
	if !validOAuthURLs(h.mcp) {
		http.Error(w, "OAuth issuer is not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{
		"issuer":                                         h.mcp.Issuer,
		"authorization_endpoint":                         h.mcp.Issuer + "/oauth/authorize",
		"token_endpoint":                                 h.mcp.Issuer + "/oauth/token",
		"registration_endpoint":                          h.mcp.Issuer + "/oauth/register",
		"revocation_endpoint":                            h.mcp.Issuer + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"scopes_supported":                               []string{mcpReadScope, "offline_access"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func validOAuthURLs(settings MCPOAuthSettings) bool {
	issuer, err := url.Parse(settings.Issuer)
	if err != nil || issuer == nil {
		return false
	}
	localHTTP := issuer.Scheme == "http" && (issuer.Hostname() == "localhost" || issuer.Hostname() == "127.0.0.1")
	if issuer.Host == "" || issuer.User != nil || issuer.Path != "" || issuer.RawQuery != "" || issuer.Fragment != "" || (issuer.Scheme != "https" && !localHTTP) {
		return false
	}
	resource, err := url.Parse(settings.Resource)
	return err == nil && resource.Host != "" && resource.User == nil && resource.Scheme == issuer.Scheme && strings.TrimRight(resource.String(), "/") == strings.TrimRight(settings.Issuer+"/mcp", "/")
}

func (h *Handler) registerOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !validOAuthURLs(h.mcp) {
		oauthError(w, http.StatusServiceUnavailable, "server_error", "OAuth issuer is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client metadata must be JSON")
		return
	}
	var input struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		ClientURI               string   `json:"client_uri"`
		LogoURI                 string   `json:"logo_uri"`
		Scope                   string   `json:"scope"`
		Contacts                []string `json:"contacts"`
		SoftwareID              string   `json:"software_id"`
		SoftwareVersion         string   `json:"software_version"`
	}
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.RedirectURIs) != 1 || input.RedirectURIs[0] != claudeRedirect || (input.TokenEndpointAuthMethod != "" && input.TokenEndpointAuthMethod != "none") || !validClientName(input.ClientName) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported OAuth client metadata")
		return
	}
	if !validOptionalString(input.ClientURI, 512) || !validOptionalString(input.LogoURI, 512) || !validOptionalString(input.SoftwareID, 128) || !validOptionalString(input.SoftwareVersion, 64) || len(input.Contacts) > 5 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported OAuth client metadata")
		return
	}
	for _, contact := range input.Contacts {
		if len(contact) > 320 || strings.ContainsAny(contact, "\r\n") {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported OAuth client metadata")
			return
		}
	}
	if !validStringList(input.GrantTypes, []string{"authorization_code", "refresh_token"}) || !validStringList(input.ResponseTypes, []string{"code"}) {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported OAuth grants")
		return
	}
	name := strings.TrimSpace(input.ClientName)
	if name == "" {
		name = "Claude MCP client"
	}
	clientID, err := randomToken("tl_client_", 24)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), "INSERT INTO oauth_clients (client_id,client_name,redirect_uri) VALUES (?,?,?)", clientID, name, claudeRedirect); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_name":                name,
		"redirect_uris":              []string{claudeRedirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

func validClientName(name string) bool {
	return len(strings.TrimSpace(name)) <= 128 && !strings.ContainsAny(name, "\r\n\x00")
}

func validOptionalString(value string, max int) bool {
	return len(value) <= max && !strings.ContainsAny(value, "\r\n\x00")
}

func validStringList(got, allowed []string) bool {
	if len(got) == 0 {
		return true
	}
	seen := map[string]bool{}
	for _, item := range got {
		valid := false
		for _, option := range allowed {
			if item == option {
				valid = true
			}
		}
		if !valid || seen[item] {
			return false
		}
		seen[item] = true
	}
	return true
}

type oauthAuthorization struct {
	ClientID    string
	RedirectURI string
	State       string
	Challenge   string
	Scope       string
	Resource    string
	CSRF        string
}

func (h *Handler) authorizeGet(w http.ResponseWriter, r *http.Request) {
	a, err := h.parseAuthorization(r.Context(), r.URL.Query())
	if err != nil {
		h.authorizationError(w, r, http.StatusBadRequest, "invalid_request")
		return
	}
	csrf, err := randomToken("", 32)
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}
	a.CSRF = csrf
	h.setCSRFCookie(w, csrf)
	h.setOAuthPageHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, renderOAuthConsent(a, ""))
}

func (h *Handler) parseAuthorization(ctx context.Context, values url.Values) (oauthAuthorization, error) {
	clientID, clientOK := formParam(values, "client_id")
	redirectURI, redirectOK := formParam(values, "redirect_uri")
	responseType, responseOK := formParam(values, "response_type")
	state, stateOK := formParam(values, "state")
	challenge, challengeOK := formParam(values, "code_challenge")
	challengeMethod, methodOK := formParam(values, "code_challenge_method")
	scope, scopeOK := formParam(values, "scope")
	resource, resourceOK := formParam(values, "resource")
	if len(values["resource"]) > 1 || !clientOK || !redirectOK || !responseOK || !stateOK || !challengeOK || !methodOK || !scopeOK || clientID == "" || redirectURI != claudeRedirect || responseType != "code" || state == "" || len(state) > 1024 || challengeMethod != "S256" || (resourceOK && resource != h.mcp.Resource) || !validScope(scope) || !validPKCEChallenge(challenge) {
		return oauthAuthorization{}, errors.New("invalid authorization request")
	}
	a := oauthAuthorization{ClientID: clientID, RedirectURI: redirectURI, State: state, Challenge: challenge, Scope: scope, Resource: resource}
	var registeredRedirect string
	if err := h.db.QueryRowContext(ctx, "SELECT redirect_uri FROM oauth_clients WHERE client_id=?", a.ClientID).Scan(&registeredRedirect); err != nil || registeredRedirect != a.RedirectURI {
		return oauthAuthorization{}, errors.New("unknown OAuth client")
	}
	return a, nil
}

func validScope(scope string) bool {
	parts := strings.Fields(scope)
	if len(parts) == 0 || len(parts) > 2 || parts[0] != mcpReadScope {
		return false
	}
	return len(parts) == 1 || parts[1] == "offline_access"
}

func validPKCEChallenge(challenge string) bool {
	if len(challenge) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == sha256.Size
}

func (h *Handler) authorizePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid authorization form", http.StatusBadRequest)
		return
	}
	a, err := h.parseAuthorization(r.Context(), r.PostForm)
	if err != nil {
		h.authorizationError(w, r, http.StatusBadRequest, "invalid_request")
		return
	}
	a.CSRF, _ = formParam(r.PostForm, "csrf_token")
	cookie, cookieErr := r.Cookie("trackline_mcp_oauth_csrf")
	if cookieErr != nil || len(r.PostForm["csrf_token"]) != 1 || !csrfPattern.MatchString(a.CSRF) || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.CSRF)) != 1 {
		http.Error(w, "authorization request expired; restart the connection", http.StatusBadRequest)
		return
	}
	h.clearCSRFCookie(w)
	consent, consentOK := formParam(r.PostForm, "consent")
	if !consentOK || consent != "allow" {
		h.authorizationError(w, r, http.StatusBadRequest, "access_denied")
		return
	}
	email, emailOK := formParam(r.PostForm, "email")
	password, passwordOK := formParam(r.PostForm, "password")
	input := credentials{Email: strings.ToLower(strings.TrimSpace(email)), Password: password}
	if !emailOK || !passwordOK || len(input.Email) > 320 || len(input.Password) > 72 || input.Email == "" || input.Password == "" {
		h.renderOAuthError(w, a, "Invalid email or password.", http.StatusUnauthorized)
		return
	}
	var userID uint64
	var role, passwordHash string
	var needsReset, active bool
	var resetVersion uint64
	err = h.db.QueryRowContext(r.Context(), "SELECT id,role,password_hash,password_reset_required,active,password_reset_version FROM users WHERE email=?", input.Email).Scan(&userID, &role, &passwordHash, &needsReset, &active, &resetVersion)
	if err != nil || !active || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		h.renderOAuthError(w, a, "Invalid email or password.", http.StatusUnauthorized)
		return
	}
	if needsReset {
		h.renderOAuthError(w, a, "Change your Trackline password before connecting Claude.", http.StatusForbidden)
		return
	}
	code, err := randomToken("", 32)
	if err != nil {
		http.Error(w, "could not authorize client", http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO oauth_authorization_codes
		(code_hash,client_id,user_id,redirect_uri,code_challenge,scope,password_reset_version,expires_at)
		VALUES (?,?,?,?,?,?,?,?)`, tokenHash(code), a.ClientID, userID, a.RedirectURI, a.Challenge, a.Scope, resetVersion, time.Now().Add(authorizationTTL)); err != nil {
		http.Error(w, "could not authorize client", http.StatusInternalServerError)
		return
	}
	redirect, _ := url.Parse(a.RedirectURI)
	query := redirect.Query()
	query.Set("code", code)
	if a.State != "" {
		query.Set("state", a.State)
	}
	query.Set("iss", h.mcp.Issuer)
	redirect.RawQuery = query.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func renderOAuthConsent(a oauthAuthorization, message string) string {
	msg := ""
	if message != "" {
		msg = `<p role="alert" style="color:#b42318">` + html.EscapeString(message) + `</p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect Trackline to Claude</title></head><body style="font:16px system-ui;max-width:34rem;margin:4rem auto;padding:0 1rem;color:#202124"><h1>Connect Trackline to Claude</h1><p>Sign in to Trackline and approve Claude to read projects, issues, activity, and project members that your account can access. Claude cannot make changes.</p>` + msg + `<form method="post" action="/oauth/authorize" autocomplete="on"><input type="hidden" name="client_id" value="` + html.EscapeString(a.ClientID) + `"><input type="hidden" name="redirect_uri" value="` + html.EscapeString(a.RedirectURI) + `"><input type="hidden" name="response_type" value="code"><input type="hidden" name="state" value="` + html.EscapeString(a.State) + `"><input type="hidden" name="code_challenge" value="` + html.EscapeString(a.Challenge) + `"><input type="hidden" name="code_challenge_method" value="S256"><input type="hidden" name="scope" value="` + html.EscapeString(a.Scope) + `"><input type="hidden" name="csrf_token" value="` + html.EscapeString(a.CSRF) + `"><label>Email<br><input type="email" name="email" autocomplete="username" required style="width:100%;padding:.7rem;margin:.3rem 0 1rem"></label><label>Password<br><input type="password" name="password" autocomplete="current-password" required style="width:100%;padding:.7rem;margin:.3rem 0 1rem"></label><input type="hidden" name="consent" value="allow"><button type="submit" style="padding:.7rem 1rem">Sign in and allow read-only access</button></form><p>Only the read-only Trackline tools are authorized. Your password is sent only to Trackline and is not shared with Claude.</p></body></html>`
}

func (h *Handler) renderOAuthError(w http.ResponseWriter, a oauthAuthorization, message string, status int) {
	csrf, err := randomToken("", 32)
	if err != nil {
		http.Error(w, "authorization unavailable", http.StatusInternalServerError)
		return
	}
	a.CSRF = csrf
	h.setCSRFCookie(w, csrf)
	h.setOAuthPageHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, renderOAuthConsent(a, message))
}

func (h *Handler) setOAuthPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (h *Handler) setCSRFCookie(w http.ResponseWriter, value string) {
	secure := strings.HasPrefix(h.mcp.Issuer, "https://")
	http.SetCookie(w, &http.Cookie{Name: "trackline_mcp_oauth_csrf", Value: value, Path: "/oauth/authorize", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
}

func (h *Handler) clearCSRFCookie(w http.ResponseWriter) {
	secure := strings.HasPrefix(h.mcp.Issuer, "https://")
	http.SetCookie(w, &http.Cookie{Name: "trackline_mcp_oauth_csrf", Value: "", Path: "/oauth/authorize", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (h *Handler) authorizationError(w http.ResponseWriter, r *http.Request, status int, code string) {
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.PostForm
	}
	redirect := values.Get("redirect_uri")
	clientID := values.Get("client_id")
	if redirect != claudeRedirect || clientID == "" {
		oauthError(w, status, code, "invalid authorization request")
		return
	}
	var registered string
	if h.db.QueryRowContext(r.Context(), "SELECT redirect_uri FROM oauth_clients WHERE client_id=?", clientID).Scan(&registered) != nil || registered != redirect {
		oauthError(w, status, code, "invalid authorization request")
		return
	}
	destination, _ := url.Parse(redirect)
	query := destination.Query()
	query.Set("error", code)
	if state := values.Get("state"); state != "" && len(state) <= 1024 {
		query.Set("state", state)
	}
	query.Set("iss", h.mcp.Issuer)
	destination.RawQuery = query.Encode()
	http.Redirect(w, r, destination.String(), http.StatusFound)
}

func (h *Handler) oauthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	grantType, grantOK := formParam(r.PostForm, "grant_type")
	clientID, clientOK := formParam(r.PostForm, "client_id")
	if !grantOK || !clientOK {
		oauthError(w, http.StatusBadRequest, "invalid_request", "OAuth parameters must occur exactly once in the form body")
		return
	}
	if clientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	resourceValues := r.PostForm["resource"]
	if len(resourceValues) > 1 || (len(resourceValues) == 1 && resourceValues[0] != "" && resourceValues[0] != h.mcp.Resource) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource does not match this MCP server")
		return
	}
	var redirectURI string
	if err := h.db.QueryRowContext(r.Context(), "SELECT redirect_uri FROM oauth_clients WHERE client_id=?", clientID).Scan(&redirectURI); err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	switch grantType {
	case "authorization_code":
		h.exchangeAuthorizationCode(w, r, clientID, redirectURI)
	case "refresh_token":
		h.refreshOAuthToken(w, r, clientID)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant type")
	}
}

func (h *Handler) exchangeAuthorizationCode(w http.ResponseWriter, r *http.Request, clientID, registeredRedirect string) {
	code, codeOK := formParam(r.PostForm, "code")
	redirectURI, redirectOK := formParam(r.PostForm, "redirect_uri")
	verifier, verifierOK := formParam(r.PostForm, "code_verifier")
	if !codeOK || !redirectOK || !verifierOK || code == "" || redirectURI != registeredRedirect || !pkceVerifierPattern.MatchString(verifier) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid")
		return
	}
	challenge := base64.RawURLEncoding.EncodeToString(sha256Bytes(verifier))
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}
	defer tx.Rollback()
	var userID, resetVersion uint64
	var storedRedirect, storedChallenge, scope string
	var expires time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT user_id,redirect_uri,code_challenge,scope,password_reset_version,expires_at
		FROM oauth_authorization_codes WHERE code_hash=? AND client_id=? FOR UPDATE`, tokenHash(code), clientID).Scan(&userID, &storedRedirect, &storedChallenge, &scope, &resetVersion, &expires)
	if err != nil || time.Now().After(expires) || storedRedirect != redirectURI || subtle.ConstantTimeCompare([]byte(storedChallenge), []byte(challenge)) != 1 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
		return
	}
	var role string
	var mustChange, active bool
	var currentVersion uint64
	if err := tx.QueryRowContext(r.Context(), "SELECT role,password_reset_required,active,password_reset_version FROM users WHERE id=? FOR UPDATE", userID).Scan(&role, &mustChange, &active, &currentVersion); err != nil || !active || mustChange || currentVersion != resetVersion {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "account is not eligible for OAuth access")
		return
	}
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM oauth_authorization_codes WHERE code_hash=?", tokenHash(code)); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}
	grantLifetime := accessTokenTTL
	if hasScope(scope, "offline_access") {
		grantLifetime = grantTTL
	}
	grantExpires := time.Now().Add(grantLifetime)
	result, err := tx.ExecContext(r.Context(), "INSERT INTO oauth_grants (client_id,user_id,scope,password_reset_version,expires_at) VALUES (?,?,?,?,?)", clientID, userID, scope, resetVersion, grantExpires)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}
	grantID, err := result.LastInsertId()
	if err != nil || grantID <= 0 {
		oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}
	refreshToken := ""
	if hasScope(scope, "offline_access") {
		refreshToken, err = randomToken("tl_mcp_rt_", 32)
		if err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO oauth_refresh_tokens (token_hash,grant_id,expires_at) VALUES (?,?,?)", tokenHash(refreshToken), grantID, grantExpires); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}
	h.writeTokenResponse(w, userID, role, resetVersion, uint64(grantID), scope, refreshToken)
}

func (h *Handler) refreshOAuthToken(w http.ResponseWriter, r *http.Request, clientID string) {
	oldToken, tokenOK := formParam(r.PostForm, "refresh_token")
	if !tokenOK || !strings.HasPrefix(oldToken, "tl_mcp_rt_") {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token refresh failed")
		return
	}
	defer tx.Rollback()
	var grantID uint64
	var expires time.Time
	var usedAt, revokedAt sql.NullTime
	err = tx.QueryRowContext(r.Context(), "SELECT grant_id,expires_at,used_at,revoked_at FROM oauth_refresh_tokens WHERE token_hash=? FOR UPDATE", tokenHash(oldToken)).Scan(&grantID, &expires, &usedAt, &revokedAt)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	if usedAt.Valid {
		if _, err := tx.ExecContext(r.Context(), "UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,CURRENT_TIMESTAMP) WHERE id=?", grantID); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke reused refresh token")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE oauth_refresh_tokens SET revoked_at=COALESCE(revoked_at,CURRENT_TIMESTAMP) WHERE grant_id=?", grantID); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke reused refresh token")
			return
		}
		if err := tx.Commit(); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke reused refresh token")
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected; reconnect Trackline")
		return
	}
	var userID, resetVersion uint64
	var role, scope string
	var grantExpiry time.Time
	var grantRevoked sql.NullTime
	var userReset, active bool
	var currentVersion uint64
	err = tx.QueryRowContext(r.Context(), `SELECT g.user_id,u.role,g.scope,g.password_reset_version,g.expires_at,g.revoked_at,u.password_reset_required,u.active,u.password_reset_version
		FROM oauth_grants g JOIN users u ON u.id=g.user_id WHERE g.id=? AND g.client_id=? FOR UPDATE`, grantID, clientID).Scan(&userID, &role, &scope, &resetVersion, &grantExpiry, &grantRevoked, &userReset, &active, &currentVersion)
	if err != nil || revokedAt.Valid || grantRevoked.Valid || !expires.After(time.Now()) || !grantExpiry.After(time.Now()) || !active || userReset || currentVersion != resetVersion {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is expired or revoked")
		return
	}
	newRefresh, err := randomToken("tl_mcp_rt_", 32)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token refresh failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE oauth_refresh_tokens SET used_at=CURRENT_TIMESTAMP WHERE token_hash=?", tokenHash(oldToken)); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token refresh failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), "INSERT INTO oauth_refresh_tokens (token_hash,grant_id,expires_at) VALUES (?,?,?)", tokenHash(newRefresh), grantID, grantExpiry); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token refresh failed")
		return
	}
	if err := tx.Commit(); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token refresh failed")
		return
	}
	h.writeTokenResponse(w, userID, role, resetVersion, grantID, scope, newRefresh)
}

func (h *Handler) writeTokenResponse(w http.ResponseWriter, userID uint64, role string, resetVersion, grantID uint64, scope, refresh string) {
	access, err := sharedauth.IssueScopedToken(userID, role, resetVersion, grantID, h.mcp.Resource, scope, h.secret, accessTokenTTL)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue access token")
		return
	}
	response := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(accessTokenTTL.Seconds()), "scope": scope}
	if refresh != "" {
		response["refresh_token"] = refresh
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, response)
}

func (h *Handler) introspectOAuthToken(w http.ResponseWriter, r *http.Request) {
	if len(h.mcp.InternalSecret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Service-Token")), h.mcp.InternalSecret) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.Token) > 8192 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	claims, err := sharedauth.VerifyTokenClaims(input.Token, h.secret)
	if err != nil || claims.TokenUse != "oauth_access" || claims.GrantID == 0 || claims.Audience != h.mcp.Resource || !hasScope(claims.Scope, mcpReadScope) {
		writeJSON(w, map[string]any{"active": false})
		return
	}
	var userID, resetVersion uint64
	var role, scope string
	var grantExpiry time.Time
	var revoked sql.NullTime
	var needsReset, active bool
	var currentVersion uint64
	err = h.db.QueryRowContext(r.Context(), `SELECT g.user_id,u.role,g.scope,g.password_reset_version,g.expires_at,g.revoked_at,u.password_reset_required,u.active,u.password_reset_version
		FROM oauth_grants g JOIN users u ON u.id=g.user_id WHERE g.id=?`, claims.GrantID).Scan(&userID, &role, &scope, &resetVersion, &grantExpiry, &revoked, &needsReset, &active, &currentVersion)
	if err != nil || revoked.Valid || !grantExpiry.After(time.Now()) || !active || needsReset || userID != claims.UserID || role != claims.Role || resetVersion != currentVersion || resetVersion != claims.PasswordResetVersion || scope != claims.Scope {
		writeJSON(w, map[string]any{"active": false})
		return
	}
	writeJSON(w, map[string]any{"active": true, "user_id": userID, "role": role, "password_reset_version": resetVersion, "scope": scope, "audience": claims.Audience})
}

func (h *Handler) revokeOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	clientID, clientOK := formParam(r.PostForm, "client_id")
	token, tokenOK := formParam(r.PostForm, "token")
	if !clientOK || !tokenOK || clientID == "" || token == "" || len(token) > 8192 {
		oauthError(w, http.StatusBadRequest, "invalid_request", "client_id and token are required")
		return
	}
	var registered string
	if err := h.db.QueryRowContext(r.Context(), "SELECT redirect_uri FROM oauth_clients WHERE client_id=?", clientID).Scan(&registered); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusOK)
		} else {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke token")
		}
		return
	}
	if strings.HasPrefix(token, "tl_mcp_rt_") {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE oauth_grants g JOIN oauth_refresh_tokens t ON t.grant_id=g.id
			SET g.revoked_at=COALESCE(g.revoked_at,CURRENT_TIMESTAMP) WHERE t.token_hash=? AND g.client_id=?`, tokenHash(token), clientID); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke token")
			return
		}
	} else if claims, err := sharedauth.VerifyTokenClaims(token, h.secret); err == nil && claims.TokenUse == "oauth_access" && claims.GrantID > 0 {
		if _, err := h.db.ExecContext(r.Context(), "UPDATE oauth_grants SET revoked_at=COALESCE(revoked_at,CURRENT_TIMESTAMP) WHERE id=? AND client_id=?", claims.GrantID, clientID); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke token")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func randomToken(prefix string, bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func hasScope(scope, wanted string) bool {
	for _, candidate := range strings.Fields(scope) {
		if candidate == wanted {
			return true
		}
	}
	return false
}

func formParam(values url.Values, key string) (string, bool) {
	items, ok := values[key]
	if !ok || len(items) != 1 {
		return "", false
	}
	return items[0], true
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSONStatus(w, status, map[string]string{"error": code, "error_description": description})
}
