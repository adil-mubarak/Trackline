package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	if !strings.Contains(result.Reply, "Please try again") || !strings.Contains(result.Reply, "explicitly linked") {
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
