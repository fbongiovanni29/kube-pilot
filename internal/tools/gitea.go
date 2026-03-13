package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GiteaClient interacts with a Gitea instance via its REST API.
type GiteaClient struct {
	baseURL  string
	user     string
	password string
	http     *http.Client
}

// NewGiteaClient creates a Gitea API client using basic auth.
func NewGiteaClient(baseURL, user, password string) *GiteaClient {
	return &GiteaClient{
		baseURL:  baseURL,
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Do executes an API request against the Gitea instance.
func (g *GiteaClient) Do(ctx context.Context, method, path string, body interface{}) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+"/api/v1"+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.SetBasicAuth(g.user, g.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// CreateRepo creates a new repository in Gitea.
func (g *GiteaClient) CreateRepo(ctx context.Context, name, description string) error {
	payload := map[string]interface{}{
		"name":          name,
		"description":   description,
		"auto_init":     true,
		"default_branch": "main",
		"private":       false,
	}
	_, status, err := g.Do(ctx, "POST", "/user/repos", payload)
	if err != nil {
		return fmt.Errorf("create repo: %w", err)
	}
	if status == 409 {
		return nil // already exists
	}
	if status >= 300 {
		return fmt.Errorf("create repo: HTTP %d", status)
	}
	return nil
}

// CreateWebhook registers a webhook on a Gitea repository.
func (g *GiteaClient) CreateWebhook(ctx context.Context, owner, repo, targetURL, secret string) error {
	payload := map[string]interface{}{
		"type":   "gitea",
		"active": true,
		"config": map[string]string{
			"url":          targetURL,
			"content_type": "json",
			"secret":       secret,
		},
		"events": []string{"issues", "issue_comment", "push"},
	}
	_, status, err := g.Do(ctx, "POST", fmt.Sprintf("/repos/%s/%s/hooks", owner, repo), payload)
	if err != nil {
		return fmt.Errorf("create webhook: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("create webhook: HTTP %d", status)
	}
	return nil
}

// Comment posts a comment on a Gitea issue.
func (g *GiteaClient) Comment(ctx context.Context, owner, repo string, issueNumber int, body string) error {
	payload := map[string]string{"body": body}
	_, status, err := g.Do(ctx, "POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNumber), payload)
	if err != nil {
		return fmt.Errorf("gitea comment: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("gitea comment: HTTP %d", status)
	}
	return nil
}

// CloseIssue closes a Gitea issue.
func (g *GiteaClient) CloseIssue(ctx context.Context, owner, repo string, issueNumber int) error {
	payload := map[string]string{"state": "closed"}
	_, status, err := g.Do(ctx, "PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNumber), payload)
	if err != nil {
		return fmt.Errorf("gitea close: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("gitea close: HTTP %d", status)
	}
	return nil
}

// GetIssue fetches a Gitea issue's details.
func (g *GiteaClient) GetIssue(ctx context.Context, owner, repo string, issueNumber int) (string, error) {
	data, status, err := g.Do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNumber), nil)
	if err != nil {
		return "", fmt.Errorf("gitea get issue: %w", err)
	}
	if status >= 300 {
		return "", fmt.Errorf("gitea get issue: HTTP %d", status)
	}
	return string(data), nil
}

// ListIssues lists open issues for a Gitea repo.
func (g *GiteaClient) ListIssues(ctx context.Context, owner, repo string) (string, error) {
	data, status, err := g.Do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/issues?state=open&limit=50", owner, repo), nil)
	if err != nil {
		return "", fmt.Errorf("gitea list issues: %w", err)
	}
	if status >= 300 {
		return "", fmt.Errorf("gitea list issues: HTTP %d", status)
	}
	return string(data), nil
}

// GetFileContent fetches a file's raw content from a Gitea repo.
// Returns "" with nil error on 404 (file not found).
func (g *GiteaClient) GetFileContent(ctx context.Context, owner, repo, filepath, ref string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/raw/%s", owner, repo, filepath)
	if ref != "" {
		path += "?ref=" + ref
	}

	req, err := http.NewRequestWithContext(ctx, "GET", g.baseURL+"/api/v1"+path, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(g.user, g.password)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gitea get file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gitea get file: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitea get file: HTTP %d", resp.StatusCode)
	}
	return string(data), nil
}

// GetIssueComments fetches all comments on a Gitea issue.
func (g *GiteaClient) GetIssueComments(ctx context.Context, owner, repo string, issueNumber int) (string, error) {
	data, status, err := g.Do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNumber), nil)
	if err != nil {
		return "", fmt.Errorf("gitea get comments: %w", err)
	}
	if status >= 300 {
		return "", fmt.Errorf("gitea get comments: HTTP %d", status)
	}
	return string(data), nil
}

// CreatePullRequest creates a pull request in a Gitea repo.
func (g *GiteaClient) CreatePullRequest(ctx context.Context, owner, repo, title, body, head, base string) error {
	payload := map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	_, status, err := g.Do(ctx, "POST", fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), payload)
	if err != nil {
		return fmt.Errorf("gitea create PR: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("gitea create PR: HTTP %d", status)
	}
	return nil
}

// UpdateFileContent creates or updates a file in a Gitea repo.
// sha is required for updates (optimistic locking); pass "" for new files.
func (g *GiteaClient) UpdateFileContent(ctx context.Context, owner, repo, filepath, content, sha, message string) error {
	payload := map[string]interface{}{
		"content": content,
		"message": message,
	}
	if sha != "" {
		payload["sha"] = sha
	}

	method := "POST"
	if sha != "" {
		method = "PUT"
	}

	_, status, err := g.Do(ctx, method, fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, filepath), payload)
	if err != nil {
		return fmt.Errorf("gitea update file: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("gitea update file: HTTP %d", status)
	}
	return nil
}
