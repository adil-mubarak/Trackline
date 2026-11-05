package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauth "github.com/adil/bug-issue-tracker/pkg/auth"
	"golang.org/x/crypto/bcrypt"
)

const testOAuthIssuer = "https://trackline.example.test"

func TestMcpOAuthDiscoveryMetadata(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := NewHandler(db, []byte("test-secret-test-secret-test-secret"))
	h.mcp = MCPOAuthSettings{Issuer: testOAuthIssuer, Resource: testOAuthIssuer + "/mcp"}

	tests := []struct {
		path string
		want map[string]any
	}{
		{path: "/.well-known/oauth-protected-resource/mcp", want: map[string]any{"resource": testOAuthIssuer + "/mcp"}},
		{path: "/.well-known/oauth-authorization-server", want: map[string]any{"issuer": testOAuthIssuer, "registration_endpoint": testOAuthIssuer + "/oauth/register", "code_challenge_methods_supported": []any{"S256"}, "token_endpoint_auth_methods_supported": []any{"none"}}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for key, want := range tt.want {
				got, _ := json.Marshal(body[key])
				wantJSON, _ := json.Marshal(want)
				if string(got) != string(wantJSON) {
					t.Errorf("%s=%s want %s", key, got, wantJSON)
				}
			}
		})
	}
}

func TestMcpOAuthDynamicRegistrationOnlyAcceptsClaudeRedirect(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := NewHandler(db, []byte("test-secret-test-secret-test-secret"))
	h.mcp = MCPOAuthSettings{Issuer: testOAuthIssuer, Resource: testOAuthIssuer + "/mcp"}

	invalid := `{"client_name":"test","redirect_uris":["https://attacker.example/callback"]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(invalid))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid redirect status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectExec("INSERT INTO oauth_clients \\(client_id,client_name,redirect_uri\\) VALUES \\(\\?,\\?,\\?\\)").
		WithArgs(sqlmock.AnyArg(), "Claude", claudeRedirect).WillReturnResult(sqlmock.NewResult(0, 1))
	body := `{"client_name":"Claude","redirect_uris":["` + claudeRedirect + `"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		ClientID    string   `json:"client_id"`
		RedirectURI []string `json:"redirect_uris"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !strings.HasPrefix(response.ClientID, "tl_client_") || len(response.RedirectURI) != 1 || response.RedirectURI[0] != claudeRedirect {
		t.Fatalf("registration response=%s err=%v", w.Body.String(), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMcpOAuthCodeFlowUsesPKCEAndIssuesScopedGrant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test-secret-test-secret-test-secret")
	h := NewHandler(db, secret)
	h.mcp = MCPOAuthSettings{Issuer: testOAuthIssuer, Resource: testOAuthIssuer + "/mcp"}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("secure-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("a", 43)
	challenge := base64URLSHA256(verifier)
	scope := "mcp:read offline_access"
	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectQuery("SELECT id,role,password_hash,password_reset_required,active,password_reset_version FROM users WHERE email=\\?").WithArgs("reader@example.test").WillReturnRows(sqlmock.NewRows([]string{"id", "role", "password_hash", "password_reset_required", "active", "password_reset_version"}).AddRow(42, "member", string(passwordHash), false, true, 7))
	mock.ExpectExec("INSERT INTO oauth_authorization_codes").WithArgs(sqlmock.AnyArg(), "client-1", uint64(42), claudeRedirect, challenge, scope, uint64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))

	query := url.Values{"client_id": {"client-1"}, "redirect_uri": {claudeRedirect}, "response_type": {"code"}, "state": {"caller-state"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {scope}}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	if get.Code != http.StatusOK {
		t.Fatalf("authorize GET status=%d body=%s", get.Code, get.Body.String())
	}
	csrfCookie := get.Result().Cookies()[0]
	if csrfCookie.Name != "trackline_mcp_oauth_csrf" || !csrfCookie.HttpOnly || !csrfCookie.Secure {
		t.Fatalf("unsafe csrf cookie: %#v", csrfCookie)
	}

	form := url.Values{}
	for k, values := range query {
		form.Set(k, values[0])
	}
	form.Set("email", "reader@example.test")
	form.Set("password", "secure-password")
	form.Set("consent", "allow")
	form.Set("csrf_token", csrfCookie.Value)
	post := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(csrfCookie)
	postResponse := httptest.NewRecorder()
	h.ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusFound {
		t.Fatalf("authorize POST status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
	callback, err := url.Parse(postResponse.Header().Get("Location"))
	if err != nil || callback.Host != "claude.ai" || callback.Query().Get("state") != "caller-state" || callback.Query().Get("iss") != testOAuthIssuer || callback.Query().Get("code") == "" {
		t.Fatalf("callback=%q err=%v", postResponse.Header().Get("Location"), err)
	}
	code := callback.Query().Get("code")

	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id,redirect_uri,code_challenge,scope,password_reset_version,expires_at").WithArgs(sqlmock.AnyArg(), "client-1").WillReturnRows(sqlmock.NewRows([]string{"user_id", "redirect_uri", "code_challenge", "scope", "password_reset_version", "expires_at"}).AddRow(42, claudeRedirect, challenge, scope, 7, time.Now().Add(time.Minute)))
	mock.ExpectQuery("SELECT role,password_reset_required,active,password_reset_version FROM users WHERE id=\\? FOR UPDATE").WithArgs(uint64(42)).WillReturnRows(sqlmock.NewRows([]string{"role", "password_reset_required", "active", "password_reset_version"}).AddRow("member", false, true, 7))
	mock.ExpectExec("DELETE FROM oauth_authorization_codes WHERE code_hash=\\?").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO oauth_grants ").WithArgs("client-1", uint64(42), scope, uint64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec("INSERT INTO oauth_refresh_tokens ").WithArgs(sqlmock.AnyArg(), uint64(9), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "client_id": {"client-1"}, "code": {code}, "redirect_uri": {claudeRedirect}, "code_verifier": {verifier}}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(tokenForm.Encode()))
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse := httptest.NewRecorder()
	h.ServeHTTP(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("token response status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	claims, err := sharedauth.VerifyTokenClaims(tokens.AccessToken, secret)
	if err != nil || claims.UserID != 42 || claims.TokenUse != "oauth_access" || claims.Audience != testOAuthIssuer+"/mcp" || claims.Scope != scope || claims.GrantID != 9 || tokens.RefreshToken == "" || tokens.Scope != scope {
		t.Fatalf("tokens=%#v claims=%#v err=%v", tokens, claims, err)
	}
	oldRefresh := tokens.RefreshToken
	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT grant_id,expires_at,used_at,revoked_at FROM oauth_refresh_tokens").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"grant_id", "expires_at", "used_at", "revoked_at"}).AddRow(9, time.Now().Add(24*time.Hour), nil, nil))
	mock.ExpectQuery("SELECT g.user_id,u.role,g.scope,g.password_reset_version,g.expires_at,g.revoked_at,u.password_reset_required,u.active,u.password_reset_version").WithArgs(uint64(9), "client-1").WillReturnRows(sqlmock.NewRows([]string{"user_id", "role", "scope", "password_reset_version", "expires_at", "revoked_at", "password_reset_required", "active", "password_reset_version"}).AddRow(42, "member", scope, 7, time.Now().Add(24*time.Hour), nil, false, true, 7))
	mock.ExpectExec("UPDATE oauth_refresh_tokens SET used_at=CURRENT_TIMESTAMP WHERE token_hash=\\?").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO oauth_refresh_tokens ").WithArgs(sqlmock.AnyArg(), uint64(9), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "client_id": {"client-1"}, "refresh_token": {oldRefresh}}
	refreshRequest := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(refreshForm.Encode()))
	refreshRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refreshResponse := httptest.NewRecorder()
	h.ServeHTTP(refreshResponse, refreshRequest)
	if refreshResponse.Code != http.StatusOK {
		t.Fatalf("refresh response status=%d body=%s", refreshResponse.Code, refreshResponse.Body.String())
	}
	var rotated struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(refreshResponse.Body.Bytes(), &rotated); err != nil || rotated.RefreshToken == "" || rotated.RefreshToken == oldRefresh {
		t.Fatalf("refresh token did not rotate: response=%s err=%v", refreshResponse.Body.String(), err)
	}

	// Replaying a rotated refresh token revokes the grant to detect theft.
	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT grant_id,expires_at,used_at,revoked_at FROM oauth_refresh_tokens").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"grant_id", "expires_at", "used_at", "revoked_at"}).AddRow(9, time.Now().Add(24*time.Hour), time.Now(), nil))
	mock.ExpectExec("UPDATE oauth_grants SET revoked_at=COALESCE\\(revoked_at,CURRENT_TIMESTAMP\\) WHERE id=\\?").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE oauth_refresh_tokens SET revoked_at=COALESCE\\(revoked_at,CURRENT_TIMESTAMP\\) WHERE grant_id=\\?").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	replayRequest := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(refreshForm.Encode()))
	replayRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replayResponse := httptest.NewRecorder()
	h.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusBadRequest || !strings.Contains(replayResponse.Body.String(), "invalid_grant") {
		t.Fatalf("refresh replay status=%d body=%s", replayResponse.Code, replayResponse.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMcpOAuthIntrospectionRequiresInternalSecretAndCurrentGrant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("test-secret-test-secret-test-secret")
	h := NewHandler(db, secret)
	h.mcp = MCPOAuthSettings{Issuer: testOAuthIssuer, Resource: testOAuthIssuer + "/mcp", InternalSecret: []byte("internal-introspection-secret-32-bytes")}
	token, err := sharedauth.IssueScopedToken(42, "member", 7, 9, h.mcp.Resource, "mcp:read offline_access", secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	request := httptest.NewRequest(http.MethodPost, "/internal/oauth/introspect", strings.NewReader(string(body)))
	request.Header.Set("X-Internal-Service-Token", "wrong-internal-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong internal secret status=%d", w.Code)
	}

	query := `SELECT g.user_id,u.role,g.scope,g.password_reset_version,g.expires_at,g.revoked_at,u.password_reset_required,u.active,u.password_reset_version
		FROM oauth_grants g JOIN users u ON u.id=g.user_id WHERE g.id=\?`
	mock.ExpectQuery(query).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "role", "scope", "password_reset_version", "expires_at", "revoked_at", "password_reset_required", "active", "password_reset_version"}).AddRow(42, "member", "mcp:read offline_access", 7, time.Now().Add(time.Hour), nil, false, true, 7))
	request = httptest.NewRequest(http.MethodPost, "/internal/oauth/introspect", strings.NewReader(string(body)))
	request.Header.Set("X-Internal-Service-Token", string(h.mcp.InternalSecret))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	var active struct {
		Active bool `json:"active"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &active); err != nil || w.Code != http.StatusOK || !active.Active {
		t.Fatalf("active introspection status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	mock.ExpectQuery(query).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "role", "scope", "password_reset_version", "expires_at", "revoked_at", "password_reset_required", "active", "password_reset_version"}).AddRow(42, "member", "mcp:read offline_access", 7, time.Now().Add(time.Hour), time.Now(), false, true, 7))
	request = httptest.NewRequest(http.MethodPost, "/internal/oauth/introspect", strings.NewReader(string(body)))
	request.Header.Set("X-Internal-Service-Token", string(h.mcp.InternalSecret))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	active = struct {
		Active bool `json:"active"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &active); err != nil || w.Code != http.StatusOK || active.Active {
		t.Fatalf("revoked introspection status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMcpOAuthRevocationReportsDatabaseFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := NewHandler(db, []byte("test-secret-test-secret-test-secret"))
	mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
	mock.ExpectExec("UPDATE oauth_grants g JOIN oauth_refresh_tokens t ON t.grant_id=g.id").WithArgs(sqlmock.AnyArg(), "client-1").WillReturnError(errors.New("database unavailable"))
	form := url.Values{"client_id": {"client-1"}, "token": {"tl_mcp_rt_test-token"}}
	r := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("revocation status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMcpOAuthRefreshReplayPersistenceErrorsReturnServerError(t *testing.T) {
	for _, failAt := range []string{"grant_update", "commit"} {
		t.Run(failAt, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			h := NewHandler(db, []byte("test-secret-test-secret-test-secret"))
			mock.ExpectQuery("SELECT redirect_uri FROM oauth_clients WHERE client_id=\\?").WithArgs("client-1").WillReturnRows(sqlmock.NewRows([]string{"redirect_uri"}).AddRow(claudeRedirect))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT grant_id,expires_at,used_at,revoked_at FROM oauth_refresh_tokens").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"grant_id", "expires_at", "used_at", "revoked_at"}).AddRow(9, time.Now().Add(time.Hour), time.Now(), nil))
			if failAt == "grant_update" {
				mock.ExpectExec("UPDATE oauth_grants SET revoked_at=COALESCE\\(revoked_at,CURRENT_TIMESTAMP\\) WHERE id=\\?").WithArgs(uint64(9)).WillReturnError(errors.New("database unavailable"))
				mock.ExpectRollback()
			} else {
				mock.ExpectExec("UPDATE oauth_grants SET revoked_at=COALESCE\\(revoked_at,CURRENT_TIMESTAMP\\) WHERE id=\\?").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE oauth_refresh_tokens SET revoked_at=COALESCE\\(revoked_at,CURRENT_TIMESTAMP\\) WHERE grant_id=\\?").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			}
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {"client-1"}, "refresh_token": {"tl_mcp_rt_reused"}}
			r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("replay persistence failure status=%d body=%s", w.Code, w.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func base64URLSHA256(value string) string {
	return base64.RawURLEncoding.EncodeToString(sha256Bytes(value))
}
