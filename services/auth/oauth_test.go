package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOAuthStateBindsProviderAndRejectsTampering(t *testing.T) {
	h := &Handler{secret: []byte("a sufficiently long test secret for oauth state")}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/oauth/google", nil)
	h.oauth = OAuthSettings{GoogleClientID: "client", GoogleClientSecret: "secret", CallbackBaseURL: "https://trackline.example"}
	h.startOAuth(w, r, "google", 42)
	if w.Code != http.StatusFound {
		t.Fatalf("start status=%d", w.Code)
	}
	state := w.Result().Cookies()[0].Value
	if !h.validOAuthState(state, "google", state) || h.validOAuthState(state, "github", state) {
		t.Fatal("expected valid provider-bound OAuth state")
	}
	tampered := state + "x"
	if h.validOAuthState(tampered, "google", tampered) {
		t.Fatal("expected tampered state to be rejected")
	}
	if !strings.Contains(w.Header().Get("Location"), "accounts.google.com") {
		t.Fatalf("expected redirect to Google authorization endpoint: %s", w.Header().Get("Location"))
	}
}

func TestLinkOAuthIdentityRejectsReplacingLinkedProvider(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT user_id FROM oauth_identities WHERE provider=\\? AND provider_subject=\\?").WithArgs("google", "new-subject").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT provider_subject FROM oauth_identities WHERE user_id=\\? AND provider=\\?").WithArgs(uint64(7), "google").WillReturnRows(sqlmock.NewRows([]string{"provider_subject"}).AddRow("old-subject"))
	err = linkOAuthIdentity(context.Background(), db, 7, "google", oauthIdentity{Subject: "new-subject", Email: "user@example.test"})
	if !errors.Is(err, errOAuthIdentityConflict) {
		t.Fatalf("expected provider conflict rather than successful no-op; error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthUnconfiguredProviderDoesNotStartRedirect(t *testing.T) {
	h := &Handler{secret: []byte("a sufficiently long test secret for oauth state")}
	w := httptest.NewRecorder()
	h.startOAuth(w, httptest.NewRequest(http.MethodGet, "/auth/oauth/github", nil), "github", 0)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "oauth_error=provider_unavailable") {
		t.Fatalf("expected safe configuration error redirect, status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
}
