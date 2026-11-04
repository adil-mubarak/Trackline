package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenAIDatabaseRegistersTracklineTLS(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYSQL_TLS_CA_FILE", caPath)

	db, err := openAIDatabase("ai:PASSWORD@tcp(localhost:3306)/ai_db?parseTime=true&tls=trackline")
	if err != nil {
		t.Fatalf("openAIDatabase() rejected the deployed TLS DSN: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChatHandlerCallsOpenRouterAndReturnsReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected OpenRouter path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q, want Bearer test-secret", got)
		}
		var request openRouterChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OpenRouter request: %v", err)
		}
		if request.Model != "nvidia/nemotron-3-ultra-550b-a55b:free" || request.Stream || !request.Reasoning.Enabled {
			t.Errorf("unexpected OpenRouter request: %+v", request)
		}
		if len(request.Messages) != 2 || request.Messages[1].Content != "Say hello" {
			t.Errorf("unexpected OpenRouter messages: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"role":"assistant","content":"Hello from OpenRouter."}}]}`)
	}))
	defer server.Close()

	handler := newAIHandler("test-secret", server.URL, "nvidia/nemotron-3-ultra-550b-a55b:free", server.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"Say hello"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got chatResult
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if got.Reply != "Hello from OpenRouter." || got.Model != "nvidia/nemotron-3-ultra-550b-a55b:free" {
		t.Fatalf("response = %+v", got)
	}
}

func TestChatDoesNotExposeUnexecutedToolSyntaxAsAssistantReply(t *testing.T) {
	const providerReply = `<tool_call><function=list_pull_requests><parameter=project_id>28</parameter></function></tool_call>`
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"`+providerReply+`"}}]}`)
	}))
	defer provider.Close()

	handler := newAIHandler("test-secret", provider.URL, "test-model", provider.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"What is the GitHub status?"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result chatResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode chat result: %v", err)
	}
	if strings.Contains(result.Reply, "<tool_call") || strings.Contains(result.Reply, "list_pull_requests") {
		t.Fatalf("unexecuted tool syntax leaked to user: %q", result.Reply)
	}
	if !strings.Contains(result.Reply, "structured format") || !strings.Contains(result.Reply, "explicitly linked") {
		t.Fatalf("unexpected safe fallback: %q", result.Reply)
	}
}

func TestContainsUnexecutedToolCallRequiresCompleteMarkup(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "complete call", content: `<tool_call><function=list_pull_requests><parameter=project_id>28</parameter></function></tool_call>`, want: true},
		{name: "discussion of opening tag", content: `The string <tool_call> can appear in an example.`, want: false},
		{name: "discussion of function marker", content: `Use the syntax <function=name> in the protocol.`, want: false},
		{name: "incomplete markup", content: `<tool_call><function=list_pull_requests>`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsUnexecutedToolCall(test.content); got != test.want {
				t.Fatalf("containsUnexecutedToolCall() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCodeReviewRequestRequiresPRContextAndCodeIntent(t *testing.T) {
	if !requestsLinkedPRCodeReview("For issue #35, explain code changes in its linked PR") {
		t.Fatal("expected linked PR code review request to be recognized")
	}
	if !requestsLinkedPRCodeReview("Review code changes in issue #35") {
		t.Fatal("explicit issue code review should not require the user to mention PR")
	}
	if requestsLinkedPRCodeReview("What is the status of PR #6?") {
		t.Fatal("metadata-only request should not enter code review mode")
	}
	if requestsLinkedPRCodeReview("Review the status of issue #35") || requestsLinkedPRCodeReview("Review the status of PR #6") {
		t.Fatal("metadata status requests should not enter code review mode")
	}
	if id, ok := explicitIssueID("For issue #35 in MCP Testing"); !ok || id != 35 {
		t.Fatalf("explicitIssueID() = %d, %t; want 35, true", id, ok)
	}
	if _, ok := explicitIssueID("Compare issue #35 and issue #36"); ok {
		t.Fatal("ambiguous issue references should not resolve")
	}
}

func TestDoneTaskSummaryRequestDetection(t *testing.T) {
	if !requestsDoneTaskSummary("Which user has more tasks done overall in every project?") {
		t.Fatal("expected cross-project done-task summary request to be recognized")
	}
	if requestsDoneTaskSummary("How many tasks are done in this project?") {
		t.Fatal("single-project task summary should not enter cross-project aggregation")
	}
	if requestsDoneTaskSummary("Which user has more open tasks across projects?") {
		t.Fatal("open task request should not enter done-task aggregation")
	}
	if !requestsIssueCountChart("Draw an unranked graph of project and user completed issues") {
		t.Fatal("expected factual issue-count graph request to be recognized")
	}
	if requestsIssueCountChart("Show a graph of employee productivity") {
		t.Fatal("must not fabricate a productivity chart without issue-count data")
	}
	if requestsIssueCountChart("Show a graph of total issues by project") {
		t.Fatal("must not silently substitute done counts for an unspecified total issue metric")
	}
}

func TestDoneTaskChartsContainOnlyAlphabeticalFactualCounts(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects":
			fmt.Fprint(w, `[{"id":8,"name":"Beta"},{"id":9,"name":"Alpha"}]`)
		case "/issues/analytics":
			if r.URL.Query().Get("project_id") == "8" {
				fmt.Fprint(w, `{"project_id":8,"by_status":{"done":3},"done_by_assignee":[{"assignee_id":7,"count":3}]}`)
			} else {
				fmt.Fprint(w, `{"project_id":9,"by_status":{"done":1},"done_by_assignee":[{"assignee_id":8,"count":1}]}`)
			}
		case "/users/names":
			fmt.Fprint(w, `[{"id":7,"username":"zoe"},{"id":8,"username":"alex"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	result, err := summarizeDoneTasksAcrossProjectsWithCharts(context.Background(), gateway.URL, "caller-token", "request", gateway.Client(), "Draw a graph of done issue counts by project and user")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Charts) != 2 || result.Charts[0].Data[0].Label != "Alpha" || result.Charts[1].Data[0].Label != "alex" || result.Charts[1].Data[1].Label != "zoe" {
		t.Fatalf("charts should contain project/user counts in alphabetical order: %+v", result.Charts)
	}
	if strings.Contains(result.Reply, "User 7") || strings.Contains(result.Reply, "assignee_id") || !strings.Contains(result.Charts[1].Note, "not ranked") || !strings.Contains(result.Charts[1].Note, "not employee performance") {
		t.Fatalf("raw user identifiers or performance ranking language leaked: %+v", result)
	}
}

func TestRecentProjectActivityResolvesActorNamesWithoutReturningIDs(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/activity":
			fmt.Fprint(w, `[{"id":5,"project_id":3,"issue_id":12,"actor_id":942,"event_type":"IssueCreated","payload":{},"occurred_at":"2026-11-04T08:00:00Z"},{"id":6,"project_id":3,"issue_id":13,"actor_id":943,"event_type":"IssueUpdated","payload":{},"occurred_at":"2026-11-04T08:01:00Z"}]`)
		case "/users/names":
			fmt.Fprint(w, `[{"id":942,"username":"alex"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	data, err := recentProjectActivity(context.Background(), gateway.URL, "caller-token", "request", "/activity?project_id=3&limit=2", gateway.Client())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "actor_id") || strings.Contains(text, "942") || strings.Contains(text, "943") || !strings.Contains(text, `"actor_name":"alex"`) || !strings.Contains(text, `"actor_name":"Unknown user"`) {
		t.Fatalf("activity actors must be names, not IDs: %s", text)
	}
}

func TestDoneTaskSummaryUsesAuthorizedProjectsAndReturnsUnrankedCounts(t *testing.T) {
	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		t.Error("deterministic tracker aggregation must not call OpenRouter")
	}))
	defer provider.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("gateway request lost caller authorization: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects":
			fmt.Fprint(w, `[{"id":8,"name":"Beta Project"},{"id":9,"name":"Alpha Project"}]`)
		case "/issues/analytics":
			if r.URL.Query().Get("include_assignees") != "true" {
				t.Errorf("assignee analytics not requested: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("project_id") == "8" {
				fmt.Fprint(w, `{"project_id":8,"by_status":{"done":5},"done_by_assignee":[{"assignee_id":11,"count":4},{"assignee_id":24,"count":1}],"done_unassigned":2}`)
			} else {
				fmt.Fprint(w, `{"project_id":9,"by_status":{"done":2},"done_by_assignee":[{"assignee_id":11,"count":1},{"assignee_id":24,"count":1}]}`)
			}
		case "/users/names":
			fmt.Fprint(w, `[{"id":11,"username":"zoe"},{"id":24,"username":"alex"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	handler := newAIHandlerWithGateway("provider-key", provider.URL, "test-model", gateway.URL, gateway.Client())
	request := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"Which user has more tasks that are done overall in every project?"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result chatResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 0 || !strings.Contains(result.Reply, "**Issues marked done across your 2 accessible projects**") || !strings.Contains(result.Reply, "| alex | 2 |") || !strings.Contains(result.Reply, "| zoe | 5 |") || !strings.Contains(result.Reply, "Unassigned issues marked done: 2") || !strings.Contains(result.Reply, "The list is not ranked") {
		t.Fatalf("unexpected non-ranked task summary: providerCalls=%d reply=%s", providerCalls, result.Reply)
	}
	if strings.Index(result.Reply, "| alex |") > strings.Index(result.Reply, "| zoe |") {
		t.Fatalf("summary should sort names, not counts: %s", result.Reply)
	}
}

func TestDoneTaskSummaryFailsClosedWhenAnyProjectCannotBeRead(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects":
			fmt.Fprint(w, `[{"id":8,"name":"Visible"},{"id":9,"name":"Restricted"}]`)
		case "/issues/analytics":
			if r.URL.Query().Get("project_id") == "9" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			fmt.Fprint(w, `{"project_id":8,"by_status":{"done":1},"done_by_assignee":[{"assignee_id":11,"count":1}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	_, err := summarizeDoneTasksAcrossProjects(context.Background(), gateway.URL, "caller-token", "request", gateway.Client())
	if err == nil {
		t.Fatal("expected incomplete project access to fail the summary rather than return partial totals")
	}
}

func TestDoneTaskSummaryResolvesNamesInAuthBatches(t *testing.T) {
	nameCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects":
			fmt.Fprint(w, `[{"id":8,"name":"One"}]`)
		case "/issues/analytics":
			var items []struct {
				AssigneeID uint64 `json:"assignee_id"`
				Count      uint64 `json:"count"`
			}
			for id := uint64(1); id <= 101; id++ {
				items = append(items, struct {
					AssigneeID uint64 `json:"assignee_id"`
					Count      uint64 `json:"count"`
				}{AssigneeID: id, Count: 1})
			}
			body, _ := json.Marshal(map[string]any{"project_id": 8, "by_status": map[string]uint64{"done": 101}, "done_by_assignee": items})
			_, _ = w.Write(body)
		case "/users/names":
			nameCalls++
			var input struct {
				UserIDs []uint64 `json:"user_ids"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.UserIDs) == 0 || len(input.UserIDs) > 100 {
				t.Errorf("invalid bounded Auth name lookup batch: %+v", input.UserIDs)
			}
			names := make([]map[string]any, 0, len(input.UserIDs))
			for _, id := range input.UserIDs {
				names = append(names, map[string]any{"id": id, "username": fmt.Sprintf("user-%03d", id)})
			}
			_ = json.NewEncoder(w).Encode(names)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	result, err := summarizeDoneTasksAcrossProjects(context.Background(), gateway.URL, "caller-token", "request", gateway.Client())
	if err != nil {
		t.Fatal(err)
	}
	if nameCalls != 2 || !strings.Contains(result, "Issues marked done | Contributing projects") || !strings.Contains(result, "| user-101 | 1 | One (1) |") {
		t.Fatalf("expected two bounded name batches and all users in result; calls=%d", nameCalls)
	}
}

func TestExplicitLinkedPRCodeReviewPrefetchesWithoutModelToolCalls(t *testing.T) {
	app, githubServer := testGitHubApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "access_tokens"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"installation-token"}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/6":
			fmt.Fprint(w, `{"number":6,"title":"Fix issue","html_url":"https://github.com/acme/tracker/pull/6"}`)
		case r.URL.Path == "/repos/acme/tracker/pulls/6/files":
			fmt.Fprint(w, `[{"filename":"src/fix.go","status":"modified","additions":1,"deletions":0,"patch":"@@ -1 +1 @@\n-oldValue := false\n+newValue := true"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer githubServer.Close()
	var gatewayCalls, providerCalls int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		if r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("gateway request did not preserve caller identity: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/issues/35":
			fmt.Fprint(w, `{"project_id":8}`)
		case "/issues/35/pull-requests":
			fmt.Fprint(w, `[{"repository_id":42,"number":6}]`)
		case "/projects/8/repository":
			fmt.Fprint(w, `{"id":42,"owner":"acme","name":"tracker"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		var request openRouterChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		if len(request.Tools) != 0 || request.ToolChoice != "" {
			t.Errorf("prefetched code path must not rely on model tool calls: %+v", request)
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, "newValue := true") || !strings.Contains(request.Messages[1].Content, "https://github.com/acme/tracker/pull/6") {
			t.Errorf("provider did not receive bounded linked-PR evidence: %+v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"PR #6 changes src/fix.go to set newValue to true. The diff contains no test execution evidence."}}]}`)
	}))
	defer provider.Close()
	handler := newAIHandlerWithIntegrations("provider-key", provider.URL, "test-model", gateway.URL, provider.Client(), app, false, true)
	request := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"For issue #35 in MCP Testing, explain the changes in its linked PR(s), summarize risks and tests, and cite the PR and changed files."}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result chatResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Reply, "src/fix.go") || providerCalls != 1 || gatewayCalls != 4 {
		t.Fatalf("reply=%q providerCalls=%d gatewayCalls=%d", result.Reply, providerCalls, gatewayCalls)
	}
}

func TestChatRunsAuthorizedTrackerToolAndReturnsGroundedReply(t *testing.T) {
	const callerToken = "caller-jwt-never-send-to-provider"
	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer model-secret" {
			t.Errorf("provider Authorization = %q, want model key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider body: %v", err)
		}
		if strings.Contains(string(body), callerToken) {
			t.Error("caller JWT was sent to OpenRouter")
		}
		if providerCalls == 1 {
			var request openRouterChatRequest
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("decode first provider request: %v", err)
			}
			if len(request.Tools) == 0 {
				t.Fatal("authenticated chat request did not expose read-only tools")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_projects","type":"function","function":{"name":"list_projects","arguments":"{}"}}]}}]}`)
			return
		}
		var request openRouterChatRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode follow-up provider request: %v", err)
		}
		foundEvidence := false
		for _, message := range request.Messages {
			if message.Role == "tool" && message.ToolCallID == "call_projects" && strings.Contains(message.Content, "Project Alpha") {
				foundEvidence = true
			}
		}
		if !foundEvidence {
			t.Error("tracker tool result was not passed back to the model")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"role":"assistant","content":"Project Alpha has one tracked record."}}]}`)
	}))
	defer provider.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("unexpected tracker path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+callerToken {
			t.Errorf("tracker Authorization = %q", got)
		}
		if got := r.Header.Get("X-Request-ID"); got != "req-ai-123" {
			t.Errorf("tracker request ID = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":4,"name":"Project Alpha","description":"A project","owner_id":7}]`)
	}))
	defer gateway.Close()

	handler := newAIHandlerWithGateway("model-secret", provider.URL, "nvidia/nemotron-3-ultra-550b-a55b:free", gateway.URL, provider.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"Summarize my projects"}`))
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set("X-Request-ID", "req-ai-123")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result chatResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode chat result: %v", err)
	}
	if result.Reply != "Project Alpha has one tracked record." || providerCalls != 2 {
		t.Fatalf("result=%+v provider calls=%d", result, providerCalls)
	}
}

func TestTrackerToolsRejectUnsafeArgumentsAndMissingCaller(t *testing.T) {
	client := &http.Client{}
	for _, test := range []struct {
		name   string
		tool   string
		args   string
		caller string
	}{
		{name: "missing caller", tool: "list_projects", args: `{}`},
		{name: "unknown tool", tool: "delete_issue", args: `{}`, caller: "caller-token"},
		{name: "zero project", tool: "get_project", args: `{"project_id":0}`, caller: "caller-token"},
		{name: "unknown argument", tool: "list_projects", args: `{"user_id":42}`, caller: "caller-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := executeTrackerTool(context.Background(), "http://127.0.0.1:1", test.caller, "", test.tool, json.RawMessage(test.args), client)
			if err == nil {
				t.Fatal("expected tool validation failure")
			}
		})
	}
}

func TestChatHandlerAcceptsMultibyteMessageWithinCharacterLimit(t *testing.T) {
	message := strings.Repeat("あ", 3000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openRouterChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OpenRouter request: %v", err)
		}
		if request.Messages[1].Content != message {
			t.Errorf("forwarded message rune count = %d, want %d", len([]rune(request.Messages[1].Content)), len([]rune(message)))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	handler := newAIHandler("test-secret", server.URL, "nvidia/nemotron-3-ultra-550b-a55b:free", server.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"`+message+`"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestChatHandlerAcceptsMaximumUnicodeMessageWhenJSONEscaped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openRouterChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode OpenRouter request: %v", err)
		}
		if got := len([]rune(request.Messages[1].Content)); got != 8000 {
			t.Errorf("forwarded message has %d runes, want 8000", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"nvidia/nemotron-3-ultra-550b-a55b:free","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	body := `{"message":"` + strings.Repeat(`\u3042`, 8000) + `"}`
	handler := newAIHandler("test-secret", server.URL, "nvidia/nemotron-3-ultra-550b-a55b:free", server.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestChatHandlerRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "method", method: http.MethodGet, contentType: "application/json", wantStatus: http.StatusMethodNotAllowed},
		{name: "content type", method: http.MethodPost, contentType: "text/plain", body: `{"message":"hi"}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "invalid json", method: http.MethodPost, contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "blank message", method: http.MethodPost, contentType: "application/json", body: `{"message":"  "}`, wantStatus: http.StatusBadRequest},
		{name: "message too long", method: http.MethodPost, contentType: "application/json", body: `{"message":"` + strings.Repeat("a", 8001) + `"}`, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newAIHandler("test-secret", "http://127.0.0.1:1", "nvidia/nemotron-3-ultra-550b-a55b:free", http.DefaultClient)
			req := httptest.NewRequest(tt.method, "/ai/chat", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestChatHandlerRequiresAPIKey(t *testing.T) {
	handler := newAIHandler("", "https://openrouter.ai/api/v1", "nvidia/nemotron-3-ultra-550b-a55b:free", http.DefaultClient)
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestAIReadinessRequiresAPIKey(t *testing.T) {
	for _, tt := range []struct {
		name    string
		apiKey  string
		wantErr bool
	}{
		{name: "configured", apiKey: "test-secret"},
		{name: "missing key", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := readinessError(tt.apiKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("readinessError() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChatHandlerDoesNotExposeOpenRouterErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive provider detail", http.StatusUnauthorized)
	}))
	defer server.Close()
	handler := newAIHandler("test-secret", server.URL, "nvidia/nemotron-3-ultra-550b-a55b:free", server.Client())
	req := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if strings.Contains(response.Body.String(), "sensitive provider detail") {
		t.Fatalf("provider body leaked: %s", response.Body.String())
	}
}
