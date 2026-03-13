package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestGitea(handler http.HandlerFunc) (*GiteaClient, *httptest.Server) {
	srv := httptest.NewServer(handler)
	client := NewGiteaClient(srv.URL, "admin", "pass")
	return client, srv
}

func TestGiteaCreateRepo(t *testing.T) {
	var gotBody map[string]interface{}
	var gotAuth string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
	})
	defer srv.Close()

	err := client.CreateRepo(context.Background(), "infra", "Infrastructure repo")
	if err != nil {
		t.Fatalf("CreateRepo() error: %v", err)
	}
	if gotAuth == "" {
		t.Error("expected Basic auth header")
	}
	if gotBody["name"] != "infra" {
		t.Errorf("name = %v, want %q", gotBody["name"], "infra")
	}
	if gotBody["auto_init"] != true {
		t.Error("expected auto_init = true")
	}
}

func TestGiteaCreateRepoAlreadyExists(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409) // conflict = already exists
	})
	defer srv.Close()

	err := client.CreateRepo(context.Background(), "infra", "")
	if err != nil {
		t.Fatalf("CreateRepo() should not error on 409, got: %v", err)
	}
}

func TestGiteaCreateRepoError(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	defer srv.Close()

	err := client.CreateRepo(context.Background(), "infra", "")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestGiteaComment(t *testing.T) {
	var gotPath string
	var gotBody map[string]string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
	})
	defer srv.Close()

	err := client.Comment(context.Background(), "kube-pilot", "infra", 1, "All good!")
	if err != nil {
		t.Fatalf("Comment() error: %v", err)
	}
	if gotPath != "/api/v1/repos/kube-pilot/infra/issues/1/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["body"] != "All good!" {
		t.Errorf("body = %q", gotBody["body"])
	}
}

func TestGiteaCloseIssue(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":1,"state":"closed"}`))
	})
	defer srv.Close()

	err := client.CloseIssue(context.Background(), "kube-pilot", "infra", 5)
	if err != nil {
		t.Fatalf("CloseIssue() error: %v", err)
	}
	if gotMethod != "PATCH" {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/repos/kube-pilot/infra/issues/5" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["state"] != "closed" {
		t.Errorf("state = %q, want closed", gotBody["state"])
	}
}

func TestGiteaGetIssue(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":1,"title":"test issue","state":"open"}`))
	})
	defer srv.Close()

	data, err := client.GetIssue(context.Background(), "kube-pilot", "infra", 1)
	if err != nil {
		t.Fatalf("GetIssue() error: %v", err)
	}

	var issue map[string]interface{}
	json.Unmarshal([]byte(data), &issue)
	if issue["title"] != "test issue" {
		t.Errorf("title = %v", issue["title"])
	}
}

func TestGiteaListIssues(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("expected state=open query param, got %q", r.URL.RawQuery)
		}
		w.Write([]byte(`[{"id":1},{"id":2}]`))
	})
	defer srv.Close()

	data, err := client.ListIssues(context.Background(), "kube-pilot", "infra")
	if err != nil {
		t.Fatalf("ListIssues() error: %v", err)
	}

	var issues []map[string]interface{}
	json.Unmarshal([]byte(data), &issues)
	if len(issues) != 2 {
		t.Errorf("len = %d, want 2", len(issues))
	}
}

func TestGiteaCreateWebhook(t *testing.T) {
	var gotBody map[string]interface{}

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
	})
	defer srv.Close()

	err := client.CreateWebhook(context.Background(), "kube-pilot", "infra", "http://localhost:8080/webhook", "secret")
	if err != nil {
		t.Fatalf("CreateWebhook() error: %v", err)
	}
	if gotBody["type"] != "gitea" {
		t.Errorf("type = %v", gotBody["type"])
	}
	cfg := gotBody["config"].(map[string]interface{})
	if cfg["url"] != "http://localhost:8080/webhook" {
		t.Errorf("config.url = %v", cfg["url"])
	}
}

func TestGiteaHTTPErrors(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	})
	defer srv.Close()

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Comment", func() error { return client.Comment(context.Background(), "o", "r", 1, "b") }},
		{"CloseIssue", func() error { return client.CloseIssue(context.Background(), "o", "r", 1) }},
		{"CreateWebhook", func() error {
			return client.CreateWebhook(context.Background(), "o", "r", "http://x", "s")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err == nil {
				t.Error("expected error on 403")
			}
		})
	}

	// GET endpoints return (string, error)
	if _, err := client.GetIssue(context.Background(), "o", "r", 1); err == nil {
		t.Error("GetIssue: expected error on 403")
	}
	if _, err := client.ListIssues(context.Background(), "o", "r"); err == nil {
		t.Error("ListIssues: expected error on 403")
	}
}

func TestGiteaGetFileContent(t *testing.T) {
	var gotPath, gotRef string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRef = r.URL.Query().Get("ref")
		w.Write([]byte(`# My README`))
	})
	defer srv.Close()

	content, err := client.GetFileContent(context.Background(), "owner", "repo", "README.md", "main")
	if err != nil {
		t.Fatalf("GetFileContent() error: %v", err)
	}
	if gotPath != "/api/v1/repos/owner/repo/raw/README.md" {
		t.Errorf("path = %q, want /api/v1/repos/owner/repo/raw/README.md", gotPath)
	}
	if gotRef != "main" {
		t.Errorf("ref = %q, want %q", gotRef, "main")
	}
	if content != "# My README" {
		t.Errorf("content = %q, want %q", content, "# My README")
	}
}

func TestGiteaGetFileContent404(t *testing.T) {
	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	defer srv.Close()

	content, err := client.GetFileContent(context.Background(), "owner", "repo", "missing.txt", "")
	if err != nil {
		t.Fatalf("GetFileContent() should not error on 404, got: %v", err)
	}
	if content != "" {
		t.Errorf("content = %q, want empty string", content)
	}
}

func TestGiteaGetIssueComments(t *testing.T) {
	var gotPath string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[{"id":1,"body":"nice"},{"id":2,"body":"thanks"}]`))
	})
	defer srv.Close()

	data, err := client.GetIssueComments(context.Background(), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("GetIssueComments() error: %v", err)
	}
	if gotPath != "/api/v1/repos/owner/repo/issues/1/comments" {
		t.Errorf("path = %q", gotPath)
	}

	var comments []map[string]interface{}
	json.Unmarshal([]byte(data), &comments)
	if len(comments) != 2 {
		t.Errorf("len = %d, want 2", len(comments))
	}
}

func TestGiteaCreatePullRequest(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]string

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
	})
	defer srv.Close()

	err := client.CreatePullRequest(context.Background(), "owner", "repo", "Add feature", "PR body", "feature-branch", "main")
	if err != nil {
		t.Fatalf("CreatePullRequest() error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["title"] != "Add feature" {
		t.Errorf("title = %q", gotBody["title"])
	}
	if gotBody["body"] != "PR body" {
		t.Errorf("body = %q", gotBody["body"])
	}
	if gotBody["head"] != "feature-branch" {
		t.Errorf("head = %q", gotBody["head"])
	}
	if gotBody["base"] != "main" {
		t.Errorf("base = %q", gotBody["base"])
	}
}

func TestGiteaUpdateFileContent(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]interface{}

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
		w.Write([]byte(`{"content":{"sha":"newsha"}}`))
	})
	defer srv.Close()

	err := client.UpdateFileContent(context.Background(), "owner", "repo", "path/to/file.json", "Y29udGVudA==", "abc123", "update file")
	if err != nil {
		t.Fatalf("UpdateFileContent() error: %v", err)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/repos/owner/repo/contents/path/to/file.json" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["content"] != "Y29udGVudA==" {
		t.Errorf("content = %v", gotBody["content"])
	}
	if gotBody["sha"] != "abc123" {
		t.Errorf("sha = %v", gotBody["sha"])
	}
	if gotBody["message"] != "update file" {
		t.Errorf("message = %v", gotBody["message"])
	}
}

func TestGiteaUpdateFileContentCreate(t *testing.T) {
	var gotMethod string
	var gotBody map[string]interface{}

	client, srv := newTestGitea(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		w.Write([]byte(`{"content":{"sha":"newsha"}}`))
	})
	defer srv.Close()

	err := client.UpdateFileContent(context.Background(), "owner", "repo", "new-file.json", "Y29udGVudA==", "", "create file")
	if err != nil {
		t.Fatalf("UpdateFileContent() error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST (new file)", gotMethod)
	}
	if _, hasSHA := gotBody["sha"]; hasSHA {
		t.Error("expected no sha field for new file creation")
	}
}
