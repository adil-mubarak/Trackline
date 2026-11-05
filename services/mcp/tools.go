package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxResultBytes = 48 << 10

type toolSpec struct{ name, description, schema string }

var toolSpecs = []toolSpec{
	{"list_projects", "List projects the authenticated caller can access.", `{"type":"object","properties":{},"additionalProperties":false}`},
	{"get_project", "Get details for an accessible project.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
	{"project_issue_summary", "Get issue counts by status and priority for an accessible project.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
	{"list_project_issues", "List up to 25 issues in an accessible project with optional status, priority, and assignee filters.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"status":{"type":"string","enum":["todo","in_progress","done"]},"priority":{"type":"string","enum":["low","medium","high","critical"]},"assignee_id":{"type":"integer","minimum":1},"after_id":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":25}},"required":["project_id"],"additionalProperties":false}`},
	{"get_issue_details", "Get details for an issue visible to the caller.", `{"type":"object","properties":{"issue_id":{"type":"integer","minimum":1}},"required":["issue_id"],"additionalProperties":false}`},
	{"recent_project_activity", "Get up to 20 recent recorded events for a project. Actor IDs are resolved to usernames or Unknown user.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["project_id"],"additionalProperties":false}`},
	{"list_project_members", "List usernames and roles for an accessible project. No email addresses are returned.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
}

func newTrackerServer(gatewayURL, authorization, requestID string, client *http.Client, internalSecret string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "trackline-mcp", Version: "1.0.0"}, nil)
	falseValue := false
	for _, spec := range toolSpecs {
		var schema any
		if json.Unmarshal([]byte(spec.schema), &schema) != nil {
			continue
		}
		spec := spec
		s.AddTool(&mcp.Tool{Name: spec.name, Description: spec.description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &falseValue, OpenWorldHint: &falseValue}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			encoded, err := json.Marshal(req.Params.Arguments)
			if err != nil {
				return nil, errors.New("invalid tool arguments")
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			data, err := execute(ctx, gatewayURL, authorization, requestID, spec.name, encoded, client, internalSecret)
			if err != nil {
				return nil, err
			}
			if len(data) > maxResultBytes {
				return nil, errors.New("tracker result exceeds tool size limit")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
	}
	return s
}

func execute(ctx context.Context, gateway, authorization, requestID, name string, raw []byte, client *http.Client, internalSecrets ...string) (json.RawMessage, error) {
	internalSecret := ""
	if len(internalSecrets) > 0 {
		internalSecret = internalSecrets[0]
	}
	if len(raw) > 8<<10 {
		return nil, errors.New("tool arguments too large")
	}
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	args := struct {
		ProjectID  uint64 `json:"project_id"`
		IssueID    uint64 `json:"issue_id"`
		AssigneeID uint64 `json:"assignee_id"`
		AfterID    uint64 `json:"after_id"`
		Limit      int    `json:"limit"`
		Status     string `json:"status"`
		Priority   string `json:"priority"`
	}{}
	if name == "list_projects" {
		if err := decodeArgs(raw, &struct{}{}); err != nil {
			return nil, err
		}
	} else {
		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}
	}
	path := ""
	switch name {
	case "list_projects":
		path = "/projects"
	case "get_project":
		if args.ProjectID == 0 {
			return nil, errors.New("project_id must be positive")
		}
		path = fmt.Sprintf("/projects/%d", args.ProjectID)
	case "project_issue_summary":
		if args.ProjectID == 0 {
			return nil, errors.New("project_id must be positive")
		}
		path = "/issues/analytics?project_id=" + strconv.FormatUint(args.ProjectID, 10)
	case "list_project_issues":
		if args.ProjectID == 0 || !oneOf(args.Status, "", "todo", "in_progress", "done") || !oneOf(args.Priority, "", "low", "medium", "high", "critical") || args.Limit < 0 || args.Limit > 25 {
			return nil, errors.New("invalid issue-list arguments")
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		q := url.Values{}
		q.Set("project_id", strconv.FormatUint(args.ProjectID, 10))
		q.Set("limit", strconv.Itoa(args.Limit))
		if args.Status != "" {
			q.Set("status", args.Status)
		}
		if args.Priority != "" {
			q.Set("priority", args.Priority)
		}
		if args.AssigneeID > 0 {
			q.Set("assignee_id", strconv.FormatUint(args.AssigneeID, 10))
		}
		if args.AfterID > 0 {
			q.Set("after_id", strconv.FormatUint(args.AfterID, 10))
		}
		path = "/issues?" + q.Encode()
	case "get_issue_details":
		if args.IssueID == 0 {
			return nil, errors.New("issue_id must be positive")
		}
		path = fmt.Sprintf("/issues/%d", args.IssueID)
	case "recent_project_activity":
		if args.ProjectID == 0 || args.Limit < 0 || args.Limit > 20 {
			return nil, errors.New("invalid activity arguments")
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		q := url.Values{}
		q.Set("project_id", strconv.FormatUint(args.ProjectID, 10))
		q.Set("limit", strconv.Itoa(args.Limit))
		data, err := callAPI(ctx, client, gateway, authorization, requestID, "GET", "/activity?"+q.Encode(), nil, internalSecret)
		if err != nil {
			return nil, err
		}
		return resolveActivity(ctx, client, gateway, authorization, requestID, data, internalSecret)
	case "list_project_members":
		if args.ProjectID == 0 {
			return nil, errors.New("project_id must be positive")
		}
		data, err := callAPI(ctx, client, gateway, authorization, requestID, "GET", fmt.Sprintf("/projects/%d/members", args.ProjectID), nil, internalSecret)
		if err != nil {
			return nil, err
		}
		return resolveMembers(ctx, client, gateway, authorization, requestID, data, internalSecret)
	default:
		return nil, errors.New("unknown read-only tracker tool")
	}
	return callAPI(ctx, client, gateway, authorization, requestID, "GET", path, nil, internalSecret)
}

func decodeArgs(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errors.New("invalid tool arguments")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("arguments must be one JSON object")
	}
	return nil
}
func oneOf(v string, choices ...string) bool {
	for _, c := range choices {
		if v == c {
			return true
		}
	}
	return false
}

func callAPI(ctx context.Context, client *http.Client, gateway, authorization, requestID, method, path string, body []byte, internalSecret string) (json.RawMessage, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid internal path")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(gateway, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not create tracker request")
	}
	req.Header.Set("Authorization", authorization)
	if path == "/users/names" && internalSecret != "" {
		req.Header.Set("X-MCP-Internal-Service-Token", internalSecret)
	}
	req.Header.Set("Accept", "application/json")
	if requestID != "" && len(requestID) <= 128 {
		req.Header.Set("X-Request-ID", requestID)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("tracker API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker API returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResultBytes+1))
	if err != nil || len(data) > maxResultBytes || !json.Valid(data) {
		return nil, errors.New("tracker API returned invalid or oversized JSON")
	}
	return data, nil
}

func resolveMembers(ctx context.Context, c *http.Client, g, a, r string, data []byte, internalSecret string) (json.RawMessage, error) {
	var members []struct {
		UserID    uint64 `json:"user_id"`
		Role      string `json:"role"`
		Synthetic bool   `json:"synthetic,omitempty"`
	}
	if json.Unmarshal(data, &members) != nil {
		return nil, errors.New("invalid project members response")
	}
	ids := make([]uint64, 0, len(members))
	for _, m := range members {
		if m.UserID > 0 {
			ids = append(ids, m.UserID)
		}
	}
	names, err := lookupNames(ctx, c, g, a, r, ids, internalSecret)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		n := names[m.UserID]
		if n == "" {
			n = "Unknown user"
		}
		item := map[string]any{"username": n, "role": m.Role}
		if m.Synthetic {
			item["synthetic"] = true
		}
		out = append(out, item)
	}
	return json.Marshal(out)
}

func resolveActivity(ctx context.Context, c *http.Client, g, a, r string, data []byte, internalSecret string) (json.RawMessage, error) {
	var events []struct {
		ID         uint64          `json:"id"`
		ProjectID  uint64          `json:"project_id"`
		IssueID    uint64          `json:"issue_id"`
		ActorID    uint64          `json:"actor_id"`
		EventType  string          `json:"event_type"`
		Payload    json.RawMessage `json:"payload"`
		OccurredAt string          `json:"occurred_at"`
	}
	if json.Unmarshal(data, &events) != nil {
		return nil, errors.New("invalid activity response")
	}
	ids := make([]uint64, 0, len(events))
	seen := map[uint64]bool{}
	for _, e := range events {
		if e.ActorID > 0 && !seen[e.ActorID] {
			seen[e.ActorID] = true
			ids = append(ids, e.ActorID)
		}
	}
	names, err := lookupNames(ctx, c, g, a, r, ids, internalSecret)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		n := names[e.ActorID]
		if n == "" {
			n = "Unknown user"
		}
		out = append(out, map[string]any{"id": e.ID, "project_id": e.ProjectID, "issue_id": e.IssueID, "actor_name": n, "event_type": e.EventType, "payload": e.Payload, "occurred_at": e.OccurredAt})
	}
	return json.Marshal(out)
}

func lookupNames(ctx context.Context, c *http.Client, g, a, r string, ids []uint64, internalSecret string) (map[uint64]string, error) {
	out := map[uint64]string{}
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		body, err := json.Marshal(struct {
			UserIDs []uint64 `json:"user_ids"`
		}{ids[start:end]})
		if err != nil {
			return nil, err
		}
		data, err := callAPI(ctx, c, g, a, r, "POST", "/users/names", body, internalSecret)
		if err != nil {
			return nil, err
		}
		var users []struct {
			ID       uint64 `json:"id"`
			Username string `json:"username"`
		}
		if json.Unmarshal(data, &users) != nil {
			return nil, errors.New("invalid auth username response")
		}
		for _, u := range users {
			out[u.ID] = strings.TrimSpace(u.Username)
		}
	}
	return out, nil
}
