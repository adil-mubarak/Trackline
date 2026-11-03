package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubMaxBody = 64 << 10
	maxLinkedPRs  = 5
)

type githubApp struct {
	appID, installationID string
	key                   *rsa.PrivateKey
	apiURL                string
	client                *http.Client
}

func newGitHubApp(appID, installationID, privateKey, apiURL string, client *http.Client) (*githubApp, error) {
	appID, installationID = strings.TrimSpace(appID), strings.TrimSpace(installationID)
	if appID == "" || installationID == "" || strings.TrimSpace(privateKey) == "" {
		return nil, errors.New("GitHub App credentials are not configured")
	}
	if n, err := strconv.ParseUint(appID, 10, 64); err != nil || n == 0 {
		return nil, errors.New("invalid GitHub App ID")
	}
	privateKey = strings.ReplaceAll(privateKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, errors.New("invalid GitHub App private key")
	}
	var key *rsa.PrivateKey
	var err error
	if key, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App key must be RSA")
		}
	}
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	parsedURL, err := url.Parse(apiURL)
	if err != nil || parsedURL.Scheme != "https" && parsedURL.Scheme != "http" || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return nil, errors.New("invalid GitHub API URL")
	}
	host := strings.ToLower(parsedURL.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if host != "api.github.com" && !loopback {
		return nil, errors.New("GitHub API URL host is not allowed")
	}
	if !loopback && parsedURL.Scheme != "https" {
		return nil, errors.New("GitHub API URL must use HTTPS")
	}
	if parsedURL.Port() != "" && !loopback {
		return nil, errors.New("custom GitHub API ports are not allowed")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &githubApp{appID: appID, installationID: installationID, key: key, apiURL: strings.TrimRight(apiURL, "/"), client: client}, nil
}

func (g *githubApp) appJWT(now time.Time) (string, error) {
	encode := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	header, err := encode(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := encode(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": g.appID})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + claims
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (g *githubApp) installationToken(ctx context.Context) (string, error) {
	jwt, err := g.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiURL+"/app/installations/"+url.PathEscape(g.installationID)+"/access_tokens", strings.NewReader("{}"))
	if err != nil {
		return "", errors.New("create GitHub token request")
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", errors.New("GitHub installation token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub token endpoint returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil || result.Token == "" {
		return "", errors.New("invalid GitHub token response")
	}
	return result.Token, nil
}

func (g *githubApp) apiRequest(ctx context.Context, method, path, token string) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "..") {
		return nil, errors.New("invalid GitHub API path")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, g.apiURL+path, nil)
	if err != nil {
		return nil, errors.New("create GitHub API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxBody+1))
	if err != nil || len(body) > githubMaxBody {
		return nil, errors.New("GitHub API response exceeds limit")
	}
	return body, nil
}

type githubRepository struct {
	ID    uint64 `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (g *githubApp) repositories(ctx context.Context) ([]githubRepository, error) {
	token, err := g.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	data, err := g.apiRequest(ctx, http.MethodGet, "/installation/repositories?per_page=100", token)
	if err != nil {
		return nil, err
	}
	var response struct {
		Repositories []struct {
			ID    uint64 `json:"id"`
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, errors.New("invalid GitHub repositories response")
	}
	if len(response.Repositories) > 100 {
		return nil, errors.New("GitHub repository result exceeds limit")
	}
	result := make([]githubRepository, 0, len(response.Repositories))
	for _, repo := range response.Repositories {
		if repo.ID > 0 && repo.Name != "" && repo.Owner.Login != "" {
			result = append(result, githubRepository{ID: repo.ID, Owner: repo.Owner.Login, Name: repo.Name})
		}
	}
	return result, nil
}

func githubHandlers(app *githubApp) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories", func(w http.ResponseWriter, r *http.Request) {
		if app == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "GitHub App is not configured"})
			return
		}
		repos, err := app.repositories(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "GitHub repositories are unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, repos)
	})
	mux.HandleFunc("GET /internal/github/repositories/{id}", func(w http.ResponseWriter, r *http.Request) {
		if app == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "GitHub App is not configured"})
			return
		}
		id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid repository ID"})
			return
		}
		repos, err := app.repositories(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorResponse{Error: "GitHub repository verification failed"})
			return
		}
		for _, repo := range repos {
			if repo.ID == id {
				writeJSON(w, http.StatusOK, repo)
				return
			}
		}
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "repository is not installed"})
	})
	return mux
}
