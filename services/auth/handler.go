package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	sharedauth "github.com/adil/bug-issue-tracker/pkg/auth"
	"github.com/adil/bug-issue-tracker/pkg/trie"
)

type Handler struct {
	db     *sql.DB
	secret []byte
	oauth  OAuthSettings
}

func NewHandler(db *sql.DB, secret []byte) *Handler {
	return &Handler{db: db, secret: secret, oauth: oauthSettingsFromEnv()}
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowedMethod := r.Method == http.MethodPost
	if r.URL.Path == "/users/autocomplete" || r.URL.Path == "/users/directory" || r.URL.Path == "/auth/session" || strings.HasPrefix(r.URL.Path, "/internal/users/") || strings.HasPrefix(r.URL.Path, "/auth/oauth/") {
		allowedMethod = r.Method == http.MethodGet
		if strings.HasSuffix(r.URL.Path, "/link") {
			allowedMethod = r.Method == http.MethodPost
		}
	} else if r.URL.Path == "/users" {
		allowedMethod = r.Method == http.MethodGet || r.Method == http.MethodPost
	} else if r.URL.Path == "/users/names" {
		allowedMethod = r.Method == http.MethodPost
	} else if strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "/status") {
		allowedMethod = r.Method == http.MethodPatch
	}
	if !allowedMethod {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/auth/register":
		h.register(w, r)
	case "/auth/login":
		h.login(w, r)
	case "/auth/oauth/github", "/auth/oauth/google":
		h.startOAuth(w, r, strings.TrimPrefix(r.URL.Path, "/auth/oauth/"), 0)
	case "/auth/oauth/github/callback", "/auth/oauth/google/callback":
		h.finishOAuth(w, r, strings.Split(r.URL.Path, "/")[3])
	case "/auth/oauth/github/link", "/auth/oauth/google/link":
		uid, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
		if err != nil || uid == 0 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h.startOAuth(w, r, strings.Split(r.URL.Path, "/")[3], uid)
	case "/users/autocomplete":
		h.autocompleteUsers(w, r)
	case "/users/directory":
		h.directoryUsers(w, r)
	case "/users/names":
		h.lookupUsernames(w, r)
	case "/users":
		if r.Method == http.MethodGet {
			h.listUsers(w, r)
		} else {
			h.createUser(w, r)
		}
	case "/auth/password":
		h.changePassword(w, r)
	case "/auth/session":
		h.sessionStatus(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/internal/users/") && strings.HasSuffix(r.URL.Path, "/active") {
			h.accountActive(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "/status") {
			h.updateUserStatus(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "/password-reset") {
			h.resetUserPassword(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func (h *Handler) lookupUsernames(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || uid == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		UserIDs []uint64 `json:"user_ids"`
	}
	if err := decodeRequest(w, r, &body, 4096); err != nil || len(body.UserIDs) == 0 || len(body.UserIDs) > 100 {
		writeError(w, http.StatusBadRequest, "user_ids must contain between 1 and 100 IDs")
		return
	}
	uniqueIDs := make([]uint64, 0, len(body.UserIDs))
	seen := make(map[uint64]struct{}, len(body.UserIDs))
	for _, id := range body.UserIDs {
		if id == 0 {
			writeError(w, http.StatusBadRequest, "user_ids must be positive")
			return
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(uniqueIDs)), ",")
	args := make([]any, len(uniqueIDs))
	for i, id := range uniqueIDs {
		args[i] = id
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id,email,active FROM users WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not look up usernames")
		return
	}
	defer rows.Close()
	type userName struct {
		ID       uint64 `json:"id"`
		Username string `json:"username"`
		Active   bool   `json:"active"`
	}
	byID := make(map[uint64]userName, len(uniqueIDs))
	for rows.Next() {
		var id uint64
		var email string
		var active bool
		if err := rows.Scan(&id, &email, &active); err != nil {
			writeError(w, http.StatusInternalServerError, "could not look up usernames")
			return
		}
		byID[id] = userName{ID: id, Username: strings.SplitN(email, "@", 2)[0], Active: active}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not look up usernames")
		return
	}
	result := make([]userName, 0, len(byID))
	for _, id := range body.UserIDs {
		if user, ok := byID[id]; ok {
			result = append(result, user)
		}
	}
	writeJSON(w, result)
}

func (h *Handler) sessionStatus(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tokenVersion, err := strconv.ParseUint(r.Header.Get("X-Password-Reset-Version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}
	var mustChangePassword bool
	var currentVersion uint64
	var active bool
	if err := h.db.QueryRowContext(r.Context(), "SELECT password_reset_required,password_reset_version,active FROM users WHERE id=?", userID).Scan(&mustChangePassword, &currentVersion, &active); err == sql.ErrNoRows {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not validate session")
		return
	}
	if !active {
		writeError(w, http.StatusUnauthorized, "account deactivated")
		return
	}
	if tokenVersion != currentVersion {
		writeError(w, http.StatusUnauthorized, "session revoked")
		return
	}
	writeJSON(w, map[string]any{"password_reset_required": mustChangePassword, "password_reset_version": currentVersion})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	if !isGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "global admin required")
		return
	}
	limit := uint64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	afterID := uint64(0)
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, http.StatusBadRequest, "invalid after_id")
			return
		}
		afterID = parsed
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id,email,role,created_at,password_reset_required,active FROM users WHERE id>? ORDER BY id ASC LIMIT ?", afterID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	defer rows.Close()
	type userRecord struct {
		ID                    uint64    `json:"id"`
		Email                 string    `json:"email"`
		Role                  string    `json:"role"`
		CreatedAt             time.Time `json:"created_at"`
		PasswordResetRequired bool      `json:"password_reset_required"`
		Active                bool      `json:"active"`
	}
	users := []userRecord{}
	for rows.Next() {
		var user userRecord
		if err := rows.Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt, &user.PasswordResetRequired, &user.Active); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list users")
			return
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	writeJSON(w, users)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if !isGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "global admin required")
		return
	}
	var body struct {
		Email             string `json:"email"`
		TemporaryPassword string `json:"temporary_password"`
	}
	if decodeRequest(w, r, &body, 4096) != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if !strings.Contains(body.Email, "@") || len(body.Email) > 320 || !validPassword(body.TemporaryPassword) {
		writeError(w, http.StatusBadRequest, "email or temporary password does not meet requirements")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.TemporaryPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	result, err := h.db.ExecContext(r.Context(), "INSERT INTO users (email,password_hash,role,password_reset_required,password_reset_version) VALUES (?,?, 'member',TRUE,1)", body.Email, string(hash))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "email": body.Email, "role": "member", "active": true, "must_change_password": true})
}

func (h *Handler) updateUserStatus(w http.ResponseWriter, r *http.Request) {
	if !isGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "global admin required")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "status" {
		http.NotFound(w, r)
		return
	}
	userID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || userID == 0 {
		http.NotFound(w, r)
		return
	}
	if actorID, _ := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64); actorID == userID {
		writeError(w, http.StatusBadRequest, "you cannot deactivate your own account")
		return
	}
	var body struct {
		Active *bool `json:"active"`
	}
	if decodeRequest(w, r, &body, 4096) != nil || body.Active == nil {
		writeError(w, http.StatusBadRequest, "active is required")
		return
	}
	var current bool
	if err := h.db.QueryRowContext(r.Context(), "SELECT active FROM users WHERE id=?", userID).Scan(&current); err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update user status")
		return
	}
	if current != *body.Active {
		if _, err := h.db.ExecContext(r.Context(), "UPDATE users SET active=?,password_reset_version=password_reset_version+1 WHERE id=?", *body.Active, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "could not update user status")
			return
		}
	}
	writeJSON(w, map[string]bool{"active": *body.Active})
}

func (h *Handler) accountActive(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "internal" || parts[1] != "users" || parts[3] != "active" {
		http.NotFound(w, r)
		return
	}
	userID, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || userID == 0 {
		http.NotFound(w, r)
		return
	}
	var active bool
	if err := h.db.QueryRowContext(r.Context(), "SELECT active FROM users WHERE id=?", userID).Scan(&active); err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "user not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check account status")
		return
	}
	writeJSON(w, map[string]bool{"active": active})
}

func (h *Handler) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "global admin required")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "users" || parts[2] != "password-reset" {
		http.NotFound(w, r)
		return
	}
	userID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || userID == 0 {
		http.NotFound(w, r)
		return
	}
	var body struct {
		TemporaryPassword string `json:"temporary_password"`
	}
	if decodeRequest(w, r, &body, 4096) != nil || !validPassword(body.TemporaryPassword) {
		writeError(w, http.StatusBadRequest, "temporary_password must be 8 to 72 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.TemporaryPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	result, err := h.db.ExecContext(r.Context(), "UPDATE users SET password_hash=?,password_reset_required=TRUE,password_reset_version=password_reset_version+1 WHERE id=?", string(hash), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	if changed == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]bool{"password_reset": true, "must_change_password": true})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Header.Get("X-Must-Change-Password") != "true" {
		writeError(w, http.StatusForbidden, "password change is not required for this session")
		return
	}
	tokenVersion, err := strconv.ParseUint(r.Header.Get("X-Password-Reset-Version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid session")
		return
	}
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if decodeRequest(w, r, &body, 4096) != nil || !validPassword(body.NewPassword) {
		writeError(w, http.StatusBadRequest, "new_password must be 8 to 72 characters")
		return
	}
	var currentHash string
	if err := h.db.QueryRowContext(r.Context(), "SELECT password_hash FROM users WHERE id=? AND password_reset_required=TRUE AND password_reset_version=?", userID, tokenVersion).Scan(&currentHash); err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "password change is no longer required")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(body.NewPassword)) == nil {
		writeError(w, http.StatusBadRequest, "new password must differ from the temporary password")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	result, err := h.db.ExecContext(r.Context(), "UPDATE users SET password_hash=?,password_reset_required=FALSE WHERE id=? AND password_reset_required=TRUE AND password_reset_version=?", string(hash), userID, tokenVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}
	if changed == 0 {
		writeError(w, http.StatusConflict, "password change is no longer required")
		return
	}
	var role string
	var resetVersion uint64
	if err := h.db.QueryRowContext(r.Context(), "SELECT role,password_reset_version FROM users WHERE id=?", userID).Scan(&role, &resetVersion); err != nil {
		writeError(w, http.StatusInternalServerError, "could not refresh session")
		return
	}
	token, err := sharedauth.IssueTokenWithResetState(userID, role, false, resetVersion, h.secret, time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not refresh session")
		return
	}
	writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600, "user_id": userID, "role": role, "must_change_password": false})
}

func isGlobalAdmin(r *http.Request) bool {
	uid, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	return err == nil && uid > 0 && r.Header.Get("X-User-Role") == "admin"
}

func validPassword(password string) bool { return len(password) >= 8 && len(password) <= 72 }

func decodeRequest(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func (h *Handler) autocompleteUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uid, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || uid == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	prefix := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, err := h.db.QueryContext(r.Context(), "SELECT id,email FROM users WHERE active=TRUE AND email LIKE ? ORDER BY email LIMIT 100", strings.ReplaceAll(prefix, "%", "\\%")+"%")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not search users")
		return
	}
	defer rows.Close()
	index := trie.New()
	userIDs := make(map[string]uint64)
	for rows.Next() {
		var email string
		var userID uint64
		if err := rows.Scan(&userID, &email); err != nil {
			writeError(w, http.StatusInternalServerError, "could not search users")
			return
		}
		username := strings.SplitN(email, "@", 2)[0]
		index.Insert(username)
		userIDs[strings.ToLower(username)] = userID
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not search users")
		return
	}
	results := make([]map[string]any, 0, 10)
	for _, username := range index.Search(prefix, 10) {
		results = append(results, map[string]any{"id": userIDs[strings.ToLower(username)], "username": username})
	}
	writeJSON(w, results)
}

func (h *Handler) directoryUsers(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
	if err != nil || userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit := uint64(25)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	afterID := uint64(0)
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, http.StatusBadRequest, "invalid after_id")
			return
		}
		afterID = parsed
	}
	rows, err := h.db.QueryContext(r.Context(), "SELECT id,email FROM users WHERE active=TRUE AND id>? AND id<>? ORDER BY id ASC LIMIT ?", afterID, userID, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list project members")
		return
	}
	defer rows.Close()
	type directoryUser struct {
		ID       uint64 `json:"id"`
		Username string `json:"username"`
	}
	users := make([]directoryUser, 0, limit)
	var hasMore bool
	for rows.Next() {
		var id uint64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			writeError(w, http.StatusInternalServerError, "could not list project members")
			return
		}
		if uint64(len(users)) == limit {
			hasMore = true
			break
		}
		users = append(users, directoryUser{ID: id, Username: strings.SplitN(email, "@", 2)[0]})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not list project members")
		return
	}
	var nextAfterID *uint64
	if hasMore && len(users) > 0 {
		lastID := users[len(users)-1].ID
		nextAfterID = &lastID
	}
	writeJSON(w, map[string]any{"users": users, "next_after_id": nextAfterID})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decodeCredentials(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if !strings.Contains(input.Email, "@") || len(input.Email) > 320 || len(input.Password) < 8 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "email or password does not meet requirements")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not register user")
		return
	}
	role := "member"
	var count int
	if err := h.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&count); err == nil && count == 0 {
		role = "admin"
	}
	result, err := h.db.ExecContext(r.Context(), "INSERT INTO users (email, password_hash, role) VALUES (?, ?, ?)", input.Email, string(hash), role)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not register user")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not register user")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "email": input.Email, "role": role})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decodeCredentials(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	var userID uint64
	var role string
	var hash string
	var mustChangePassword bool
	var resetVersion uint64
	var active bool
	err := h.db.QueryRowContext(r.Context(), "SELECT id,password_hash,role,password_reset_required,password_reset_version,active FROM users WHERE email=?", strings.ToLower(strings.TrimSpace(input.Email))).Scan(&userID, &hash, &role, &mustChangePassword, &resetVersion, &active)
	if err != nil || !active || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	token, err := sharedauth.IssueTokenWithResetState(userID, role, mustChangePassword, resetVersion, h.secret, time.Hour)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600, "user_id": userID, "role": role, "must_change_password": mustChangePassword})
}

func decodeCredentials(w http.ResponseWriter, r *http.Request, dst *credentials) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]string{"error": message})
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
