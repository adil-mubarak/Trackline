package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCodePRs        = 5
	maxCodeFiles      = 10
	maxCodeBytes      = 16 << 10
	maxCodePatchBytes = 8 << 10
	maxCodeFileBytes  = 3 << 10
)

var (
	secretAssignment = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|client[_-]?secret|private[_-]?key)\s*[:=]\s*[^\s,;]+`)
	githubToken      = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)
	awsAccessKey     = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	googleAPIKey     = regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`)
)

type linkedCodePR struct {
	Number    uint64           `json:"number"`
	Title     string           `json:"title"`
	URL       string           `json:"url"`
	Truncated bool             `json:"truncated,omitempty"`
	Files     []linkedCodeFile `json:"files"`
}

type linkedCodeFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
	Omitted   string `json:"omitted,omitempty"`
}

func linkedPRCodeEvidenceForIssue(ctx context.Context, gatewayURL, bearerToken, requestID string, issueID uint64, client *http.Client, app *githubApp) (json.RawMessage, error) {
	if issueID == 0 || strings.TrimSpace(bearerToken) == "" {
		return nil, errors.New("an authenticated caller and explicit issue ID are required")
	}
	issueData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, fmt.Sprintf("/issues/%d", issueID), nil)
	if err != nil {
		return nil, err
	}
	var issue struct {
		ProjectID uint64 `json:"project_id"`
	}
	if json.Unmarshal(issueData, &issue) != nil || issue.ProjectID == 0 {
		return nil, errors.New("issue has no valid project")
	}
	arguments, err := json.Marshal(struct {
		ProjectID uint64 `json:"project_id"`
		IssueID   uint64 `json:"issue_id"`
	}{ProjectID: issue.ProjectID, IssueID: issueID})
	if err != nil {
		return nil, fmt.Errorf("encode code evidence request: %w", err)
	}
	return linkedPRCodeChanges(ctx, gatewayURL, bearerToken, requestID, arguments, client, app)
}

func linkedPRCodeChanges(ctx context.Context, gatewayURL, bearerToken, requestID string, arguments json.RawMessage, client *http.Client, app *githubApp) (json.RawMessage, error) {
	if app == nil {
		return nil, errors.New("GitHub code evidence is unavailable")
	}
	var args struct {
		ProjectID uint64 `json:"project_id"`
		IssueID   uint64 `json:"issue_id"`
	}
	if err := decodeToolArguments(arguments, &args); err != nil || args.ProjectID == 0 || args.IssueID == 0 {
		return nil, errors.New("invalid project or issue ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
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
	if len(links) > maxLinkedPRs {
		return nil, errors.New("issue has too many linked pull requests")
	}
	if len(links) == 0 {
		return json.RawMessage(`[]`), nil
	}
	repoData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, fmt.Sprintf("/projects/%d/repository", args.ProjectID), nil)
	if err != nil {
		return nil, err
	}
	var connected githubRepository
	if json.Unmarshal(repoData, &connected) != nil || connected.ID == 0 || connected.Owner == "" || connected.Name == "" {
		return nil, errors.New("project has no valid connected GitHub repository")
	}
	matching := make([]uint64, 0, len(links))
	for _, link := range links {
		if link.Number == 0 || link.RepositoryID == 0 {
			return nil, errors.New("invalid pull request link")
		}
		if link.RepositoryID == connected.ID {
			matching = append(matching, link.Number)
		}
	}
	if len(matching) == 0 {
		return json.RawMessage(`[]`), nil
	}
	token, err := app.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	output := make([]linkedCodePR, 0, len(matching))
	used := 0
	fileCount := 0
	for _, number := range matching {
		if len(output) >= maxCodePRs {
			break
		}
		prefix := "/repos/" + url.PathEscape(connected.Owner) + "/" + url.PathEscape(connected.Name) + "/pulls/" + fmt.Sprint(number)
		prData, err := app.apiRequest(ctx, http.MethodGet, prefix, token)
		if err != nil {
			return nil, err
		}
		var pr struct {
			Number  uint64 `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
		}
		if json.Unmarshal(prData, &pr) != nil || pr.Number != number || pr.HTMLURL == "" {
			return nil, errors.New("invalid pull request response")
		}
		filesData, err := app.apiRequest(ctx, http.MethodGet, prefix+"/files?per_page=11", token)
		if err != nil {
			return nil, err
		}
		var files []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Patch     string `json:"patch"`
		}
		if json.Unmarshal(filesData, &files) != nil {
			return nil, errors.New("invalid pull request files response")
		}
		item := linkedCodePR{Number: number, Title: boundedCodeText(pr.Title, 256), URL: boundedCodeText(pr.HTMLURL, 512), Files: make([]linkedCodeFile, 0, maxCodeFiles)}
		if len(files) > maxCodeFiles {
			item.Truncated = true
			files = files[:maxCodeFiles]
		}
		for _, file := range files {
			if fileCount >= maxCodeFiles {
				item.Truncated = true
				break
			}
			fileCount++
			filePath := boundedCodeText(file.Filename, 256)
			status := codeFileStatus(file.Status)
			if !safeCodePath(file.Filename) {
				item.Truncated = true
				item.Files = append(item.Files, linkedCodeFile{Path: filePath, Status: status, Additions: file.Additions, Deletions: file.Deletions, Omitted: "excluded by code-evidence policy"})
				continue
			}
			patch := redactCodePatch(file.Patch)
			if patch == "" {
				item.Truncated = true
				item.Files = append(item.Files, linkedCodeFile{Path: filePath, Status: status, Additions: file.Additions, Deletions: file.Deletions, Omitted: "patch unavailable or removed by secret filter"})
				continue
			}
			remaining := maxCodePatchBytes - used
			if remaining <= 0 {
				item.Truncated = true
				item.Files = append(item.Files, linkedCodeFile{Path: filePath, Status: status, Additions: file.Additions, Deletions: file.Deletions, Omitted: "diff evidence size limit reached"})
				continue
			}
			if len(patch) > maxCodeFileBytes {
				patch = patch[:maxCodeFileBytes]
				item.Truncated = true
			}
			if len(patch) > remaining {
				patch = patch[:remaining]
				item.Truncated = true
			}
			used += len(patch)
			item.Files = append(item.Files, linkedCodeFile{Path: filePath, Status: status, Additions: file.Additions, Deletions: file.Deletions, Patch: patch})
		}
		output = append(output, item)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	for len(encoded) > maxCodeBytes {
		trimmed := false
		for prIndex := len(output) - 1; prIndex >= 0 && !trimmed; prIndex-- {
			for fileIndex := len(output[prIndex].Files) - 1; fileIndex >= 0; fileIndex-- {
				file := &output[prIndex].Files[fileIndex]
				if file.Patch == "" {
					continue
				}
				excess := len(encoded) - maxCodeBytes + 16
				if excess >= len(file.Patch) {
					file.Patch = ""
					file.Omitted = "diff evidence size limit reached"
				} else {
					file.Patch = trimCodeText(file.Patch, len(file.Patch)-excess)
				}
				output[prIndex].Truncated = true
				trimmed = true
				break
			}
		}
		if !trimmed {
			return nil, errors.New("pull request code evidence metadata exceeds tool limit")
		}
		encoded, err = json.Marshal(output)
		if err != nil {
			return nil, err
		}
	}
	return encoded, nil
}

func safeCodePath(filename string) bool {
	clean := path.Clean(strings.ReplaceAll(filename, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || len(clean) > 256 {
		return false
	}
	lower := strings.ToLower(clean)
	base := path.Base(lower)
	for _, segment := range strings.Split(lower, "/") {
		switch segment {
		case "vendor", "node_modules", "dist", "build", "coverage", "third_party", "generated":
			return false
		}
	}
	if strings.HasPrefix(base, ".env") || strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "id_rsa") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return false
	}
	return true
}

func codeFileStatus(status string) string {
	switch status {
	case "added", "removed", "modified", "renamed", "copied", "changed":
		return status
	default:
		return "other"
	}
}

func boundedCodeText(value string, limit int) string {
	return trimCodeText(value, limit)
}

func trimCodeText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func redactCodePatch(patch string) string {
	if patch == "" {
		return ""
	}
	lines := strings.Split(patch, "\n")
	inPrivateKey := false
	for index, line := range lines {
		if strings.Contains(line, "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY-----") {
			inPrivateKey = true
		}
		if inPrivateKey {
			lines[index] = "[REDACTED PRIVATE KEY MATERIAL]"
			if strings.Contains(line, "-----END ") && strings.Contains(line, "PRIVATE KEY-----") {
				inPrivateKey = false
			}
			continue
		}
		if secretAssignment.MatchString(line) {
			switch {
			case strings.HasPrefix(line, "+"):
				line = "+[REDACTED SENSITIVE ASSIGNMENT]"
			case strings.HasPrefix(line, "-"):
				line = "-[REDACTED SENSITIVE ASSIGNMENT]"
			case strings.HasPrefix(line, " "):
				line = " [REDACTED SENSITIVE ASSIGNMENT]"
			default:
				line = "[REDACTED SENSITIVE ASSIGNMENT]"
			}
		}
		line = githubToken.ReplaceAllString(line, "[REDACTED TOKEN]")
		line = awsAccessKey.ReplaceAllString(line, "[REDACTED AWS KEY]")
		line = googleAPIKey.ReplaceAllString(line, "[REDACTED API KEY]")
		lines[index] = line
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
