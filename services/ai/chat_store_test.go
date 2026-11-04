package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConversationTitleIsBoundedAndSanitized(t *testing.T) {
	title := conversationTitle("\x00  A very long question " + strings.Repeat("word ", 50))
	if strings.ContainsRune(title, '\x00') || len([]rune(title)) > 160 {
		t.Fatalf("conversation title is not safe and bounded: %q", title)
	}
}

func TestSaveChatTurnCreatesOwnerScopedConversation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO ai_conversations \\(user_id,title\\) VALUES \\(\\?,\\?\\)").WithArgs(uint64(17), "Show issue summary").WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec("INSERT INTO ai_messages \\(conversation_id,role,content\\) VALUES \\(\\?, 'user', \\?\\)").WithArgs(uint64(81), "Show issue summary").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO ai_messages \\(conversation_id,role,content,charts\\) VALUES \\(\\?, 'assistant', \\?, \\?\\)").WithArgs(uint64(81), "Three issues are done.", nil).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	id, err := (&conversationStore{db: db}).saveTurn(context.Background(), 17, 0, "Show issue summary", chatResult{Reply: "Three issues are done."})
	if err != nil || id != 81 {
		t.Fatalf("saveTurn() = %d, %v", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveChatTurnCannotAppendToAnotherUsersConversation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE ai_conversations SET updated_at=CURRENT_TIMESTAMP\\(6\\) WHERE id=\\? AND user_id=\\?").WithArgs(uint64(22), uint64(17)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err = (&conversationStore{db: db}).saveTurn(context.Background(), 17, 22, "Read private chat", chatResult{Reply: "no"})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected unauthorized conversation to be rejected, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChatPersistenceMiddlewareReturnsConversationID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO ai_conversations \\(user_id,title\\) VALUES \\(\\?,\\?\\)").WithArgs(uint64(5), "recent changes").WillReturnResult(sqlmock.NewResult(90, 1))
	mock.ExpectExec("INSERT INTO ai_messages \\(conversation_id,role,content\\) VALUES \\(\\?, 'user', \\?\\)").WithArgs(uint64(90), "recent changes").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO ai_messages \\(conversation_id,role,content,charts\\) VALUES \\(\\?, 'assistant', \\?, \\?\\)").WithArgs(uint64(90), "Changed three issues.", nil).WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, chatResult{Reply: "Changed three issues.", Model: "test"})
	})
	handler := persistChatTurns(next, &conversationStore{db: db})
	r := httptest.NewRequest(http.MethodPost, "/ai/chat", strings.NewReader(`{"message":"recent changes"}`))
	r.Header.Set("X-User-ID", "5")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var result chatResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ConversationID != 90 {
		t.Fatalf("unexpected persisted chat response: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConversationRoutesRequireAuthenticatedIdentity(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := conversationRoutes(&conversationStore{db: db})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ai/conversations", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history status=%d body=%s", w.Code, w.Body.String())
	}
}
