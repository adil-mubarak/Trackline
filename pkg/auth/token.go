// Package auth contains JWT primitives shared by the issuer and gateway.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type claims struct {
	UserID               uint64 `json:"sub"`
	Role                 string `json:"role"`
	Expires              int64  `json:"exp"`
	MustChangePassword   bool   `json:"must_change_password,omitempty"`
	PasswordResetVersion uint64 `json:"password_reset_version,omitempty"`
	TokenUse             string `json:"token_use,omitempty"`
	Audience             string `json:"aud,omitempty"`
	Scope                string `json:"scope,omitempty"`
	GrantID              uint64 `json:"grant_id,omitempty"`
}

// TokenClaims is the validated identity and scope carried by a Trackline JWT.
type TokenClaims struct {
	UserID               uint64
	Role                 string
	Expires              int64
	MustChangePassword   bool
	PasswordResetVersion uint64
	TokenUse             string
	Audience             string
	Scope                string
	GrantID              uint64
}

func IssueToken(userID uint64, role string, secret []byte, lifetime time.Duration) (string, error) {
	return IssueTokenWithPasswordReset(userID, role, false, secret, lifetime)
}

func IssueTokenWithPasswordReset(userID uint64, role string, mustChangePassword bool, secret []byte, lifetime time.Duration) (string, error) {
	return IssueTokenWithResetState(userID, role, mustChangePassword, 0, secret, lifetime)
}

func IssueTokenWithResetState(userID uint64, role string, mustChangePassword bool, resetVersion uint64, secret []byte, lifetime time.Duration) (string, error) {
	return issueToken(claims{UserID: userID, Role: role, MustChangePassword: mustChangePassword, PasswordResetVersion: resetVersion}, secret, lifetime)
}

// IssueScopedToken issues a short-lived JWT restricted to the supplied audience and scope.
func IssueScopedToken(userID uint64, role string, resetVersion, grantID uint64, audience, scope string, secret []byte, lifetime time.Duration) (string, error) {
	if userID == 0 || (role != "admin" && role != "member") || grantID == 0 || audience == "" || scope == "" || len(secret) < 32 || lifetime <= 0 {
		return "", errors.New("invalid scoped token parameters")
	}
	return issueToken(claims{UserID: userID, Role: role, PasswordResetVersion: resetVersion, TokenUse: "oauth_access", Audience: audience, Scope: scope, GrantID: grantID}, secret, lifetime)
}

func issueToken(c claims, secret []byte, lifetime time.Duration) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	c.Expires = time.Now().Add(lifetime).Unix()
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifyToken(token string, secret []byte) (uint64, string, error) {
	userID, role, _, err := VerifyTokenWithPasswordReset(token, secret)
	return userID, role, err
}

func VerifyTokenWithPasswordReset(token string, secret []byte) (uint64, string, bool, error) {
	userID, role, mustChange, _, err := VerifyTokenWithResetState(token, secret)
	return userID, role, mustChange, err
}

func VerifyTokenWithResetState(token string, secret []byte) (uint64, string, bool, uint64, error) {
	c, err := VerifyTokenClaims(token, secret)
	if err != nil {
		return 0, "", false, 0, err
	}
	return c.UserID, c.Role, c.MustChangePassword, c.PasswordResetVersion, nil
}

// VerifyTokenClaims verifies the signature, algorithm, identity, role, and expiry.
func VerifyTokenClaims(token string, secret []byte) (TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return TokenClaims{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return TokenClaims{}, errors.New("invalid signature")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TokenClaims{}, err
	}
	var headerValues map[string]string
	if err := json.Unmarshal(header, &headerValues); err != nil || headerValues["alg"] != "HS256" {
		return TokenClaims{}, errors.New("invalid algorithm")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenClaims{}, err
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || c.UserID == 0 || c.Expires <= time.Now().Unix() || (c.Role != "admin" && c.Role != "member") {
		return TokenClaims{}, errors.New("invalid claims")
	}
	return TokenClaims(c), nil
}
