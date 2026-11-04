package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const transcriptRetention = 90 * 24 * time.Hour
const maxConversationMessages = 200

var errConversationMessageLimit = errors.New("chat has reached its message limit")

type aiChartPoint struct {
	Label string `json:"label"`
	Value uint64 `json:"value"`
}

type aiChart struct {
	Type  string         `json:"type"`
	Title string         `json:"title"`
	Note  string         `json:"note"`
	Data  []aiChartPoint `json:"data"`
}

type aiConversation struct {
	ID        uint64    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type aiStoredMessage struct {
	ID        uint64    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Charts    []aiChart `json:"charts,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type conversationStore struct{ db *sql.DB }

func (s *conversationStore) owns(ctx context.Context, userID, conversationID uint64) (bool, error) {
	var exists uint8
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM ai_conversations WHERE id=? AND user_id=? AND updated_at >= UTC_TIMESTAMP(6) - INTERVAL 90 DAY", conversationID, userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *conversationStore) canAppend(ctx context.Context, conversationID uint64) (bool, error) {
	var messageCount int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ai_messages WHERE conversation_id=?", conversationID).Scan(&messageCount)
	if err != nil {
		return false, err
	}
	return messageCount+2 <= maxConversationMessages, nil
}

func (s *conversationStore) saveTurn(ctx context.Context, userID, conversationID uint64, question string, answer chatResult) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin conversation transaction: %w", err)
	}
	defer tx.Rollback()
	if conversationID == 0 {
		result, err := tx.ExecContext(ctx, "INSERT INTO ai_conversations (user_id,title) VALUES (?,?)", userID, conversationTitle(question))
		if err != nil {
			return 0, fmt.Errorf("create conversation: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil || id <= 0 {
			return 0, errors.New("create conversation returned invalid ID")
		}
		conversationID = uint64(id)
	} else {
		result, err := tx.ExecContext(ctx, "UPDATE ai_conversations SET updated_at=CURRENT_TIMESTAMP(6) WHERE id=? AND user_id=? AND updated_at >= UTC_TIMESTAMP(6) - INTERVAL 90 DAY", conversationID, userID)
		if err != nil {
			return 0, fmt.Errorf("update conversation: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return 0, sql.ErrNoRows
		}
		var messageCount int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ai_messages WHERE conversation_id=?", conversationID).Scan(&messageCount); err != nil {
			return 0, fmt.Errorf("count conversation messages: %w", err)
		}
		if messageCount+2 > maxConversationMessages {
			return 0, errConversationMessageLimit
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO ai_messages (conversation_id,role,content) VALUES (?, 'user', ?)", conversationID, question); err != nil {
		return 0, fmt.Errorf("save user message: %w", err)
	}
	var charts any
	if len(answer.Charts) > 0 {
		encoded, err := json.Marshal(answer.Charts)
		if err != nil {
			return 0, fmt.Errorf("encode assistant charts: %w", err)
		}
		charts = encoded
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO ai_messages (conversation_id,role,content,charts) VALUES (?, 'assistant', ?, ?)", conversationID, answer.Reply, charts); err != nil {
		return 0, fmt.Errorf("save assistant message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit conversation turn: %w", err)
	}
	return conversationID, nil
}

func conversationTitle(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(message))
	message = strings.Join(strings.Fields(message), " ")
	if utf8.RuneCountInString(message) > 157 {
		message = string([]rune(message)[:157]) + "..."
	}
	if message == "" {
		return "New chat"
	}
	return message
}

func (s *conversationStore) list(ctx context.Context, userID uint64, afterID uint64, limit int) ([]aiConversation, error) {
	query := "SELECT id,title,created_at,updated_at FROM ai_conversations WHERE user_id=? AND updated_at >= UTC_TIMESTAMP(6) - INTERVAL 90 DAY"
	args := []any{userID}
	if afterID > 0 {
		query += " AND id<?"
		args = append(args, afterID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	items := make([]aiConversation, 0, limit)
	for rows.Next() {
		var item aiConversation
		if err := rows.Scan(&item.ID, &item.Title, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	return items, nil
}

func (s *conversationStore) messages(ctx context.Context, userID, conversationID uint64) ([]aiStoredMessage, error) {
	var exists uint64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM ai_conversations WHERE id=? AND user_id=? AND updated_at >= UTC_TIMESTAMP(6) - INTERVAL 90 DAY", conversationID, userID).Scan(&exists)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,role,content,charts,created_at FROM ai_messages WHERE conversation_id=? ORDER BY id ASC LIMIT 200", conversationID)
	if err != nil {
		return nil, fmt.Errorf("load conversation messages: %w", err)
	}
	defer rows.Close()
	items := make([]aiStoredMessage, 0)
	for rows.Next() {
		var item aiStoredMessage
		var rawCharts []byte
		if err := rows.Scan(&item.ID, &item.Role, &item.Content, &rawCharts, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		if len(rawCharts) > 0 && json.Unmarshal(rawCharts, &item.Charts) != nil {
			return nil, errors.New("invalid stored chart data")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation messages: %w", err)
	}
	return items, nil
}

func (s *conversationStore) delete(ctx context.Context, userID, conversationID uint64) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM ai_conversations WHERE id=? AND user_id=?", conversationID, userID)
	if err != nil {
		return false, fmt.Errorf("delete conversation: %w", err)
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *conversationStore) purgeExpired(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		result, err := s.db.ExecContext(ctx, "DELETE FROM ai_conversations WHERE updated_at < UTC_TIMESTAMP(6) - INTERVAL 90 DAY ORDER BY updated_at ASC LIMIT 500")
		if err != nil {
			return fmt.Errorf("purge expired conversations: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count purged conversations: %w", err)
		}
		if count < 500 {
			return nil
		}
	}
	return nil
}

func conversationRoutes(store *conversationStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
		if err != nil || userID == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/ai/conversations" {
			switch r.Method {
			case http.MethodGet:
				limit := 30
				if raw := r.URL.Query().Get("limit"); raw != "" {
					parsed, err := strconv.Atoi(raw)
					if err != nil || parsed < 1 || parsed > 50 {
						http.Error(w, "invalid limit", http.StatusBadRequest)
						return
					}
					limit = parsed
				}
				afterID := uint64(0)
				if raw := r.URL.Query().Get("after_id"); raw != "" {
					afterID, err = strconv.ParseUint(raw, 10, 64)
					if err != nil || afterID == 0 {
						http.Error(w, "invalid after_id", http.StatusBadRequest)
						return
					}
				}
				items, err := store.list(r.Context(), userID, afterID, limit)
				if err != nil {
					http.Error(w, "could not list chats", http.StatusInternalServerError)
					return
				}
				writeJSON(w, http.StatusOK, items)
			default:
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if (len(parts) != 3 && len(parts) != 4) || parts[0] != "ai" || parts[1] != "conversations" {
			http.NotFound(w, r)
			return
		}
		conversationID, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil || conversationID == 0 || len(parts) == 4 && parts[3] != "messages" {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 4 && r.Method == http.MethodGet {
			items, err := store.messages(r.Context(), userID, conversationID)
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				http.Error(w, "could not load chat", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, items)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodDelete {
			deleted, err := store.delete(r.Context(), userID, conversationID)
			if err != nil {
				http.Error(w, "could not delete chat", http.StatusInternalServerError)
				return
			}
			if !deleted {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Allow", "GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

type bufferedResponseWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }
func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *bufferedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func persistChatTurns(next http.Handler, store *conversationStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		requestBody, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(requestBody)))
		var input chatInput
		if json.Unmarshal(requestBody, &input) != nil {
			next.ServeHTTP(w, r)
			return
		}
		userID, err := strconv.ParseUint(r.Header.Get("X-User-ID"), 10, 64)
		if err != nil || userID == 0 {
			if input.ConversationID > 0 {
				writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "authentication required"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if input.ConversationID > 0 {
			owns, err := store.owns(r.Context(), userID, input.ConversationID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not load chat"})
				return
			}
			if !owns {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "chat not found"})
				return
			}
			canAppend, err := store.canAppend(r.Context(), input.ConversationID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not load chat"})
				return
			}
			if !canAppend {
				writeJSON(w, http.StatusConflict, errorResponse{Error: "chat reached its 100-turn limit; start a new chat"})
				return
			}
		}
		buffered := newBufferedResponseWriter()
		next.ServeHTTP(buffered, r)
		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}
		if status == http.StatusOK {
			var result chatResult
			if json.Unmarshal([]byte(buffered.body.String()), &result) == nil && strings.TrimSpace(result.Reply) != "" {
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				conversationID, saveErr := store.saveTurn(ctx, userID, input.ConversationID, input.Message, result)
				cancel()
				if saveErr != nil {
					slog.Warn("could not persist AI chat turn", "request_id", r.Header.Get("X-Request-ID"), "error", saveErr)
					if errors.Is(saveErr, errConversationMessageLimit) {
						writeJSON(w, http.StatusConflict, errorResponse{Error: "chat reached its 100-turn limit; start a new chat"})
					} else {
						writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not save chat history"})
					}
					return
				}
				result.ConversationID = conversationID
				buffered.body.Reset()
				encoded, _ := json.Marshal(result)
				buffered.body.Write(encoded)
				buffered.body.WriteByte('\n')
			}
		}
		for key, values := range buffered.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, buffered.body.String())
	})
}
