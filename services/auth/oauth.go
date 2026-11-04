package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sharedauth "github.com/adil/bug-issue-tracker/pkg/auth"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

type OAuthSettings struct {
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	CallbackBaseURL    string
	FrontendURL        string
}

func oauthSettingsFromEnv() OAuthSettings {
	return OAuthSettings{
		GitHubClientID: os.Getenv("GITHUB_OAUTH_CLIENT_ID"), GitHubClientSecret: os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
		GoogleClientID: os.Getenv("GOOGLE_OAUTH_CLIENT_ID"), GoogleClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		CallbackBaseURL: strings.TrimRight(os.Getenv("OAUTH_CALLBACK_BASE_URL"), "/"),
		FrontendURL:     strings.TrimRight(os.Getenv("OAUTH_FRONTEND_URL"), "/"),
	}
}

type oauthIdentity struct {
	Subject string
	Email   string
}

func (h *Handler) oauthConfig(provider string) (oauth2.Config, bool) {
	var config oauth2.Config
	switch provider {
	case "github":
		config = oauth2.Config{ClientID: h.oauth.GitHubClientID, ClientSecret: h.oauth.GitHubClientSecret, Scopes: []string{"read:user", "user:email"}, Endpoint: github.Endpoint}
	case "google":
		config = oauth2.Config{ClientID: h.oauth.GoogleClientID, ClientSecret: h.oauth.GoogleClientSecret, Scopes: []string{"openid", "email", "profile"}, Endpoint: google.Endpoint}
	default:
		return oauth2.Config{}, false
	}
	if config.ClientID == "" || config.ClientSecret == "" || h.oauth.CallbackBaseURL == "" {
		return oauth2.Config{}, false
	}
	config.RedirectURL = h.oauth.CallbackBaseURL + "/api/auth/oauth/" + provider + "/callback"
	return config, true
}

func (h *Handler) startOAuth(w http.ResponseWriter, r *http.Request, provider string, linkUserID uint64) {
	config, ok := h.oauthConfig(provider)
	if !ok {
		if r.Method == http.MethodPost {
			writeError(w, http.StatusServiceUnavailable, "sign-in provider is not configured")
		} else {
			h.oauthRedirectError(w, r, "provider_unavailable")
		}
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		http.Error(w, "could not start sign in", http.StatusInternalServerError)
		return
	}
	stateBody := provider + ":" + strconv.FormatUint(linkUserID, 10) + ":" + base64.RawURLEncoding.EncodeToString(random)
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(stateBody))
	state := base64.RawURLEncoding.EncodeToString([]byte(stateBody)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{Name: "trackline_oauth_state", Value: state, Path: "/api/auth/oauth/" + provider + "/callback", MaxAge: 600, HttpOnly: true, Secure: strings.HasPrefix(h.oauth.CallbackBaseURL, "https://"), SameSite: http.SameSiteLaxMode})
	url := config.AuthCodeURL(state, oauth2.AccessTypeOnline)
	if r.Method == http.MethodPost {
		writeJSON(w, map[string]string{"authorization_url": url})
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *Handler) finishOAuth(w http.ResponseWriter, r *http.Request, provider string) {
	cookie, err := r.Cookie("trackline_oauth_state")
	if err != nil || !h.validOAuthState(cookie.Value, provider, r.URL.Query().Get("state")) {
		h.oauthRedirectError(w, r, "invalid_oauth_state")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "trackline_oauth_state", Value: "", Path: "/api/auth/oauth/" + provider + "/callback", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(h.oauth.CallbackBaseURL, "https://"), SameSite: http.SameSiteLaxMode})
	if r.URL.Query().Get("error") != "" {
		h.oauthRedirectError(w, r, "provider_denied")
		return
	}
	parts, _ := h.parseOAuthState(cookie.Value)
	linkUserID, _ := strconv.ParseUint(parts[1], 10, 64)
	config, ok := h.oauthConfig(provider)
	if !ok {
		h.oauthRedirectError(w, r, "provider_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	token, err := config.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		h.oauthRedirectError(w, r, "provider_exchange_failed")
		return
	}
	identity, err := fetchOAuthIdentity(ctx, provider, config.Client(ctx, token))
	if err != nil || identity.Subject == "" || identity.Email == "" {
		h.oauthRedirectError(w, r, "provider_identity_unavailable")
		return
	}
	var userID uint64
	if linkUserID > 0 {
		var currentEmail string
		if err := h.db.QueryRowContext(ctx, "SELECT email FROM users WHERE id=? AND active=TRUE", linkUserID).Scan(&currentEmail); err != nil {
			h.oauthRedirectError(w, r, "account_unavailable")
			return
		}
		if err := linkOAuthIdentity(ctx, h.db, linkUserID, provider, identity); err != nil {
			if errors.Is(err, errOAuthIdentityConflict) {
				h.oauthRedirectError(w, r, "provider_already_linked")
			} else {
				h.oauthRedirectError(w, r, "account_link_failed")
			}
			return
		}
		userID = linkUserID
	} else {
		userID, err = h.findOrCreateOAuthUser(ctx, provider, identity)
		if err != nil {
			if errors.Is(err, errAccountLinkRequired) {
				h.oauthRedirectError(w, r, "account_link_required")
			} else {
				h.oauthRedirectError(w, r, "sign_in_failed")
			}
			return
		}
	}
	var role string
	var mustChange bool
	var resetVersion uint64
	if err := h.db.QueryRowContext(ctx, "SELECT role,password_reset_required,password_reset_version FROM users WHERE id=? AND active=TRUE", userID).Scan(&role, &mustChange, &resetVersion); err != nil {
		h.oauthRedirectError(w, r, "account_unavailable")
		return
	}
	jwt, err := sharedauth.IssueTokenWithResetState(userID, role, mustChange, resetVersion, h.secret, time.Hour)
	if err != nil {
		h.oauthRedirectError(w, r, "sign_in_failed")
		return
	}
	if linkUserID > 0 {
		h.oauthRedirect(w, r, "oauth_linked", "true")
		return
	}
	h.oauthRedirect(w, r, "oauth_token", jwt)
}

var errAccountLinkRequired = errors.New("account linking required")
var errOAuthIdentityConflict = errors.New("OAuth identity conflict")

func linkOAuthIdentity(ctx context.Context, db *sql.DB, userID uint64, provider string, identity oauthIdentity) error {
	var identityOwner uint64
	err := db.QueryRowContext(ctx, "SELECT user_id FROM oauth_identities WHERE provider=? AND provider_subject=?", provider, identity.Subject).Scan(&identityOwner)
	if err == nil && identityOwner != userID {
		return errOAuthIdentityConflict
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var existingSubject string
	err = db.QueryRowContext(ctx, "SELECT provider_subject FROM oauth_identities WHERE user_id=? AND provider=?", userID, provider).Scan(&existingSubject)
	if err == nil {
		if existingSubject != identity.Subject {
			return errOAuthIdentityConflict
		}
		_, err = db.ExecContext(ctx, "UPDATE oauth_identities SET provider_email=? WHERE user_id=? AND provider=? AND provider_subject=?", strings.ToLower(identity.Email), userID, provider, identity.Subject)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO oauth_identities (user_id,provider,provider_subject,provider_email) VALUES (?,?,?,?)", userID, provider, identity.Subject, strings.ToLower(identity.Email))
	return err
}

func (h *Handler) findOrCreateOAuthUser(ctx context.Context, provider string, identity oauthIdentity) (uint64, error) {
	var userID uint64
	err := h.db.QueryRowContext(ctx, "SELECT user_id FROM oauth_identities WHERE provider=? AND provider_subject=?", provider, identity.Subject).Scan(&userID)
	if err == nil {
		var active bool
		if err := h.db.QueryRowContext(ctx, "SELECT active FROM users WHERE id=?", userID).Scan(&active); err != nil || !active {
			return 0, errors.New("account unavailable")
		}
		return userID, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	err = h.db.QueryRowContext(ctx, "SELECT id FROM users WHERE email=?", email).Scan(&userID)
	if err == nil {
		return 0, errAccountLinkRequired
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	passwordMaterial := make([]byte, 48)
	if _, err := rand.Read(passwordMaterial); err != nil {
		return 0, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(base64.RawURLEncoding.EncodeToString(passwordMaterial)), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return 0, err
	}
	role := "member"
	if count == 0 {
		role = "admin"
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO users (email,password_hash,role) VALUES (?,?,?)", email, string(hash), role)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO oauth_identities (user_id,provider,provider_subject,provider_email) VALUES (?,?,?,?)", id, provider, identity.Subject, email); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (h *Handler) parseOAuthState(state string) ([]string, bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return nil, false
	}
	body, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	fields := strings.Split(string(body), ":")
	if len(fields) != 3 {
		return nil, false
	}
	if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
		return nil, false
	}
	return fields, true
}

func (h *Handler) validOAuthState(cookie, provider, query string) bool {
	if cookie == "" || query == "" || !hmac.Equal([]byte(cookie), []byte(query)) {
		return false
	}
	fields, ok := h.parseOAuthState(cookie)
	return ok && fields[0] == provider
}

func fetchOAuthIdentity(ctx context.Context, provider string, client *http.Client) (oauthIdentity, error) {
	requestJSON := func(endpoint string, destination any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if provider == "github" {
			req.Header.Set("User-Agent", "Trackline")
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("provider returned %s", resp.Status)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(destination)
	}
	if provider == "google" {
		var profile struct {
			Subject  string `json:"sub"`
			Email    string `json:"email"`
			Verified bool   `json:"email_verified"`
		}
		err := requestJSON("https://openidconnect.googleapis.com/v1/userinfo", &profile)
		if err != nil || !profile.Verified {
			return oauthIdentity{}, errors.New("google email is not verified")
		}
		return oauthIdentity{Subject: profile.Subject, Email: profile.Email}, nil
	}
	var profile struct {
		ID int64 `json:"id"`
	}
	if err := requestJSON("https://api.github.com/user", &profile); err != nil || profile.ID <= 0 {
		return oauthIdentity{}, errors.New("could not read github profile")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := requestJSON("https://api.github.com/user/emails", &emails); err != nil {
		return oauthIdentity{}, err
	}
	for _, candidate := range emails {
		if candidate.Primary && candidate.Verified && strings.TrimSpace(candidate.Email) != "" {
			return oauthIdentity{Subject: strconv.FormatInt(profile.ID, 10), Email: candidate.Email}, nil
		}
	}
	return oauthIdentity{}, errors.New("github has no verified primary email")
}

func (h *Handler) oauthRedirectError(w http.ResponseWriter, r *http.Request, reason string) {
	h.oauthRedirect(w, r, "oauth_error", reason)
}

func (h *Handler) oauthRedirect(w http.ResponseWriter, r *http.Request, key, value string) {
	base := h.oauth.FrontendURL
	if base == "" {
		base = "/"
	}
	u, err := url.Parse(base)
	if err != nil || (u.IsAbs() && u.Scheme != "https" && u.Scheme != "http") {
		http.Error(w, "OAuth redirect is not configured safely", http.StatusInternalServerError)
		return
	}
	fragment := url.Values{}
	fragment.Set(key, value)
	u.Fragment = fragment.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
