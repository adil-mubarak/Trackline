package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/adil/bug-issue-tracker/pkg/lru"
)

func pullRequestProjectServer(admin, repository bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/4/members/check":
			if r.URL.Query().Get("require_admin") == "true" {
				_, _ = w.Write([]byte(`{"member":` + map[bool]string{true: "true", false: "false"}[admin] + `,"admin":` + map[bool]string{true: "true", false: "false"}[admin] + `}`))
			} else {
				_, _ = w.Write([]byte(`{"member":true}`))
			}
		case "/projects/4/repository":
			if !repository {
				_, _ = w.Write([]byte(`null`))
				return
			}
			_, _ = w.Write([]byte(`{"id":987,"owner":"acme","name":"bugs"}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestIssuePullRequestLinksCRUDAndRepositoryOwnership(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := pullRequestProjectServer(true, true)
	defer server.Close()
	h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM issues WHERE id=.*FOR UPDATE").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(30))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM issue_pull_requests WHERE issue_id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("INSERT INTO issue_pull_requests").WithArgs(uint64(30), uint64(987), uint64(42), "acme", "bugs").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w := sendIssueRequest(h, http.MethodPost, "/issues/30/pull-requests", `{"number":42,"repository_id":123,"url":"https://evil.invalid"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"repository_id":987`) || !strings.Contains(w.Body.String(), `"html_url":"https://github.com/acme/bugs/pull/42"`) {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectQuery("SELECT repository_id,number,owner,repository_name FROM issue_pull_requests WHERE issue_id=.*ORDER BY repository_id,number LIMIT").WithArgs(uint64(30), 5).WillReturnRows(sqlmock.NewRows([]string{"repository_id", "number", "owner", "repository_name"}).AddRow(987, 42, "acme", "bugs"))
	w = sendIssueRequest(h, http.MethodGet, "/issues/30/pull-requests", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"number":42`) {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectExec("DELETE FROM issue_pull_requests WHERE issue_id=.*repository_id=.*number=").WithArgs(uint64(30), uint64(987), uint64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	w = sendIssueRequest(h, http.MethodDelete, "/issues/30/pull-requests/42?repository_id=987", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssuePullRequestLinkRequiresAdminAndConnectedRepository(t *testing.T) {
	for _, test := range []struct {
		name              string
		admin, repository bool
		want              int
	}{
		{name: "member forbidden", admin: false, repository: true, want: http.StatusForbidden},
		{name: "no repository", admin: true, repository: false, want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server := pullRequestProjectServer(test.admin, test.repository)
			defer server.Close()
			h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}
			mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
			w := sendIssueRequest(h, http.MethodPost, "/issues/30/pull-requests", `{"number":42}`)
			if w.Code != test.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueHistoricalPullRequestCanBeUnlinkedAfterRepositoryDisconnect(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := pullRequestProjectServer(true, false)
	defer server.Close()
	h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}
	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectExec("DELETE FROM issue_pull_requests WHERE issue_id=.*repository_id=.*number=").WithArgs(uint64(30), uint64(987), uint64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	w := sendIssueRequest(h, http.MethodDelete, "/issues/30/pull-requests/42?repository_id=987", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete historical link status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssuePullRequestNumberMustFitPositiveInt64(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := pullRequestProjectServer(true, true)
	defer server.Close()
	h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}
	for _, body := range []string{`{"number":0}`, `{"number":9223372036854775808}`} {
		mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
		w := sendIssueRequest(h, http.MethodPost, "/issues/30/pull-requests", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d", body, w.Code)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssuePullRequestDuplicateReturnsConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := pullRequestProjectServer(true, true)
	defer server.Close()
	h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}
	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM issues WHERE id=.*FOR UPDATE").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(30))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM issue_pull_requests WHERE issue_id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec("INSERT INTO issue_pull_requests").WithArgs(uint64(30), uint64(987), uint64(42), "acme", "bugs").WillReturnError(errors.New("Duplicate entry '30-987-42' for key 'PRIMARY'"))
	mock.ExpectRollback()
	w := sendIssueRequest(h, http.MethodPost, "/issues/30/pull-requests", `{"number":42}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func membershipServer(member bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/4/members/check" && r.URL.Path != "/projects/9/members/check" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if member {
			_, _ = w.Write([]byte(`{"member":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"member":false}`))
	}))
}

func newIssueTestHandler(t *testing.T, db *sql.DB, member bool) (*issueHandler, func()) {
	t.Helper()
	server := membershipServer(member)
	return &issueHandler{db: db, projectURL: server.URL, client: server.Client()}, server.Close
}

func sendIssueRequest(h *issueHandler, method, path, body string) *httptest.ResponseRecorder {
	return sendIssueRequestToRole(h, method, path, body, "member")
}

func sendIssueRequestTo(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return sendIssueRequestToRole(handler, method, path, body, "member")
}

func sendIssueRequestToRole(handler http.Handler, method, path, body, role string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-User-ID", "12")
	r.Header.Set("X-User-Role", role)
	r.Header.Set("X-Request-ID", "test-request-id")
	handler.ServeHTTP(w, r)
	return w
}

func TestIssueCreateAndMembershipAuthorization(t *testing.T) {
	t.Run("member can create", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		h, closeServer := newIssueTestHandler(t, db, true)
		defer closeServer()
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO issues").WithArgs(uint64(4), "Title", "Description", "medium", "todo", nil, uint64(12), nil, nil, nil, nil, nil).WillReturnResult(sqlmock.NewResult(30, 1))
		mock.ExpectExec("UPDATE issues SET position=").WithArgs(int64(30), int64(30), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		w := sendIssueRequest(h, http.MethodPost, "/issues", `{"project_id":4,"title":"Title","description":"Description"}`)
		if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"id":30`) {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nonmember is forbidden", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		h, closeServer := newIssueTestHandler(t, db, false)
		defer closeServer()
		w := sendIssueRequest(h, http.MethodPost, "/issues", `{"project_id":4,"title":"Title"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("invalid input is rejected", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		h, closeServer := newIssueTestHandler(t, db, true)
		defer closeServer()
		for _, body := range []string{`{}`, `{"project_id":4,"title":" "}`, `{"project_id":4,"title":"Title","priority":"urgent"}`} {
			w := sendIssueRequest(h, http.MethodPost, "/issues", body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("body=%s status=%d", body, w.Code)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMembershipCallsForwardGlobalRoleAndIsolateCache(t *testing.T) {
	var callerRoles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callerRoles = append(callerRoles, r.Header.Get("X-User-Role"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"member":` + map[bool]string{true: "true", false: "false"}[r.Header.Get("X-User-Role") == "admin"] + `}`))
	}))
	defer server.Close()
	h := &issueHandler{projectURL: server.URL, client: server.Client(), memberships: lru.New[string, membershipEntry](4)}
	r := httptest.NewRequest(http.MethodGet, "/issues?project_id=9", nil)
	r.Header.Set("X-User-ID", "12")
	r.Header.Set("X-User-Role", "admin")
	if !h.isMember(r, 9, 12) {
		t.Fatal("platform admin should receive effective membership from Project")
	}
	r.Header.Set("X-User-Role", "member")
	if h.isMember(r, 9, 12) {
		t.Fatal("member request must not reuse platform admin's cached membership")
	}
	if len(callerRoles) != 2 || callerRoles[0] != "admin" || callerRoles[1] != "member" {
		t.Fatalf("unexpected role headers forwarded: %#v", callerRoles)
	}

	assigneeRequest := httptest.NewRequest(http.MethodGet, "/issues", nil)
	assigneeRequest.Header.Set("X-User-ID", "12")
	assigneeRequest.Header.Set("X-User-Role", "admin")
	if !h.isAssigneeMember(assigneeRequest, 9, 7) {
		t.Fatal("expected Project to confirm target membership")
	}
	if callerRoles[len(callerRoles)-1] != "admin" {
		t.Fatalf("assignee membership call did not forward global role: %#v", callerRoles)
	}
}

func TestIssueListValidatesCursorAndLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	for _, path := range []string{"/issues?project_id=4&after_id=abc", "/issues?project_id=4&limit=0", "/issues?project_id=4&limit=abc"} {
		w := sendIssueRequest(h, http.MethodGet, path, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("path=%s status=%d", path, w.Code)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueGetUpdateAndDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectQuery("SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "description", "priority", "status", "assignee_id", "reporter_id", "position", "start_date", "due_date", "assigned_at", "started_at", "completed_at"}).AddRow(30, 4, "A bug", "Details", "high", "todo", nil, 12, 0, nil, nil, nil, nil, nil))
	w := sendIssueRequest(h, http.MethodGet, "/issues/30", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"title":"A bug"`) {
		t.Fatalf("get status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,status,position,assignee_id,assigned_at,started_at,completed_at FROM issues WHERE project_id=.*FOR UPDATE").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "position", "assignee_id", "assigned_at", "started_at", "completed_at"}).AddRow(30, "todo", 0, nil, nil, nil, nil))
	mock.ExpectExec("UPDATE issues SET title=").WithArgs("Fixed", "Details", "high", "in_progress", nil, uint64(0), nil, nil, nil, sqlmock.AnyArg(), nil, uint64(30), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w = sendIssueRequest(h, http.MethodPatch, "/issues/30", `{"title":"Fixed","description":"Details","priority":"high","status":"in_progress"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(30)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectExec("DELETE FROM issues WHERE id=.*reporter_id=.*assignee_id=.*").WithArgs(uint64(30), uint64(4), uint64(12), uint64(12), "member").WillReturnResult(sqlmock.NewResult(0, 1))
	w = sendIssueRequest(h, http.MethodDelete, "/issues/30", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueDeleteRequiresReporterAssigneeOrGlobalAdmin(t *testing.T) {
	for _, test := range []struct {
		name       string
		role       string
		rows       int64
		wantStatus int
	}{
		{name: "issue creator", role: "member", rows: 1, wantStatus: http.StatusNoContent},
		{name: "assigned user", role: "member", rows: 1, wantStatus: http.StatusNoContent},
		{name: "unrelated project member", role: "member", rows: 0, wantStatus: http.StatusForbidden},
		{name: "global admin", role: "admin", rows: 1, wantStatus: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			h, closeServer := newIssueTestHandler(t, db, true)
			defer closeServer()
			mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(31)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
			mock.ExpectExec("DELETE FROM issues WHERE id=.*reporter_id=.*assignee_id=.*").WithArgs(uint64(31), uint64(4), uint64(12), uint64(12), test.role).WillReturnResult(sqlmock.NewResult(0, test.rows))
			w := sendIssueRequestToRole(h, http.MethodDelete, "/issues/31", "", test.role)
			if w.Code != test.wantStatus {
				t.Fatalf("delete status=%d body=%s want=%d", w.Code, w.Body.String(), test.wantStatus)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssueDependencyCycleRejected(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(1)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectQuery("SELECT project_id FROM issues WHERE id=").WithArgs(uint64(2)).WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(4))
	mock.ExpectQuery("SELECT i.id,d.depends_on_issue_id FROM issues").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"id", "depends_on_issue_id"}).AddRow(2, 1))
	w := sendIssueRequest(h, http.MethodPost, "/issues/1/dependencies", `{"depends_on_issue_id":2}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueWorkOrderEndpoint(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT i.id,d.depends_on_issue_id FROM issues i LEFT JOIN issue_dependencies").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"id", "depends_on_issue_id"}).AddRow(1, nil).AddRow(2, 1).AddRow(3, 2))
	w := sendIssueRequest(h, http.MethodGet, "/issues/work-order?project_id=4", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"work_order":[1,2,3]`) {
		t.Fatalf("work order status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLabelAutocompleteIsMembershipScoped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT name FROM labels WHERE project_id=").WithArgs(uint64(4), "ba%").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("Backend").AddRow("basecamp").AddRow("frontend"))
	w := sendIssueRequest(h, http.MethodGet, "/issues/labels/autocomplete?project_id=4&q=ba", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Backend") || !strings.Contains(w.Body.String(), "basecamp") || strings.Contains(w.Body.String(), "frontend") {
		t.Fatalf("autocomplete status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueRoutesRequireGatewayIdentity(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &issueHandler{db: db}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/issues?project_id=4", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestIssueMoveReordersWithinColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE project_id=.*FOR UPDATE").WithArgs(uint64(4)).WillReturnRows(issueMoveRows(
		moveTestIssue(1, "todo", 0), moveTestIssue(2, "todo", 1), moveTestIssue(3, "todo", 2),
	))
	for _, update := range []struct {
		status string
		pos    uint64
		id     uint64
	}{{"todo", 0, 2}, {"todo", 1, 3}, {"todo", 2, 1}} {
		mock.ExpectExec("UPDATE issues SET status=").WithArgs(update.status, update.pos, nil, nil, update.id, uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	w := sendIssueRequest(h, http.MethodPatch, "/projects/4/issues/1/move", `{"status":"todo","position":2}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":1`) || !strings.Contains(w.Body.String(), `"position":2`) {
		t.Fatalf("move status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueMoveAcrossColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE project_id=.*FOR UPDATE").WithArgs(uint64(4)).WillReturnRows(issueMoveRows(
		moveTestIssue(1, "todo", 0), moveTestIssue(2, "todo", 1), moveTestIssue(3, "done", 0),
	))
	for _, update := range []struct {
		status string
		pos    uint64
		id     uint64
	}{{"todo", 0, 1}, {"done", 0, 2}, {"done", 1, 3}} {
		completed := any(nil)
		if update.id == 2 {
			completed = sqlmock.AnyArg()
		}
		mock.ExpectExec("UPDATE issues SET status=").WithArgs(update.status, update.pos, nil, completed, update.id, uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	// Exercise the production mux registration: direct handler tests alone would
	// miss a route omitted from the service's ServeMux.
	w := sendIssueRequestTo(newIssueMux(h), http.MethodPatch, "/projects/4/issues/2/move", `{"status":"done","position":0}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"done"`) || !strings.Contains(w.Body.String(), `"position":0`) {
		t.Fatalf("move status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueMoveRejectsInvalidStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &issueHandler{db: db}
	w := sendIssueRequest(h, http.MethodPatch, "/projects/4/issues/1/move", `{"status":"blocked","position":0}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueMoveRequiresProjectMembership(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, false)
	defer closeServer()
	w := sendIssueRequest(h, http.MethodPatch, "/projects/4/issues/1/move", `{"status":"todo","position":0}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueRejectsAssigneeOutsideProject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("user_id") == "77" {
			_, _ = w.Write([]byte(`{"member":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"member":true}`))
	}))
	defer server.Close()
	h := &issueHandler{db: db, projectURL: server.URL, client: server.Client()}
	w := sendIssueRequest(h, http.MethodPost, "/issues", `{"project_id":4,"title":"Issue","assignee_id":77}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIssueScheduleRequiresDueDateAfterStartDate(t *testing.T) {
	start := dateValue{Time: time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC), Valid: true}
	due := dateValue{Time: time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC), Valid: true}
	if validIssueSchedule(start, due) {
		t.Fatal("expected reversed issue schedule to be rejected")
	}
}

func TestIssueCalendarListsDateRangeOverlapsForProjectMember(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	start := "2026-10-05"
	due := "2026-10-12"
	mock.ExpectQuery("SELECT id,project_id,title,description,priority,status,assignee_id,reporter_id,position,start_date,due_date,assigned_at,started_at,completed_at FROM issues WHERE project_id=.*ORDER BY COALESCE").WithArgs(uint64(4), "2026-10-31", "2026-10-01", "2026-10-01", "2026-10-31", "2026-10-01", "2026-10-31", "2026-10-01", "2026-10-31", "2026-10-01", "2026-10-31", "2026-10-01", "2026-10-31").WillReturnRows(sqlmock.NewRows([]string{"id", "project_id", "title", "description", "priority", "status", "assignee_id", "reporter_id", "position", "start_date", "due_date", "assigned_at", "started_at", "completed_at"}).AddRow(9, 4, "Release task", "", "high", "todo", nil, 12, 0, start, due, nil, nil, nil))
	w := sendIssueRequest(h, http.MethodGet, "/issues/calendar?project_id=4&from=2026-10-01&to=2026-10-31", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"start_date":"2026-10-05"`) || !strings.Contains(w.Body.String(), `"due_date":"2026-10-12"`) {
		t.Fatalf("calendar status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAnalyticsReturnsCurrentIssueCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*FROM issues WHERE project_id=\\?").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"total", "todo", "in_progress", "done", "critical", "high", "medium", "low", "open_high"}).AddRow(8, 2, 3, 3, 1, 2, 4, 1, 2))
	w := sendIssueRequest(h, http.MethodGet, "/issues/analytics?project_id=4", "")
	for _, expected := range []string{`"total":8`, `"todo":2`, `"in_progress":3`, `"done":3`, `"critical":1`, `"high":2`, `"open_high_priority":2`} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("analytics status=%d missing %s body=%s", w.Code, expected, w.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAnalyticsCanReturnDoneCountsByAssignee(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*FROM issues WHERE project_id=\\?").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"total", "todo", "in_progress", "done", "critical", "high", "medium", "low", "open_high"}).AddRow(8, 2, 3, 3, 1, 2, 4, 1, 2))
	mock.ExpectQuery("SELECT assignee_id, COUNT\\(\\*\\) FROM issues WHERE project_id=\\? AND status='done' GROUP BY assignee_id").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"assignee_id", "count"}).AddRow(11, 2).AddRow(24, 1).AddRow(nil, 3))
	w := sendIssueRequest(h, http.MethodGet, "/issues/analytics?project_id=4&include_assignees=true", "")
	for _, expected := range []string{`"done_by_assignee":[{"assignee_id":11,"count":2},{"assignee_id":24,"count":1}]`, `"done_unassigned":3`} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("analytics status=%d missing %s body=%s", w.Code, expected, w.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAnalyticsRejectsMoreThan500AssignedUsersEvenWithUnassignedGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, true)
	defer closeServer()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*FROM issues WHERE project_id=\\?").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"total", "todo", "in_progress", "done", "critical", "high", "medium", "low", "open_high"}).AddRow(600, 0, 0, 600, 0, 0, 0, 0, 0))
	rows := sqlmock.NewRows([]string{"assignee_id", "count"})
	for id := 1; id <= 501; id++ {
		rows.AddRow(id, 1)
	}
	rows.AddRow(nil, 99)
	mock.ExpectQuery("SELECT assignee_id, COUNT\\(\\*\\) FROM issues WHERE project_id=\\? AND status='done' GROUP BY assignee_id").WithArgs(uint64(4)).WillReturnRows(rows)
	w := sendIssueRequest(h, http.MethodGet, "/issues/analytics?project_id=4&include_assignees=true", "")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("analytics should fail closed on excessive assignees; status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAnalyticsRequiresMembershipAndProjectID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, closeServer := newIssueTestHandler(t, db, false)
	defer closeServer()
	w := sendIssueRequest(h, http.MethodGet, "/issues/analytics?project_id=4", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-member analytics status=%d body=%s", w.Code, w.Body.String())
	}
	w = sendIssueRequest(h, http.MethodGet, "/issues/analytics", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing project_id status=%d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func issueMoveRows(issues ...issue) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "project_id", "title", "description", "priority", "status", "assignee_id", "reporter_id", "position", "start_date", "due_date", "assigned_at", "started_at", "completed_at"})
	for _, item := range issues {
		start, _ := item.StartDate.Value()
		due, _ := item.DueDate.Value()
		rows.AddRow(item.ID, item.ProjectID, item.Title, item.Description, item.Priority, item.Status, item.AssigneeID, item.ReporterID, item.Position, start, due, item.AssignedAt, item.StartedAt, item.CompletedAt)
	}
	return rows
}

func moveTestIssue(id uint64, status string, position uint64) issue {
	return issue{ID: id, ProjectID: 4, Title: "Issue", Description: "", Priority: "medium", Status: status, ReporterID: 12, Position: position}
}
