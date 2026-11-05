package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testGitHubApp(t *testing.T, handler http.Handler) (*githubApp, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	app, err := newGitHubApp("12345", "67890", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), server.URL, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return app, server
}

func TestGitHubAppJWTAndInstallationRepositoryListing(t *testing.T) {
	var tokenCalls, listCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/67890/access_tokens":
			tokenCalls++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Error("missing App JWT")
			}
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			if len(parts) != 3 {
				t.Fatal("invalid JWT")
			}
			claimBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			if json.Unmarshal(claimBytes, &claims) != nil || claims["iss"] != "12345" {
				t.Errorf("invalid JWT claims: %s", claimBytes)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"installation-secret"}`)
		case "/installation/repositories":
			listCalls++
			if r.URL.Query().Get("per_page") != "100" || r.Header.Get("Authorization") != "Bearer installation-secret" {
				t.Errorf("unexpected repo request: %s %v", r.URL.Path, r.Header)
			}
			fmt.Fprint(w, `{"repositories":[{"id":42,"name":"tracker","owner":{"login":"acme"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	app, err := newGitHubApp("12345", "67890", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := app.appJWT(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(jwt, ".")) != 3 {
		t.Fatal("JWT has wrong segment count")
	}
	repos, err := app.repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0] != (githubRepository{ID: 42, Owner: "acme", Name: "tracker"}) || tokenCalls != 1 || listCalls != 1 {
		t.Fatalf("repos=%+v tokenCalls=%d listCalls=%d", repos, tokenCalls, listCalls)
	}
}

func TestGitHubHandlersVerifyInstallationRepository(t *testing.T) {
	app, server := testGitHubApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "access_tokens") {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"token"}`)
			return
		}
		fmt.Fprint(w, `{"repositories":[{"id":42,"name":"tracker","owner":{"login":"acme"}}]}`)
	}))
	defer server.Close()
	for _, tt := range []struct {
		path   string
		status int
		body   string
	}{{"/internal/github/repositories/42", 200, ""}, {"/internal/github/repositories/99", 404, ""}, {"/internal/github/repositories/nope", 400, ""}} {
		r := httptest.NewRequest(http.MethodGet, tt.path, nil)
		w := httptest.NewRecorder()
		githubHandlers(app).ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Errorf("%s status=%d body=%s", tt.path, w.Code, w.Body.String())
		}
		if tt.status == 200 && !strings.Contains(w.Body.String(), `"owner":"acme"`) {
			t.Errorf("invalid repository response: %s", w.Body.String())
		}
	}
}

func TestGitHubHandlersAndMCPDisableGracefullyWithoutApp(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/github/repositories", nil)
	response := httptest.NewRecorder()
	githubHandlers(nil).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured picker status=%d", response.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, definitions, err := newTrackerMCP(ctx, "http://127.0.0.1:1", "caller", "", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, tool := range definitions {
		if tool.Function.Name == "linked_pr_evidence" || tool.Function.Name == "linked_pr_code_changes" {
			t.Fatalf("GitHub tool %q advertised without App credentials", tool.Function.Name)
		}
	}
}

func TestIdentityTransportForwardsCallerContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-User-ID") != "55" || r.Header.Get("X-Request-ID") != "req-1" {
			t.Errorf("identity headers not forwarded: %#v", r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	request.Header.Set("Authorization", "Bearer attacker-controlled")
	response, err := (&http.Client{Transport: &identityTransport{bearer: "private-token", userID: "55", requestID: "req-1"}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestLinkedPRCodeChangesAreBoundedAndRedacted(t *testing.T) {
	app, githubServer := testGitHubApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "access_tokens"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"token"}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/7":
			fmt.Fprint(w, `{"number":7,"title":"Fix login","html_url":"https://github.com/acme/tracker/pull/7"}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/7/files":
			if r.URL.Query().Get("per_page") != "11" {
				t.Errorf("file query is not bounded: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"filename":"services/auth/login.go","status":"modified","additions":2,"deletions":1,"patch":"@@ -1 +1 @@\n-password = \"hunter2\"\n+password = \"hunter3\""},{"filename":".env","status":"modified","additions":1,"deletions":0,"patch":"+OPENAI_API_KEY=sk-secret"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer githubServer.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issues/8":
			fmt.Fprint(w, `{"project_id":3}`)
		case "/issues/8/pull-requests":
			fmt.Fprint(w, `[{"repository_id":42,"number":7}]`)
		case "/projects/3/repository":
			fmt.Fprint(w, `{"id":42,"owner":"acme","name":"tracker"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	data, err := linkedPRCodeChanges(context.Background(), gateway.URL, "caller", "request", json.RawMessage(`{"project_id":3,"issue_id":8}`), gateway.Client(), app)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "services/auth/login.go") || !strings.Contains(text, "[REDACTED SENSITIVE ASSIGNMENT]") {
		t.Fatalf("expected source diff with secret redaction: %s", text)
	}
	if strings.Contains(text, "hunter2") || strings.Contains(text, "hunter3") || strings.Contains(text, "OPENAI_API_KEY") || strings.Contains(text, "sk-secret") {
		t.Fatalf("sensitive code leaked: %s", text)
	}
	if !strings.Contains(text, "excluded by code-evidence policy") {
		t.Fatalf("sensitive path omission was not reported: %s", text)
	}
}

func TestCodeEvidencePathAndPatchFiltering(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{{"src/main.go", true}, {"vendor/lib/a.go", false}, {".env.production", false}, {"certs/private.pem", false}, {"frontend/dist/app.js", false}, {"src/app.min.js", false}} {
		if got := safeCodePath(test.path); got != test.want {
			t.Errorf("safeCodePath(%q) = %t, want %t", test.path, got, test.want)
		}
	}
	patch := `+const apiKey = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
+-----BEGIN PRIVATE KEY-----
+secret-key-material
+-----END PRIVATE KEY-----`
	redacted := redactCodePatch(patch)
	for _, raw := range []string{"ghp_abcdefghijklmnopqrstuvwxyz", "secret-key-material"} {
		if strings.Contains(redacted, raw) {
			t.Fatalf("redaction left secret material in patch: %s", redacted)
		}
	}
	if got := codeFileStatus(strings.Repeat("modified", maxCodeBytes)); got != "other" {
		t.Fatalf("untrusted status was not normalized: %q", got)
	}
}

func TestGitHubEvidenceRequiresExplicitOpenRouterOptIn(t *testing.T) {
	app := &githubApp{}
	if got := githubEvidenceForChat(app, false); got != nil {
		t.Fatal("GitHub evidence should remain disabled without OpenRouter approval")
	}
	if got := githubEvidenceForChat(app, true); got != app {
		t.Fatal("explicit OpenRouter approval should enable the configured GitHub App tool")
	}
}

func TestCodeToolCanBeOptedInSeparatelyFromMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, definitions, err := newTrackerMCP(ctx, "http://127.0.0.1:1", "caller", "", http.DefaultClient, nil, &githubApp{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var codeTool, metadataTool bool
	for _, tool := range definitions {
		codeTool = codeTool || tool.Function.Name == "linked_pr_code_changes"
		metadataTool = metadataTool || tool.Function.Name == "linked_pr_evidence"
	}
	if !codeTool || metadataTool {
		t.Fatalf("unexpected opt-in tool exposure: code=%t metadata=%t", codeTool, metadataTool)
	}
}

func TestGitHubAPIURLIsRestricted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	for _, apiURL := range []string{"http://api.github.com", "https://example.com", "https://api.github.com:8443"} {
		if _, err := newGitHubApp("123", "456", privateKey, apiURL, nil); err == nil {
			t.Errorf("accepted unsafe API URL %q", apiURL)
		}
	}
}

func TestLinkedPREvidenceRequiresExplicitMatchingLinksAndBounds(t *testing.T) {
	var githubCalls int
	app, githubServer := testGitHubApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		githubCalls++
		switch {
		case strings.Contains(r.URL.Path, "access_tokens"):
			w.WriteHeader(201)
			fmt.Fprint(w, `{"token":"token"}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/7":
			fmt.Fprint(w, `{"number":7,"title":"Fix","state":"closed","merged":true,"merged_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-01T10:00:00Z","html_url":"https://github.com/acme/tracker/pull/7","head":{"sha":"abc123"}}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/7/reviews":
			fmt.Fprint(w, `[{"state":"APPROVED","submitted_at":"2026-01-01T00:00:00Z"}]`)
		case strings.Contains(r.URL.Path, "check-runs"):
			fmt.Fprint(w, `{"check_runs":[{"name":"ci","status":"completed","conclusion":"success","completed_at":"2026-10-01T09:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer githubServer.Close()
	var wrongProject bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issues/8":
			if wrongProject {
				fmt.Fprint(w, `{"id":8,"project_id":99}`)
			} else {
				fmt.Fprint(w, `{"id":8,"project_id":9}`)
			}
		case "/issues/8/pull-requests":
			fmt.Fprint(w, `[{"repository_id":42,"number":7}]`)
		case "/projects/9/repository":
			fmt.Fprint(w, `{"id":42,"owner":"acme","name":"tracker"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	args := json.RawMessage(`{"project_id":9,"issue_id":8}`)
	data, err := linkedPREvidence(context.Background(), gateway.URL, "caller", "", args, gateway.Client(), app)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 20<<10 || !strings.Contains(string(data), `"conclusion":"success"`) || !strings.Contains(string(data), `"merged":true`) || !strings.Contains(string(data), `"completed_at":"2026-10-01T09:00:00Z"`) || strings.Contains(string(data), "abc123") {
		t.Fatalf("unexpected/sensitive evidence: %s", data)
	}
	if githubCalls != 4 {
		t.Fatalf("GitHub calls=%d want 4", githubCalls)
	}
	wrongProject = true
	before := githubCalls
	if _, err := linkedPREvidence(context.Background(), gateway.URL, "caller", "", args, gateway.Client(), app); err == nil {
		t.Fatal("accepted issue from another project")
	}
	if githubCalls != before {
		t.Fatal("GitHub called before verifying issue project")
	}
}

func TestLinkedPREvidenceRejectsUnlinkedOrMismatchedPRAndMissingGitHubConfig(t *testing.T) {
	for i, links := range []string{`[]`, `[{"repository_id":77,"number":7}]`, `[{"repository_id":42,"number":7},{"repository_id":42,"number":8},{"repository_id":42,"number":9},{"repository_id":42,"number":10},{"repository_id":42,"number":11},{"repository_id":42,"number":12}]`} {
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/issues/8":
				fmt.Fprint(w, `{"id":8,"project_id":9}`)
			case "/issues/8/pull-requests":
				fmt.Fprint(w, links)
			default:
				fmt.Fprint(w, `{"id":42,"owner":"acme","name":"tracker"}`)
			}
		}))
		calls := 0
		app, gh := testGitHubApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
		_, err := linkedPREvidence(context.Background(), gateway.URL, "caller", "", json.RawMessage(`{"project_id":9,"issue_id":8}`), gateway.Client(), app)
		if i < 2 && err != nil {
			t.Errorf("unlinked or mismatched issue should safely produce no evidence: %v", err)
		} else if i == 2 && err == nil {
			t.Errorf("accepted too many linked PRs %s", links)
		}
		if calls != 0 {
			t.Errorf("made GitHub calls for rejected link data: %d", calls)
		}
		gateway.Close()
		gh.Close()
	}
	if _, err := newGitHubApp("", "", "", "", nil); err == nil {
		t.Fatal("missing credentials accepted")
	}
}
