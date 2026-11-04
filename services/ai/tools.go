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

const (
	maxToolCalls       = 6
	maxToolRounds      = 3
	maxToolResultBytes = 48 << 10
	maxToolTotalBytes  = 128 << 10
)

type openRouterTool struct {
	Type     string                   `json:"type"`
	Function openRouterToolDefinition `json:"function"`
}

type openRouterToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openRouterToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type trackerToolSpec struct {
	name, description, schema string
}

var trackerToolSpecs = []trackerToolSpec{
	{"list_projects", "List only projects the authenticated caller can access.", `{"type":"object","properties":{},"additionalProperties":false}`},
	{"get_project", "Get details for a project the authenticated caller can access.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
	{"project_issue_summary", "Get current issue counts by status and priority for an accessible project.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
	{"list_project_issues", "List up to 25 issues in an accessible project, optionally filtered by status, priority, or assignee. Results use the issue service's ID cursor.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"status":{"type":"string","enum":["todo","in_progress","done"]},"priority":{"type":"string","enum":["low","medium","high","critical"]},"assignee_id":{"type":"integer","minimum":1},"after_id":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":25}},"required":["project_id"],"additionalProperties":false}`},
	{"get_issue_details", "Get current details for an issue visible to the authenticated caller.", `{"type":"object","properties":{"issue_id":{"type":"integer","minimum":1}},"required":["issue_id"],"additionalProperties":false}`},
	{"recent_project_activity", "Get up to 20 recent recorded activity entries for an accessible project. Activity is evidence of recorded events, not proof that a person is currently working.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["project_id"],"additionalProperties":false}`},
	{"list_project_members", "List member usernames and roles for an accessible project. Only IDs returned by the Project service are resolved through Auth; no email addresses are returned.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1}},"required":["project_id"],"additionalProperties":false}`},
}

func newTrackerMCP(ctx context.Context, gatewayURL, bearer, requestID string, client *http.Client, github ...*githubApp) (*mcp.ClientSession, []openRouterTool, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "bug-issue-tracker-readonly", Version: "1.0.0"}, &mcp.ServerOptions{})
	readOnly, openWorld := false, false
	specs := trackerToolSpecs
	var app *githubApp
	var codeApp *githubApp
	if len(github) > 0 {
		app = github[0]
	}
	if len(github) > 1 {
		codeApp = github[1]
	}
	if app != nil {
		specs = append(append([]trackerToolSpec(nil), trackerToolSpecs...), trackerToolSpec{"linked_pr_evidence", "Read metadata-only evidence for pull requests explicitly linked to this issue, and only if the project is connected to the same installed repository.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"issue_id":{"type":"integer","minimum":1}},"required":["project_id","issue_id"],"additionalProperties":false}`})
	}
	if codeApp != nil {
		if app == nil {
			specs = append([]trackerToolSpec(nil), trackerToolSpecs...)
		}
		specs = append(specs, trackerToolSpec{"linked_pr_code_changes", "Read bounded changed-file diff excerpts for pull requests explicitly linked to this issue in its connected installed repository. Do not request arbitrary repository files.", `{"type":"object","properties":{"project_id":{"type":"integer","minimum":1},"issue_id":{"type":"integer","minimum":1}},"required":["project_id","issue_id"],"additionalProperties":false}`})
	}
	for _, spec := range specs {
		var schema any
		if err := json.Unmarshal([]byte(spec.schema), &schema); err != nil {
			return nil, nil, fmt.Errorf("decode %s schema: %w", spec.name, err)
		}
		tool := &mcp.Tool{
			Name:        spec.name,
			Description: spec.description,
			InputSchema: schema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &readOnly, OpenWorldHint: &openWorld},
		}
		server.AddTool(tool, func(callCtx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var data json.RawMessage
			var err error
			if request.Params.Name == "linked_pr_evidence" {
				data, err = linkedPREvidence(callCtx, gatewayURL, bearer, requestID, request.Params.Arguments, client, app)
			} else if request.Params.Name == "linked_pr_code_changes" {
				data, err = linkedPRCodeChanges(callCtx, gatewayURL, bearer, requestID, request.Params.Arguments, client, codeApp)
			} else {
				data, err = executeTrackerTool(callCtx, gatewayURL, bearer, requestID, request.Params.Name, request.Params.Arguments, client)
			}
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect MCP server: %w", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "tracker-chat-orchestrator", Version: "1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		return nil, nil, fmt.Errorf("connect MCP client: %w", err)
	}
	listed, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		_ = clientSession.Close()
		_ = serverSession.Close()
		return nil, nil, fmt.Errorf("discover MCP tools: %w", err)
	}
	definitions := make([]openRouterTool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		parameters, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, nil, fmt.Errorf("encode %s MCP schema: %w", tool.Name, err)
		}
		definitions = append(definitions, openRouterTool{Type: "function", Function: openRouterToolDefinition{Name: tool.Name, Description: tool.Description, Parameters: parameters}})
	}
	return clientSession, definitions, nil
}

func linkedPREvidence(ctx context.Context, gatewayURL, bearerToken, requestID string, arguments json.RawMessage, client *http.Client, app *githubApp) (json.RawMessage, error) {
	if app == nil {
		return nil, errors.New("GitHub evidence is unavailable")
	}
	var args struct {
		ProjectID uint64 `json:"project_id"`
		IssueID   uint64 `json:"issue_id"`
	}
	if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 || args.IssueID == 0 {
		return nil, errors.New("invalid project or issue ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	issueData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, fmt.Sprintf("/issues/%d", args.IssueID), nil)
	if err != nil {
		return nil, err
	}
	var issue struct {
		ProjectID uint64 `json:"project_id"`
	}
	if json.Unmarshal(issueData, &issue) != nil || issue.ProjectID != args.ProjectID {
		return nil, errors.New("issue does not belong to supplied project")
	}
	linksData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, fmt.Sprintf("/issues/%d/pull-requests", args.IssueID), nil)
	if err != nil {
		return nil, err
	}
	var links []struct {
		RepositoryID uint64 `json:"repository_id"`
		Number       uint64 `json:"number"`
	}
	if json.Unmarshal(linksData, &links) != nil {
		return nil, errors.New("invalid issue pull request links response")
	}
	if len(links) == 0 {
		return json.RawMessage(`[]`), nil
	}
	if len(links) > maxLinkedPRs {
		return nil, errors.New("issue has too many linked pull requests")
	}
	repoData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, fmt.Sprintf("/projects/%d/repository", args.ProjectID), nil)
	if err != nil {
		return nil, err
	}
	var connected *githubRepository
	if string(repoData) != "null" {
		var repo githubRepository
		if json.Unmarshal(repoData, &repo) != nil || repo.ID == 0 {
			return nil, errors.New("invalid project repository response")
		}
		connected = &repo
	}
	if connected == nil {
		return nil, errors.New("project has no connected GitHub repository")
	}
	matchingLinks := links[:0]
	for _, link := range links {
		if link.Number == 0 || link.RepositoryID == 0 {
			return nil, errors.New("invalid pull request link")
		}
		if link.RepositoryID == connected.ID {
			matchingLinks = append(matchingLinks, link)
		}
	}
	links = matchingLinks
	if len(links) == 0 {
		return json.RawMessage(`[]`), nil
	}
	token, err := app.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	type evidence struct {
		Number     uint64 `json:"number"`
		Title      string `json:"title"`
		State      string `json:"state"`
		Draft      bool   `json:"draft"`
		Merged     bool   `json:"merged"`
		MergedAt   string `json:"merged_at,omitempty"`
		UpdatedAt  string `json:"updated_at,omitempty"`
		ObservedAt string `json:"observed_at"`
		URL        string `json:"url"`
		HeadSHA    string `json:"head_sha"`
		Reviews    []struct {
			State       string `json:"state"`
			SubmittedAt string `json:"submitted_at"`
		} `json:"reviews"`
		Checks []struct {
			Name        string `json:"name"`
			Status      string `json:"status"`
			Conclusion  string `json:"conclusion,omitempty"`
			StartedAt   string `json:"started_at,omitempty"`
			CompletedAt string `json:"completed_at,omitempty"`
		} `json:"checks"`
	}
	out := make([]evidence, 0, len(links))
	for _, link := range links {
		prefix := "/repos/" + url.PathEscape(connected.Owner) + "/" + url.PathEscape(connected.Name) + "/pulls/" + strconv.FormatUint(link.Number, 10)
		data, err := app.apiRequest(ctx, http.MethodGet, prefix, token)
		if err != nil {
			return nil, err
		}
		var pr struct {
			Number    uint64 `json:"number"`
			Title     string `json:"title"`
			State     string `json:"state"`
			Draft     bool   `json:"draft"`
			HTMLURL   string `json:"html_url"`
			Merged    bool   `json:"merged"`
			MergedAt  string `json:"merged_at"`
			UpdatedAt string `json:"updated_at"`
			Head      struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if json.Unmarshal(data, &pr) != nil || pr.Number != link.Number || pr.HTMLURL == "" {
			return nil, errors.New("invalid pull request response")
		}
		item := evidence{Number: pr.Number, Title: pr.Title, State: pr.State, Draft: pr.Draft, Merged: pr.Merged, MergedAt: pr.MergedAt, UpdatedAt: pr.UpdatedAt, ObservedAt: time.Now().UTC().Format(time.RFC3339), URL: pr.HTMLURL, HeadSHA: pr.Head.SHA}
		reviewsData, err := app.apiRequest(ctx, http.MethodGet, prefix+"/reviews?per_page=10", token)
		if err != nil {
			return nil, err
		}
		var reviews []struct {
			State       string `json:"state"`
			SubmittedAt string `json:"submitted_at"`
		}
		if json.Unmarshal(reviewsData, &reviews) != nil {
			return nil, errors.New("invalid pull request reviews response")
		}
		if len(reviews) > 10 {
			reviews = reviews[:10]
		}
		for _, review := range reviews {
			item.Reviews = append(item.Reviews, struct {
				State       string `json:"state"`
				SubmittedAt string `json:"submitted_at"`
			}{review.State, review.SubmittedAt})
		}
		if pr.Head.SHA != "" {
			checksData, err := app.apiRequest(ctx, http.MethodGet, "/repos/"+url.PathEscape(connected.Owner)+"/"+url.PathEscape(connected.Name)+"/commits/"+url.PathEscape(pr.Head.SHA)+"/check-runs?per_page=10", token)
			if err != nil {
				return nil, err
			}
			var checks struct {
				CheckRuns []struct {
					Name        string `json:"name"`
					Status      string `json:"status"`
					Conclusion  string `json:"conclusion"`
					StartedAt   string `json:"started_at"`
					CompletedAt string `json:"completed_at"`
				} `json:"check_runs"`
			}
			if json.Unmarshal(checksData, &checks) != nil {
				return nil, errors.New("invalid pull request checks response")
			}
			if len(checks.CheckRuns) > 10 {
				checks.CheckRuns = checks.CheckRuns[:10]
			}
			for _, check := range checks.CheckRuns {
				item.Checks = append(item.Checks, struct {
					Name        string `json:"name"`
					Status      string `json:"status"`
					Conclusion  string `json:"conclusion,omitempty"`
					StartedAt   string `json:"started_at,omitempty"`
					CompletedAt string `json:"completed_at,omitempty"`
				}{check.Name, check.Status, check.Conclusion, check.StartedAt, check.CompletedAt})
			}
		}
		item.HeadSHA = ""
		out = append(out, item)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 20<<10 {
		return nil, errors.New("pull request evidence exceeds tool limit")
	}
	return encoded, nil
}

func callMCPTool(ctx context.Context, session *mcp.ClientSession, name, arguments string) (string, error) {
	var args any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", errors.New("invalid tool arguments")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", errors.New("MCP tool call failed")
	}
	if result.IsError {
		return "", errors.New("MCP tool returned an error")
	}
	var out strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(text.Text)
		}
	}
	if out.Len() == 0 {
		return "", errors.New("MCP tool returned no content")
	}
	return out.String(), nil
}

func executeTrackerTool(ctx context.Context, gatewayURL, bearerToken, requestID, name string, arguments json.RawMessage, client *http.Client) (json.RawMessage, error) {
	if bearerToken == "" {
		return nil, errors.New("authenticated caller required")
	}
	if gatewayURL == "" {
		return nil, errors.New("tracker API gateway is not configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var path string
	switch name {
	case "list_projects":
		var args struct{}
		if err := decodeToolArguments(arguments, &args); err != nil {
			return nil, err
		}
		path = "/projects"
	case "get_project":
		var args struct {
			ProjectID uint64 `json:"project_id"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 {
			return nil, errors.New("project_id must be a positive integer")
		}
		path = fmt.Sprintf("/projects/%d", args.ProjectID)
	case "project_issue_summary":
		var args struct {
			ProjectID uint64 `json:"project_id"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 {
			return nil, errors.New("project_id must be a positive integer")
		}
		path = "/issues/analytics?project_id=" + strconv.FormatUint(args.ProjectID, 10)
	case "list_project_issues":
		var args struct {
			ProjectID  uint64 `json:"project_id"`
			Status     string `json:"status"`
			Priority   string `json:"priority"`
			AssigneeID uint64 `json:"assignee_id"`
			AfterID    uint64 `json:"after_id"`
			Limit      int    `json:"limit"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 {
			return nil, errors.New("invalid issue-list arguments")
		}
		if args.Status != "" && args.Status != "todo" && args.Status != "in_progress" && args.Status != "done" {
			return nil, errors.New("unsupported issue status")
		}
		if args.Priority != "" && args.Priority != "low" && args.Priority != "medium" && args.Priority != "high" && args.Priority != "critical" {
			return nil, errors.New("unsupported issue priority")
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		if args.Limit < 1 || args.Limit > 25 {
			return nil, errors.New("limit must be between 1 and 25")
		}
		query := url.Values{}
		query.Set("project_id", strconv.FormatUint(args.ProjectID, 10))
		query.Set("limit", strconv.Itoa(args.Limit))
		if args.Status != "" {
			query.Set("status", args.Status)
		}
		if args.Priority != "" {
			query.Set("priority", args.Priority)
		}
		if args.AssigneeID > 0 {
			query.Set("assignee_id", strconv.FormatUint(args.AssigneeID, 10))
		}
		if args.AfterID > 0 {
			query.Set("after_id", strconv.FormatUint(args.AfterID, 10))
		}
		path = "/issues?" + query.Encode()
	case "get_issue_details":
		var args struct {
			IssueID uint64 `json:"issue_id"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.IssueID == 0 {
			return nil, errors.New("issue_id must be a positive integer")
		}
		path = fmt.Sprintf("/issues/%d", args.IssueID)
	case "recent_project_activity":
		var args struct {
			ProjectID uint64 `json:"project_id"`
			Limit     int    `json:"limit"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 {
			return nil, errors.New("invalid activity arguments")
		}
		if args.Limit == 0 {
			args.Limit = 10
		}
		if args.Limit < 1 || args.Limit > 20 {
			return nil, errors.New("limit must be between 1 and 20")
		}
		query := url.Values{}
		query.Set("project_id", strconv.FormatUint(args.ProjectID, 10))
		query.Set("limit", strconv.Itoa(args.Limit))
		path = "/activity?" + query.Encode()
	case "list_project_members":
		var args struct {
			ProjectID uint64 `json:"project_id"`
		}
		if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 {
			return nil, errors.New("project_id must be a positive integer")
		}
		return listProjectMembers(ctx, gatewayURL, bearerToken, requestID, args.ProjectID, client)
	default:
		return nil, errors.New("unknown read-only tracker tool")
	}
	return callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, path, nil)
}

func listProjectMembers(ctx context.Context, gatewayURL, bearerToken, requestID string, projectID uint64, client *http.Client) (json.RawMessage, error) {
	membersPath := fmt.Sprintf("/projects/%d/members", projectID)
	data, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, membersPath, nil)
	if err != nil {
		return nil, err
	}
	var members []struct {
		UserID    uint64 `json:"user_id"`
		Role      string `json:"role"`
		Synthetic bool   `json:"synthetic,omitempty"`
	}
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, errors.New("invalid project-members response")
	}
	if len(members) == 0 {
		return json.RawMessage("[]"), nil
	}
	userIDs := make([]uint64, 0, len(members))
	for _, member := range members {
		if member.UserID > 0 {
			userIDs = append(userIDs, member.UserID)
		}
	}
	if len(userIDs) == 0 {
		return json.RawMessage("[]"), nil
	}
	nameRequest, err := json.Marshal(struct {
		UserIDs []uint64 `json:"user_ids"`
	}{UserIDs: userIDs})
	if err != nil {
		return nil, fmt.Errorf("encode member-name lookup: %w", err)
	}
	namesData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodPost, "/users/names", nameRequest)
	if err != nil {
		return nil, err
	}
	var names []struct {
		ID       uint64 `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(namesData, &names); err != nil {
		return nil, errors.New("invalid auth usernames response")
	}
	nameByID := make(map[uint64]string, len(names))
	for _, item := range names {
		nameByID[item.ID] = item.Username
	}
	type memberResult struct {
		UserID    uint64 `json:"user_id"`
		Username  string `json:"username,omitempty"`
		Role      string `json:"role"`
		Synthetic bool   `json:"synthetic,omitempty"`
	}
	result := make([]memberResult, 0, len(members))
	for _, member := range members {
		result = append(result, memberResult{
			UserID:    member.UserID,
			Username:  nameByID[member.UserID],
			Role:      member.Role,
			Synthetic: member.Synthetic,
		})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode project members: %w", err)
	}
	return encoded, nil
}

func callGatewayJSON(ctx context.Context, client *http.Client, gatewayURL, bearerToken, requestID, method, path string, body []byte) (json.RawMessage, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid internal API path")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(gatewayURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not create tracker API request")
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" && len(requestID) <= 128 {
		request.Header.Set("X-Request-ID", requestID)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("tracker API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker API returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxToolResultBytes+1))
	if err != nil {
		return nil, errors.New("could not read tracker API response")
	}
	if len(data) > maxToolResultBytes {
		return nil, errors.New("tracker API result exceeds tool size limit")
	}
	if !json.Valid(data) {
		return nil, errors.New("tracker API returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

func decodeToolArguments(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid tool arguments")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("tool arguments must contain one JSON object")
	}
	return nil
}
