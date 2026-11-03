package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type repositoryVerifierFunc func(context.Context, uint64) (githubRepository, error)

func (f repositoryVerifierFunc) VerifyRepository(ctx context.Context, id uint64) (githubRepository, error) {
	return f(ctx, id)
}

func TestRepositoryConnectionVerifiesCanonicalIdentityAndRequiresAdmin(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db, repoVerifier: repositoryVerifierFunc(func(_ context.Context, id uint64) (githubRepository, error) {
		if id != 42 {
			t.Fatalf("verified id=%d", id)
		}
		return githubRepository{ID: 42, Owner: "acme", Name: "tracker"}, nil
	})}
	adminQuery := "SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND m.role='admin' AND p.deleted_at IS NULL)"
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO project_repositories").WithArgs(uint64(9), uint64(42), "acme", "tracker").WillReturnResult(sqlmock.NewResult(0, 1))
	w := projectRequest(h, http.MethodPut, "/projects/9/repository", "5", `{"github_repository_id":42,"owner":"fake","name":"untrusted"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"owner":"acme"`) {
		t.Fatalf("repository response %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWithRepositoryFailsClosedWhenVerifierUnconfigured(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	w := projectRequest(h, http.MethodPost, "/projects", "5", `{"name":"Launch","github_repository_id":42}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAIRepositoryVerifierUsesInstalledRepositoryResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/github/repositories/42" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"id":42,"owner":"acme","name":"tracker"}`)
	}))
	defer server.Close()
	repo, err := (&aiRepositoryVerifier{baseURL: server.URL, client: server.Client()}).VerifyRepository(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if repo.ID != 42 || repo.Owner != "acme" || repo.Name != "tracker" {
		t.Fatalf("repository=%+v", repo)
	}
}

func projectRequest(h *projectHandler, method, path, userID, body string) *httptest.ResponseRecorder {
	return projectRequestWithRole(h, method, path, userID, "member", body)
}

func projectRequestWithRole(h *projectHandler, method, path, userID, role, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	r.Header.Set("X-User-Role", role)
	h.ServeHTTP(w, r)
	return w
}

func activeAccountServer(t *testing.T, active bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/7/active" && r.URL.Path != "/internal/users/6/active" {
			http.NotFound(w, r)
			return
		}
		if active {
			_, _ = w.Write([]byte(`{"active":true}`))
		} else {
			_, _ = w.Write([]byte(`{"active":false}`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProjectCreateTransactionAndAuthentication(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	if got := projectRequest(h, http.MethodGet, "/projects", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("missing user status=%d", got)
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO projects").WithArgs("Roadmap", "Quarterly plan", uint64(22)).WillReturnResult(sqlmock.NewResult(8, 1))
	mock.ExpectExec("INSERT INTO project_members").WithArgs(int64(8), uint64(22)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w := projectRequest(h, http.MethodPost, "/projects", "22", `{"name":" Roadmap ","description":"Quarterly plan"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"owner_id":22`) {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCreateAddsSelectedMembersAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, true)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO projects").WithArgs("Launch", "First release", uint64(22)).WillReturnResult(sqlmock.NewResult(8, 1))
	mock.ExpectExec("INSERT INTO project_members \\(project_id, user_id, role\\) VALUES \\(\\?, \\?, 'admin'\\)").WithArgs(int64(8), uint64(22)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO project_members \\(project_id, user_id, role\\) VALUES \\(\\?, \\?, 'member'\\)").WithArgs(int64(8), uint64(6)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO project_members \\(project_id, user_id, role\\) VALUES \\(\\?, \\?, 'member'\\)").WithArgs(int64(8), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w := projectRequest(h, http.MethodPost, "/projects", "22", `{"name":"Launch","description":"First release","member_ids":[6,7]}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"owner_id":22`) {
		t.Fatalf("project create status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCreateRejectsInactiveSelectedMemberBeforeWriting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, false)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	w := projectRequest(h, http.MethodPost, "/projects", "22", `{"name":"Launch","member_ids":[7]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("inactive member create status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCreateRejectsDuplicateCreatorMemberIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	w := projectRequest(h, http.MethodPost, "/projects", "22", `{"name":"Launch","member_ids":[22,22]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate creator IDs status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectReadAndMembershipChecks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	mock.ExpectQuery("SELECT p.id, p.name, p.description, p.owner_id").WithArgs(uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "owner_id"}).AddRow(3, "App", "Description", 4))
	w := projectRequest(h, http.MethodGet, "/projects", "4", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"App"`) {
		t.Fatalf("list response %d %s", w.Code, w.Body.String())
	}
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM project_members").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	w = projectRequest(h, http.MethodGet, "/projects/3/members/check", "4", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"member":true`) {
		t.Fatalf("membership response %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMembersListAndTargetMembershipCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, true)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM project_members").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT user_id,role FROM project_members").WithArgs(uint64(3)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "role"}).AddRow(4, "admin").AddRow(7, "member"))
	w := projectRequest(h, http.MethodGet, "/projects/3/members", "4", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user_id":7`) || !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("members status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM project_members").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM project_members").WithArgs(uint64(3), uint64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	w = projectRequest(h, http.MethodGet, "/projects/3/members/check?user_id=7", "4", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"member":true`) {
		t.Fatalf("target membership status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAdminCanPromoteMemberAndCannotDemoteOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	adminQuery := "SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND m.role='admin' AND p.deleted_at IS NULL)"
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(3)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(4))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(3), uint64(7)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("UPDATE project_members SET role=").WithArgs("admin", uint64(3), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w := projectRequest(h, http.MethodPatch, "/projects/3/members", "4", `{"user_id":7,"role":"admin"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("promotion status=%d body=%s", w.Code, w.Body.String())
	}

	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(3)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(4))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	w = projectRequest(h, http.MethodPatch, "/projects/3/members", "4", `{"user_id":4,"role":"member"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("owner demotion status=%d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMemberDemotedBeforeRoleMutationIsForbidden(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	adminQuery := "SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND m.role='admin' AND p.deleted_at IS NULL)"
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(3)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(4))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(3), uint64(4)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
	w := projectRequest(h, http.MethodPatch, "/projects/3/members", "4", `{"user_id":7,"role":"admin"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("demoted actor status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectUpdateAndMemberAdministration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, true)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	adminQuery := "SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND m.role='admin' AND p.deleted_at IS NULL)"
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if got := projectRequest(h, http.MethodPut, "/projects/9", "5", `{"name":"Nope"}`).Code; got != http.StatusForbidden {
		t.Fatalf("non-admin update status=%d", got)
	}
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE projects SET name=").WithArgs("Updated", "Details", uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	if got := projectRequest(h, http.MethodPut, "/projects/9", "5", `{"name":"Updated","description":"Details"}`).Code; got != 200 {
		t.Fatalf("update status=%d", got)
	}
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectExec("INSERT INTO project_members").WithArgs(uint64(9), uint64(6)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if got := projectRequest(h, http.MethodPost, "/projects/9/members", "5", `{"user_id":6}`).Code; got != http.StatusCreated {
		t.Fatalf("add member status=%d", got)
	}
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(9), uint64(6)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("DELETE FROM project_members").WithArgs(uint64(9), uint64(6)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if got := projectRequest(h, http.MethodDelete, "/projects/9/members?user_id=6", "5", "").Code; got != http.StatusNoContent {
		t.Fatalf("remove member status=%d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectMemberCannotReadPrivateProject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM project_members").WithArgs(uint64(2), uint64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if got := projectRequest(h, http.MethodGet, "/projects/2", "7", "").Code; got != http.StatusForbidden {
		t.Fatalf("non-member read status=%d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDeleteRequiresOwnerOrGlobalAdminAndSoftDeletes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*deleted_at IS NULL").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	if got := projectRequest(h, http.MethodDelete, "/projects/9", "6", "").Code; got != http.StatusForbidden {
		t.Fatalf("non-owner project manager delete status=%d", got)
	}
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*deleted_at IS NULL").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectExec("UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=.*deleted_at IS NULL").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	if got := projectRequest(h, http.MethodDelete, "/projects/9", "5", "").Code; got != http.StatusNoContent {
		t.Fatalf("owner soft delete status=%d", got)
	}
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*deleted_at IS NULL").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectExec("UPDATE projects SET deleted_at=CURRENT_TIMESTAMP WHERE id=.*deleted_at IS NULL").WithArgs(uint64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	if got := projectRequestWithRole(h, http.MethodDelete, "/projects/9", "1", "admin", "").Code; got != http.StatusNoContent {
		t.Fatalf("global admin soft delete status=%d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformAdminCanManageProjectMembershipsAcrossProjects(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, true)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	mock.ExpectQuery("SELECT id,name,description,owner_id FROM projects WHERE deleted_at IS NULL ORDER BY id").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "owner_id"}).AddRow(9, "Planning", "Roadmap", 5))
	w := projectRequestWithRole(h, http.MethodGet, "/projects", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":9`) {
		t.Fatalf("default global project list status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectQuery("SELECT id,name,description,owner_id FROM projects WHERE deleted_at IS NULL ORDER BY id").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "description", "owner_id"}).AddRow(9, "Planning", "Roadmap", 5))
	w = projectRequestWithRole(h, http.MethodGet, "/projects?scope=all", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":9`) {
		t.Fatalf("global project list status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT user_id,role FROM project_members").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "role"}).AddRow(5, "admin"))
	w = projectRequestWithRole(h, http.MethodGet, "/projects/9/members", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user_id":5`) || !strings.Contains(w.Body.String(), `"user_id":1,"role":"member","synthetic":true`) {
		t.Fatalf("global member list status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(10)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	w = projectRequestWithRole(h, http.MethodGet, "/projects/10/members", "1", "admin", "")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), `"synthetic":true`) {
		t.Fatalf("nonexistent project must not grant synthetic membership: status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectExec("INSERT INTO project_members").WithArgs(uint64(9), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w = projectRequestWithRole(h, http.MethodPost, "/projects/9/members", "1", "admin", `{"user_id":7}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("global add member status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT owner_id FROM projects WHERE id=.*FOR UPDATE").WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(5))
	mock.ExpectQuery("SELECT role FROM project_members WHERE project_id=.*FOR UPDATE").WithArgs(uint64(9), uint64(7)).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("DELETE FROM project_members").WithArgs(uint64(9), uint64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	w = projectRequestWithRole(h, http.MethodDelete, "/projects/9/members?user_id=7", "1", "admin", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("global remove member status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyPlatformAdminsCanRequestAllProjects(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &projectHandler{db: db}
	w := projectRequest(h, http.MethodGet, "/projects?scope=all", "4", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin all-projects status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformAdminHasEffectiveMembershipWithoutPersistedRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, true)
	defer authServer.Close()
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	w := projectRequestWithRole(h, http.MethodGet, "/projects/9/members/check", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"member":true`) {
		t.Fatalf("admin effective membership status=%d body=%s", w.Code, w.Body.String())
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	w = projectRequestWithRole(h, http.MethodGet, "/projects/9/members/check?user_id=1", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"member":true`) {
		t.Fatalf("global admin should be assignable as self: status=%d body=%s", w.Code, w.Body.String())
	}

	// Effective access does not make the admin an eligible assignee on behalf
	// of another user; target membership is still checked against stored rows.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND deleted_at IS NULL)")).WithArgs(uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND p.deleted_at IS NULL)")).WithArgs(uint64(9), uint64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	w = projectRequestWithRole(h, http.MethodGet, "/projects/9/members/check?user_id=7", "1", "admin", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"member":false`) {
		t.Fatalf("nonmember target check status=%d body=%s", w.Code, w.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAdminCannotAddInactiveAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer := activeAccountServer(t, false)
	h := &projectHandler{db: db, authURL: authServer.URL, client: authServer.Client()}
	adminQuery := "SELECT EXISTS(SELECT 1 FROM project_members m JOIN projects p ON p.id=m.project_id WHERE m.project_id=? AND m.user_id=? AND m.role='admin' AND p.deleted_at IS NULL)"
	mock.ExpectQuery(regexp.QuoteMeta(adminQuery)).WithArgs(uint64(9), uint64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	w := projectRequest(h, http.MethodPost, "/projects/9/members", "5", `{"user_id":7}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("inactive member status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
