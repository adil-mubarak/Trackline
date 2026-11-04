package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type aiRepositoryVerifier struct {
	baseURL string
	client  *http.Client
}

func (v *aiRepositoryVerifier) VerifyRepository(parent context.Context, repositoryID uint64) (githubRepository, error) {
	if v == nil || strings.TrimSpace(v.baseURL) == "" {
		return githubRepository{}, fmt.Errorf("AI service URL is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	url := strings.TrimRight(v.baseURL, "/") + "/internal/github/repositories/" + strconv.FormatUint(repositoryID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRepository{}, err
	}
	client := v.client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return githubRepository{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return githubRepository{}, fmt.Errorf("AI service returned %s", resp.Status)
	}
	var repo githubRepository
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&repo); err != nil {
		return githubRepository{}, err
	}
	if repo.ID != repositoryID || strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
		return githubRepository{}, fmt.Errorf("AI service returned invalid repository")
	}
	repo.Owner, repo.Name = strings.TrimSpace(repo.Owner), strings.TrimSpace(repo.Name)
	return repo, nil
}
