package main

import (
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/adil/bug-issue-tracker/pkg/graph"
	"github.com/adil/bug-issue-tracker/pkg/lru"
	"github.com/adil/bug-issue-tracker/pkg/trie"
	"github.com/nats-io/nats.go"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type issueHandler struct {
	db          *sql.DB
	projectURL  string
	client      *http.Client
	events      *nats.Conn
	memberships *lru.Cache[string, membershipEntry]
}
type membershipEntry struct {
	allowed bool
	expires time.Time
}
type issue struct {
	ID          uint64     `json:"id"`
	ProjectID   uint64     `json:"project_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Priority    string     `json:"priority"`
	Status      string     `json:"status"`
	Position    uint64     `json:"position"`
	StartDate   dateValue  `json:"start_date"`
	DueDate     dateValue  `json:"due_date"`
	AssignedAt  *time.Time `json:"assigned_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	AssigneeID  *uint64    `json:"assignee_id"`
	ReporterID  uint64     `json:"reporter_id"`
}
type issueInput struct {
	ProjectID   uint64    `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    string    `json:"priority"`
	Status      string    `json:"status"`
	AssigneeID  *uint64   `json:"assignee_id"`
	StartDate   dateValue `json:"start_date"`
	DueDate     dateValue `json:"due_date"`
}

type dateValue struct {
	Time  time.Time
	Valid bool
}

func (d *dateValue) Scan(value any) error {
	if value == nil {
		d.Time, d.Valid = time.Time{}, false
		return nil
	}
	switch v := value.(type) {
	case time.Time:
		d.Time, d.Valid = v, true
	case []byte:
		return d.parse(string(v))
	case string:
		return d.parse(v)
	default:
		return fmt.Errorf("cannot scan %T as date", value)
	}
	return nil
}

func (d *dateValue) parse(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return err
	}
	d.Time, d.Valid = parsed, true
	return nil
}

func (d dateValue) Value() (driver.Value, error) {
	if !d.Valid {
		return nil, nil
	}
	return d.Time.Format("2006-01-02"), nil
}

func (d dateValue) MarshalJSON() ([]byte, error) {
	if !d.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(d.Time.Format("2006-01-02"))
}

func (d *dateValue) UnmarshalJSON(value []byte) error {
	if string(value) == "null" {
		d.Time, d.Valid = time.Time{}, false
		return nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return err
	}
	return d.parse(text)
}

func (h *issueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || uid == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/projects/") {
		h.projectIssueMove(w, r, uid)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/issues"), "/")
	if path == "labels/autocomplete" {
		h.labelAutocomplete(w, r, uid)
		return
	}
	if path == "work-order" {
		h.workOrder(w, r, uid)
		return
	}
	if path == "calendar" {
		h.calendar(w, r, uid)
		return
	}
	if path == "analytics" {
		h.analytics(w, r, uid)
		return
	}
	if path == "" {
		switch r.Method {
		case http.MethodPost:
			h.create(w, r, uid)
		case http.MethodGet:
			h.list(w, r, uid)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	id, err := strconv.ParseUint(strings.Split(path, "/")[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var projectID uint64
	if err := h.db.QueryRowContext(r.Context(), "SELECT project_id FROM issues WHERE id=?", id).Scan(&projectID); err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.Contains(path, "/") {
		if strings.HasSuffix(path, "/pull-requests") || strings.Contains(path, "/pull-requests/") {
			h.pullRequests(w, r, id, projectID, uid)
			return
		}
		if strings.HasSuffix(path, "/dependencies") {
			h.dependencies(w, r, id, projectID)
			return
		}
		h.labels(w, r, id)
		return
	}
	h.item(w, r, id, projectID, uid)
}

const maxIssuePullRequests = 5

type issuePullRequest struct {
	RepositoryID uint64 `json:"repository_id"`
	Number       uint64 `json:"number"`
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	HTMLURL      string `json:"html_url"`
}

func (h *issueHandler) pullRequests(w http.ResponseWriter, r *http.Request, issueID, projectID, uid uint64) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/issues/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "pull-requests" || len(parts) > 3 {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		number, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil || number == 0 || number > math.MaxInt64 {
			http.Error(w, "invalid pull request number", http.StatusBadRequest)
			return
		}
		if !h.projectAdmin(r, projectID, uid) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		repositoryID, err := strconv.ParseUint(r.URL.Query().Get("repository_id"), 10, 64)
		if err != nil || repositoryID == 0 {
			http.Error(w, "repository_id is required", http.StatusBadRequest)
			return
		}
		result, err := h.db.ExecContext(r.Context(), "DELETE FROM issue_pull_requests WHERE issue_id=? AND repository_id=? AND number=?", issueID, repositoryID, number)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		count, err := result.RowsAffected()
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if count == 0 {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.QueryContext(r.Context(), "SELECT repository_id,number,owner,repository_name FROM issue_pull_requests WHERE issue_id=? ORDER BY repository_id,number LIMIT ?", issueID, maxIssuePullRequests)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		links := make([]issuePullRequest, 0)
		for rows.Next() {
			var link issuePullRequest
			if err := rows.Scan(&link.RepositoryID, &link.Number, &link.Owner, &link.Repo); err != nil {
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}
			link.HTMLURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", link.Owner, link.Repo, link.Number)
			links = append(links, link)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		writeIssue(w, http.StatusOK, links)
	case http.MethodPost:
		if !h.projectAdmin(r, projectID, uid) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var input struct {
			Number uint64 `json:"number"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || input.Number == 0 || input.Number > math.MaxInt64 {
			http.Error(w, "invalid pull request number", http.StatusBadRequest)
			return
		}
		repo, ok := h.connectedRepository(r, w, projectID, uid)
		if !ok {
			return
		}
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		var lockedIssue uint64
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM issues WHERE id=? FOR UPDATE", issueID).Scan(&lockedIssue); err != nil {
			http.NotFound(w, r)
			return
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM issue_pull_requests WHERE issue_id=?", issueID).Scan(&count); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if count >= maxIssuePullRequests {
			http.Error(w, "issue pull request limit reached", http.StatusConflict)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO issue_pull_requests(issue_id,repository_id,number,owner,repository_name) VALUES (?,?,?,?,?)", issueID, repo.ID, input.Number, repo.Owner, repo.Name); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				http.Error(w, "pull request already linked", http.StatusConflict)
				return
			}
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		writeIssue(w, http.StatusCreated, makeIssuePullRequest(repo, input.Number))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type connectedRepository struct {
	ID    uint64 `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func makeIssuePullRequest(repo connectedRepository, number uint64) issuePullRequest {
	return issuePullRequest{RepositoryID: repo.ID, Number: number, Owner: repo.Owner, Repo: repo.Name, HTMLURL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", repo.Owner, repo.Name, number)}
}

func (h *issueHandler) projectCall(r *http.Request, method, path string, uid uint64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, strings.TrimRight(h.projectURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-ID", strconv.FormatUint(uid, 10))
	if role := r.Header.Get("X-User-Role"); role != "" {
		req.Header.Set("X-User-Role", role)
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	return h.client.Do(req)
}

func (h *issueHandler) projectAdmin(r *http.Request, projectID, uid uint64) bool {
	resp, err := h.projectCall(r, http.MethodGet, fmt.Sprintf("/projects/%d/members/check?require_admin=true", projectID), uid)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Admin bool `json:"admin"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) == nil && result.Admin
}

func (h *issueHandler) connectedRepository(r *http.Request, w http.ResponseWriter, projectID, uid uint64) (connectedRepository, bool) {
	resp, err := h.projectCall(r, http.MethodGet, fmt.Sprintf("/projects/%d/repository", projectID), uid)
	if err != nil {
		http.Error(w, "project service unavailable", http.StatusBadGateway)
		return connectedRepository{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "project repository unavailable", http.StatusBadGateway)
		return connectedRepository{}, false
	}
	var repo *connectedRepository
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&repo); err != nil {
		http.Error(w, "invalid project repository response", http.StatusBadGateway)
		return connectedRepository{}, false
	}
	if repo == nil || repo.ID == 0 || repo.Owner == "" || repo.Name == "" {
		http.Error(w, "project has no connected repository", http.StatusConflict)
		return connectedRepository{}, false
	}
	return *repo, true
}

func (h *issueHandler) labelAutocomplete(w http.ResponseWriter, r *http.Request, uid uint64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID, err := strconv.ParseUint(r.URL.Query().Get("project_id"), 10, 64)
	if err != nil || projectID == 0 {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	prefix := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := h.db.QueryContext(r.Context(), "SELECT name FROM labels WHERE project_id=? AND name LIKE ? ORDER BY name LIMIT 100", projectID, strings.ReplaceAll(strings.ReplaceAll(prefix, "\\", "\\\\"), "%", "\\%")+"%")
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	index := trie.New()
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		index.Insert(label)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeIssue(w, http.StatusOK, index.Search(prefix, 10))
}

func (h *issueHandler) dependencies(w http.ResponseWriter, r *http.Request, issueID, projectID uint64) {
	switch r.Method {
	case http.MethodGet:
		rows, err := h.db.QueryContext(r.Context(), "SELECT depends_on_issue_id FROM issue_dependencies WHERE issue_id=? ORDER BY depends_on_issue_id", issueID)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer rows.Close()
		ids := []uint64{}
		for rows.Next() {
			var id uint64
			if rows.Scan(&id) != nil {
				http.Error(w, "database error", 500)
				return
			}
			ids = append(ids, id)
		}
		writeIssue(w, 200, ids)
	case http.MethodPost:
		var input struct {
			DependsOn uint64 `json:"depends_on_issue_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || input.DependsOn == 0 || input.DependsOn == issueID {
			http.Error(w, "invalid dependency", 400)
			return
		}
		var otherProject uint64
		if err := h.db.QueryRowContext(r.Context(), "SELECT project_id FROM issues WHERE id=?", input.DependsOn).Scan(&otherProject); err != nil || otherProject != projectID {
			http.Error(w, "dependency issue must exist in the same project", 400)
			return
		}
		rows, err := h.db.QueryContext(r.Context(), "SELECT i.id,d.depends_on_issue_id FROM issues i LEFT JOIN issue_dependencies d ON d.issue_id=i.id WHERE i.project_id=?", projectID)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		g := graph.New()
		for rows.Next() {
			var item uint64
			var dependency sql.NullInt64
			if rows.Scan(&item, &dependency) != nil {
				rows.Close()
				http.Error(w, "database error", 500)
				return
			}
			if dependency.Valid {
				if err := g.AddDependency(strconv.FormatUint(item, 10), strconv.FormatInt(dependency.Int64, 10)); err != nil {
					rows.Close()
					http.Error(w, "dependency graph error", 500)
					return
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			http.Error(w, "database error", 500)
			return
		}
		rows.Close()
		if err := g.AddDependency(strconv.FormatUint(issueID, 10), strconv.FormatUint(input.DependsOn, 10)); err != nil {
			http.Error(w, "dependency would create a cycle", http.StatusConflict)
			return
		}
		_, err = h.db.ExecContext(r.Context(), "INSERT INTO issue_dependencies(issue_id,depends_on_issue_id) VALUES (?,?)", issueID, input.DependsOn)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		writeIssue(w, 201, map[string]any{"issue_id": issueID, "depends_on_issue_id": input.DependsOn})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *issueHandler) workOrder(w http.ResponseWriter, r *http.Request, uid uint64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID, err := strconv.ParseUint(r.URL.Query().Get("project_id"), 10, 64)
	if err != nil || projectID == 0 {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT i.id,d.depends_on_issue_id FROM issues i LEFT JOIN issue_dependencies d ON d.issue_id=i.id WHERE i.project_id=? ORDER BY i.id", projectID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	g := graph.New()
	for rows.Next() {
		var issueID uint64
		var dependency sql.NullInt64
		if err := rows.Scan(&issueID, &dependency); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		id := strconv.FormatUint(issueID, 10)
		g.AddNode(id)
		if dependency.Valid {
			if err := g.AddDependency(id, strconv.FormatInt(dependency.Int64, 10)); err != nil {
				http.Error(w, "dependency graph error", http.StatusInternalServerError)
				return
			}
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	order, err := g.TopologicalOrder()
	if err != nil {
		http.Error(w, "dependency graph error", http.StatusInternalServerError)
		return
	}
	ids := make([]uint64, 0, len(order))
	for _, id := range order {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			http.Error(w, "dependency graph error", http.StatusInternalServerError)
			return
		}
		ids = append(ids, n)
	}
	writeIssue(w, http.StatusOK, map[string]any{"project_id": projectID, "work_order": ids})
}

func (h *issueHandler) isMember(r *http.Request, projectID, userID uint64) bool {
	role := r.Header.Get("X-User-Role")
	key := fmt.Sprintf("%d:%d:%s", projectID, userID, role)
	if h.memberships != nil {
		if cached, ok := h.memberships.Get(key); ok && cached.expires.After(time.Now()) {
			return cached.allowed
		}
	}
	u := fmt.Sprintf("%s/projects/%d/members/check", strings.TrimRight(h.projectURL, "/"), projectID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-User-ID", strconv.FormatUint(userID, 10))
	if role != "" {
		req.Header.Set("X-User-Role", role)
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Member bool `json:"member"`
	}
	isMember := json.NewDecoder(resp.Body).Decode(&result) == nil && result.Member
	if h.memberships != nil {
		h.memberships.Put(key, membershipEntry{allowed: isMember, expires: time.Now().Add(30 * time.Second)})
	}
	return isMember
}

func (h *issueHandler) isAssigneeMember(r *http.Request, projectID, assigneeID uint64) bool {
	// Check the target directly: a cached positive result could keep a removed
	// project member assignable until the membership cache expires.
	actorID := r.Header.Get("X-User-ID")
	u := fmt.Sprintf("%s/projects/%d/members/check?user_id=%d", strings.TrimRight(h.projectURL, "/"), projectID, assigneeID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-User-ID", actorID)
	if role := r.Header.Get("X-User-Role"); role != "" {
		req.Header.Set("X-User-Role", role)
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Member bool `json:"member"`
	}
	return json.NewDecoder(resp.Body).Decode(&result) == nil && result.Member
}
func (h *issueHandler) create(w http.ResponseWriter, r *http.Request, uid uint64) {
	var in issueInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.ProjectID == 0 || strings.TrimSpace(in.Title) == "" || len(in.Title) > 240 {
		http.Error(w, "invalid issue", 400)
		return
	}
	if !h.isMember(r, in.ProjectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	if in.Status == "" {
		in.Status = "todo"
	}
	if !oneOf(in.Priority, "low", "medium", "high", "critical") || !oneOf(in.Status, "todo", "in_progress", "done") {
		http.Error(w, "invalid priority or status", 400)
		return
	}
	if !validIssueSchedule(in.StartDate, in.DueDate) {
		http.Error(w, "due_date must be on or after start_date", http.StatusBadRequest)
		return
	}
	if in.AssigneeID != nil && !h.isAssigneeMember(r, in.ProjectID, *in.AssigneeID) {
		http.Error(w, "assignee must be a project member", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	var assignedAt, startedAt, completedAt any
	if in.AssigneeID != nil {
		assignedAt = now
	}
	if in.Status == "in_progress" {
		startedAt = now
	}
	if in.Status == "done" {
		completedAt = now
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), "INSERT INTO issues (project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at) VALUES (?,?,?,?,?,?,?,0,?,?,?,?,?)", in.ProjectID, strings.TrimSpace(in.Title), in.Description, in.Priority, in.Status, in.AssigneeID, uid, in.StartDate, in.DueDate, assignedAt, startedAt, completedAt)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// IDs are monotonically increasing, so this gives newly-created issues a
	// stable append position without racing concurrent creates.
	if _, err := tx.ExecContext(r.Context(), "UPDATE issues SET position=? WHERE id=? AND project_id=?", id, id, in.ProjectID); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	h.publish(r, "IssueCreated", uint64(id), in.ProjectID, uid, in.Status)
	writeIssue(w, 201, issue{ID: uint64(id), ProjectID: in.ProjectID, Title: strings.TrimSpace(in.Title), Description: in.Description, Priority: in.Priority, Status: in.Status, Position: uint64(id), StartDate: in.StartDate, DueDate: in.DueDate, AssignedAt: timePointer(assignedAt), StartedAt: timePointer(startedAt), CompletedAt: timePointer(completedAt), AssigneeID: in.AssigneeID, ReporterID: uid})
}

func validIssueSchedule(startDate, dueDate dateValue) bool {
	return !startDate.Valid || !dueDate.Valid || !dueDate.Time.Before(startDate.Time)
}

func timePointer(value any) *time.Time {
	if value == nil {
		return nil
	}
	if timestamp, ok := value.(time.Time); ok {
		return &timestamp
	}
	return nil
}

func (h *issueHandler) projectIssueMove(w http.ResponseWriter, r *http.Request, uid uint64) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "projects" || parts[2] != "issues" || parts[4] != "move" {
		http.NotFound(w, r)
		return
	}
	projectID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || projectID == 0 {
		http.NotFound(w, r)
		return
	}
	issueID, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || issueID == 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Status   string `json:"status"`
		Position *int64 `json:"position"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !oneOf(input.Status, "todo", "in_progress", "done") || input.Position == nil || *input.Position < 0 {
		http.Error(w, "invalid status or position", http.StatusBadRequest)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Every move locks the complete project issue set in the same order so
	// competing moves for this project serialize before reading positions.
	rows, err := tx.QueryContext(r.Context(), "SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE project_id=? ORDER BY status,position,id FOR UPDATE", projectID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	projectIssues := make([]issue, 0)
	for rows.Next() {
		var current issue
		if err := rows.Scan(&current.ID, &current.ProjectID, &current.Title, &current.Description, &current.Priority, &current.Status, &current.AssigneeID, &current.ReporterID, &current.Position, &current.StartDate, &current.DueDate, &current.AssignedAt, &current.StartedAt, &current.CompletedAt); err != nil {
			rows.Close()
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		projectIssues = append(projectIssues, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	movingIndex := -1
	for index := range projectIssues {
		if projectIssues[index].ID == issueID {
			movingIndex = index
			break
		}
	}
	if movingIndex < 0 {
		http.NotFound(w, r)
		return
	}
	moving := projectIssues[movingIndex]
	oldStatus := moving.Status
	if input.Status == "in_progress" && oldStatus != "in_progress" && moving.StartedAt == nil {
		now := time.Now().UTC()
		moving.StartedAt = &now
	}
	if input.Status == "done" && oldStatus != "done" {
		now := time.Now().UTC()
		moving.CompletedAt = &now
	} else if input.Status != "done" {
		moving.CompletedAt = nil
	}
	columns := map[string][]issue{"todo": {}, "in_progress": {}, "done": {}}
	for _, current := range projectIssues {
		if current.ID != issueID {
			columns[current.Status] = append(columns[current.Status], current)
		}
	}
	position := *input.Position
	if position > int64(len(columns[input.Status])) {
		position = int64(len(columns[input.Status]))
	}
	destination := columns[input.Status]
	destination = append(destination, issue{})
	copy(destination[int(position)+1:], destination[int(position):])
	moving.Status = input.Status
	moving.Position = uint64(position)
	destination[int(position)] = moving
	columns[input.Status] = destination

	for _, status := range []string{"todo", "in_progress", "done"} {
		items := columns[status]
		for index := range items {
			items[index].Position = uint64(index)
			if _, err := tx.ExecContext(r.Context(), "UPDATE issues SET status=?,position=?,started_at=?,completed_at=? WHERE id=? AND project_id=?", status, items[index].Position, items[index].StartedAt, items[index].CompletedAt, items[index].ID, projectID); err != nil {
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}
			if items[index].ID == issueID {
				moving = items[index]
			}
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if oldStatus != input.Status {
		h.publish(r, "StatusChanged", issueID, projectID, uid, input.Status)
	}
	writeIssue(w, http.StatusOK, moving)
}

func (h *issueHandler) calendar(w http.ResponseWriter, r *http.Request, uid uint64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID, err := strconv.ParseUint(r.URL.Query().Get("project_id"), 10, 64)
	if err != nil || projectID == 0 {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	from, err := parseCalendarDate(r.URL.Query().Get("from"))
	if err != nil {
		http.Error(w, "from must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	to, err := parseCalendarDate(r.URL.Query().Get("to"))
	if err != nil || to.Before(from) || to.Sub(from) > 93*24*time.Hour {
		http.Error(w, "invalid calendar range", http.StatusBadRequest)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE project_id=? AND (((start_date IS NOT NULL AND due_date IS NOT NULL AND start_date<=? AND due_date>=?) OR (start_date IS NOT NULL AND due_date IS NULL AND start_date BETWEEN ? AND ?) OR (start_date IS NULL AND due_date BETWEEN ? AND ?)) OR DATE(assigned_at) BETWEEN ? AND ? OR DATE(started_at) BETWEEN ? AND ? OR DATE(completed_at) BETWEEN ? AND ?) ORDER BY COALESCE(start_date,due_date),position,id LIMIT 500", projectID, to.Format("2006-01-02"), from.Format("2006-01-02"), from.Format("2006-01-02"), to.Format("2006-01-02"), from.Format("2006-01-02"), to.Format("2006-01-02"), from.Format("2006-01-02"), to.Format("2006-01-02"), from.Format("2006-01-02"), to.Format("2006-01-02"), from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []issue{}
	for rows.Next() {
		var item issue
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Title, &item.Description, &item.Priority, &item.Status, &item.AssigneeID, &item.ReporterID, &item.Position, &item.StartDate, &item.DueDate, &item.AssignedAt, &item.StartedAt, &item.CompletedAt); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeIssue(w, http.StatusOK, items)
}

func parseCalendarDate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", value)
}

type projectAnalytics struct {
	ProjectID  uint64            `json:"project_id"`
	Total      uint64            `json:"total"`
	ByStatus   map[string]uint64 `json:"by_status"`
	ByPriority map[string]uint64 `json:"by_priority"`
	OpenHigh   uint64            `json:"open_high_priority"`
}

func (h *issueHandler) analytics(w http.ResponseWriter, r *http.Request, uid uint64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID, err := strconv.ParseUint(r.URL.Query().Get("project_id"), 10, 64)
	if err != nil || projectID == 0 {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	out := projectAnalytics{
		ProjectID:  projectID,
		ByStatus:   map[string]uint64{"todo": 0, "in_progress": 0, "done": 0},
		ByPriority: map[string]uint64{"critical": 0, "high": 0, "medium": 0, "low": 0},
	}
	var todo, inProgress, done, critical, high, medium, low, openHigh uint64
	err = h.db.QueryRowContext(r.Context(), "SELECT COUNT(*), COALESCE(SUM(status='todo'),0), COALESCE(SUM(status='in_progress'),0), COALESCE(SUM(status='done'),0), COALESCE(SUM(priority='critical'),0), COALESCE(SUM(priority='high'),0), COALESCE(SUM(priority='medium'),0), COALESCE(SUM(priority='low'),0), COALESCE(SUM(status<>'done' AND priority IN ('high','critical')),0) FROM issues WHERE project_id=?", projectID).Scan(&out.Total, &todo, &inProgress, &done, &critical, &high, &medium, &low, &openHigh)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	out.ByStatus["todo"], out.ByStatus["in_progress"], out.ByStatus["done"] = todo, inProgress, done
	out.ByPriority["critical"], out.ByPriority["high"], out.ByPriority["medium"], out.ByPriority["low"] = critical, high, medium, low
	out.OpenHigh = openHigh
	writeIssue(w, http.StatusOK, out)
}

func (h *issueHandler) list(w http.ResponseWriter, r *http.Request, uid uint64) {
	query, err := buildIssueQuery(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	projectID, _ := strconv.ParseUint(r.URL.Query().Get("project_id"), 10, 64)
	if !h.isMember(r, projectID, uid) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), query.SQL, query.Args...)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	items := []issue{}
	for rows.Next() {
		var x issue
		if err := rows.Scan(&x.ID, &x.ProjectID, &x.Title, &x.Description, &x.Priority, &x.Status, &x.AssigneeID, &x.ReporterID, &x.Position, &x.StartDate, &x.DueDate, &x.AssignedAt, &x.StartedAt, &x.CompletedAt); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		items = append(items, x)
	}
	if rows.Err() != nil {
		http.Error(w, "database error", 500)
		return
	}
	writeIssue(w, 200, items)
}
func (h *issueHandler) item(w http.ResponseWriter, r *http.Request, id, projectID, actor uint64) {
	switch r.Method {
	case http.MethodGet:
		var x issue
		err := h.db.QueryRowContext(r.Context(), "SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE id=?", id).Scan(&x.ID, &x.ProjectID, &x.Title, &x.Description, &x.Priority, &x.Status, &x.AssigneeID, &x.ReporterID, &x.Position, &x.StartDate, &x.DueDate, &x.AssignedAt, &x.StartedAt, &x.CompletedAt)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		writeIssue(w, 200, x)
	case http.MethodPut, http.MethodPatch:
		var in issueInput
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || strings.TrimSpace(in.Title) == "" {
			http.Error(w, "invalid issue", 400)
			return
		}
		if in.Priority == "" {
			in.Priority = "medium"
		}
		if in.Status == "" {
			in.Status = "todo"
		}
		if !oneOf(in.Priority, "low", "medium", "high", "critical") || !oneOf(in.Status, "todo", "in_progress", "done") {
			http.Error(w, "invalid priority or status", 400)
			return
		}
		if !validIssueSchedule(in.StartDate, in.DueDate) {
			http.Error(w, "due_date must be on or after start_date", http.StatusBadRequest)
			return
		}
		var projectID uint64
		if err := h.db.QueryRowContext(r.Context(), "SELECT project_id FROM issues WHERE id=?", id).Scan(&projectID); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if in.AssigneeID != nil && !h.isAssigneeMember(r, projectID, *in.AssigneeID) {
			http.Error(w, "assignee must be a project member", http.StatusBadRequest)
			return
		}
		actorID, _ := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(r.Context(), "SELECT id,status,position,assignee_id,assigned_at,started_at,completed_at FROM issues WHERE project_id=? ORDER BY status,position,id FOR UPDATE", projectID)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		var previousStatus string
		var position uint64
		var currentAssignee *uint64
		var assignedAt, startedAt, completedAt sql.NullTime
		var found bool
		var destinationMax uint64
		var destinationFound bool
		for rows.Next() {
			var issueID, currentPosition uint64
			var status string
			var assigneeID *uint64
			var assigned, started, completed sql.NullTime
			if err := rows.Scan(&issueID, &status, &currentPosition, &assigneeID, &assigned, &started, &completed); err != nil {
				rows.Close()
				http.Error(w, "database error", http.StatusInternalServerError)
				return
			}
			if issueID == id {
				found, previousStatus, position = true, status, currentPosition
				currentAssignee, assignedAt, startedAt, completedAt = assigneeID, assigned, started, completed
			}
			if status == in.Status && issueID != id && (!destinationFound || currentPosition > destinationMax) {
				destinationFound, destinationMax = true, currentPosition
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if err := rows.Close(); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if previousStatus != in.Status {
			position = 0
			if destinationFound {
				position = destinationMax + 1
			}
		}
		now := time.Now().UTC()
		var assignedArg, startedArg, completedArg any
		if in.AssigneeID != nil {
			if currentAssignee == nil || *currentAssignee != *in.AssigneeID {
				assignedArg = now
			} else if assignedAt.Valid {
				assignedArg = assignedAt.Time
			}
		}
		if startedAt.Valid {
			startedArg = startedAt.Time
		}
		if in.Status == "in_progress" && !startedAt.Valid {
			startedArg = now
		}
		if in.Status == "done" {
			if completedAt.Valid {
				completedArg = completedAt.Time
			} else {
				completedArg = now
			}
		}
		_, err = tx.ExecContext(r.Context(), "UPDATE issues SET title=?,description=?,priority=?,status=?,assignee_id=?,position=?,start_date=?,due_date=?,assigned_at=?,started_at=?,completed_at=? WHERE id=? AND project_id=?", strings.TrimSpace(in.Title), in.Description, in.Priority, in.Status, in.AssigneeID, position, in.StartDate, in.DueDate, assignedArg, startedArg, completedArg, id, projectID)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if previousStatus != in.Status {
			h.publish(r, "StatusChanged", id, projectID, actorID, in.Status)
		}
		writeIssue(w, 200, map[string]bool{"updated": true})
	case http.MethodDelete:
		role := r.Header.Get("X-User-Role")
		result, err := h.db.ExecContext(r.Context(), "DELETE FROM issues WHERE id=? AND project_id=? AND (reporter_id=? OR assignee_id=? OR ?='admin')", id, projectID, actor, actor, role)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if affected, err := result.RowsAffected(); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		} else if affected == 0 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *issueHandler) publish(r *http.Request, eventType string, issueID, projectID, actorID uint64, status string) {
	if h.events == nil {
		return
	}
	var eventID [16]byte
	if _, err := rand.Read(eventID[:]); err != nil {
		slog.Warn("generate event ID failed", "error", err)
		return
	}
	body, _ := json.Marshal(map[string]any{"event_id": hex.EncodeToString(eventID[:]), "request_id": r.Header.Get("X-Request-ID"), "issue_id": issueID, "project_id": projectID, "actor_id": actorID, "event_type": eventType, "status": status})
	if err := h.events.Publish("tracker.issue.events", body); err != nil {
		slog.Warn("publish issue event failed", "error", err)
	}
}
func (h *issueHandler) labels(w http.ResponseWriter, r *http.Request, id uint64) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[2] != "labels" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := h.db.QueryContext(r.Context(), "SELECT l.id,l.name FROM labels l JOIN issue_labels il ON il.label_id=l.id WHERE il.issue_id=? ORDER BY l.name", id)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var labelID uint64
			var name string
			if rows.Scan(&labelID, &name) != nil {
				http.Error(w, "database error", 500)
				return
			}
			out = append(out, map[string]any{"id": labelID, "name": name})
		}
		writeIssue(w, 200, out)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" || len(input.Name) > 64 {
		http.Error(w, "invalid label", 400)
		return
	}
	var projectID uint64
	_ = h.db.QueryRowContext(r.Context(), "SELECT project_id FROM issues WHERE id=?", id).Scan(&projectID)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), "INSERT INTO labels (project_id,name) VALUES (?,?) ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)", projectID, strings.TrimSpace(input.Name))
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	var labelID uint64
	_ = tx.QueryRowContext(r.Context(), "SELECT id FROM labels WHERE project_id=? AND name=?", projectID, strings.TrimSpace(input.Name)).Scan(&labelID)
	_, err = tx.ExecContext(r.Context(), "INSERT IGNORE INTO issue_labels(issue_id,label_id) VALUES (?,?)", id, labelID)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	if tx.Commit() != nil {
		http.Error(w, "database error", 500)
		return
	}
	writeIssue(w, 201, map[string]any{"id": labelID, "name": input.Name})
}
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
func writeIssue(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
