package context

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

func newTestStore(handler http.HandlerFunc) (*Store, *httptest.Server) {
	srv := httptest.NewServer(handler)
	client := tools.NewGiteaClient(srv.URL, "admin", "pass")
	store := NewStore(client, "kube-pilot/context")
	return store, srv
}

func TestLoadRepoContextEmpty(t *testing.T) {
	store, srv := newTestStore(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	defer srv.Close()

	rc, err := store.LoadRepoContext(context.Background(), "owner/myrepo")
	if err != nil {
		t.Fatalf("LoadRepoContext() error: %v", err)
	}
	if rc.Repo != "owner/myrepo" {
		t.Errorf("repo = %q, want %q", rc.Repo, "owner/myrepo")
	}
	if len(rc.Insights) != 0 {
		t.Errorf("insights = %d, want 0", len(rc.Insights))
	}
}

func TestLoadRepoContextWithData(t *testing.T) {
	rc := RepoContext{
		Repo: "owner/myrepo",
		Insights: []Insight{
			{Category: "pattern", Content: "Uses Go modules"},
			{Category: "failure", Content: "OOM on large builds"},
		},
	}
	data, _ := json.Marshal(rc)

	store, srv := newTestStore(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	})
	defer srv.Close()

	loaded, err := store.LoadRepoContext(context.Background(), "owner/myrepo")
	if err != nil {
		t.Fatalf("LoadRepoContext() error: %v", err)
	}
	if loaded.Repo != "owner/myrepo" {
		t.Errorf("repo = %q", loaded.Repo)
	}
	if len(loaded.Insights) != 2 {
		t.Errorf("insights = %d, want 2", len(loaded.Insights))
	}
	if loaded.Insights[0].Category != "pattern" {
		t.Errorf("insights[0].category = %q", loaded.Insights[0].Category)
	}
	if loaded.Insights[1].Content != "OOM on large builds" {
		t.Errorf("insights[1].content = %q", loaded.Insights[1].Content)
	}
}

func TestAddInsight(t *testing.T) {
	requestCount := 0
	var savedBody map[string]interface{}

	store, srv := newTestStore(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch {
		// Request 1: GetFileContent (raw) — 404, no existing context
		case r.Method == "GET" && requestCount == 1:
			w.WriteHeader(404)
		// Request 2: getFileSHA (contents API) — 404, new file
		case r.Method == "GET" && requestCount == 2:
			w.WriteHeader(404)
		// Request 3: POST to create file
		case r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&savedBody)
			w.WriteHeader(201)
			w.Write([]byte(`{"content":{"sha":"newsha"}}`))
		default:
			w.WriteHeader(500)
		}
	})
	defer srv.Close()

	err := store.AddInsight(context.Background(), "owner/myrepo", "pattern", "Uses Makefile for builds", "")
	if err != nil {
		t.Fatalf("AddInsight() error: %v", err)
	}
	if savedBody == nil {
		t.Fatal("expected POST body to be captured")
	}
	if savedBody["content"] == nil {
		t.Fatal("expected content field in saved body")
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input      string
		wantOwner  string
		wantRepo   string
	}{
		{"owner/repo", "owner", "repo"},
		{"noslash", "noslash", "noslash"},
		{"a/b/c", "a", "b/c"},
	}
	for _, tt := range tests {
		owner, repo := splitRepo(tt.input)
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("splitRepo(%q) = (%q, %q), want (%q, %q)", tt.input, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestRepoContextPath(t *testing.T) {
	got := repoContextPath("owner/repo")
	want := "repos/owner/repo.json"
	if got != want {
		t.Errorf("repoContextPath() = %q, want %q", got, want)
	}
}

func TestMaxInsightsPruning(t *testing.T) {
	requestCount := 0

	store, srv := newTestStore(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch {
		// getFileSHA — 404, new file
		case r.Method == "GET":
			w.WriteHeader(404)
		// POST to create file
		case r.Method == "POST":
			w.WriteHeader(201)
			w.Write([]byte(`{"content":{"sha":"newsha"}}`))
		default:
			w.WriteHeader(500)
		}
	})
	defer srv.Close()

	// Create a RepoContext with 55 insights
	rc := &RepoContext{Repo: "owner/repo"}
	for i := 0; i < 55; i++ {
		rc.Insights = append(rc.Insights, Insight{
			Category: "pattern",
			Content:  fmt.Sprintf("insight %d", i),
		})
	}

	err := store.SaveRepoContext(context.Background(), rc)
	if err != nil {
		t.Fatalf("SaveRepoContext() error: %v", err)
	}

	// After save, the rc should be pruned to 50
	if len(rc.Insights) != 50 {
		t.Errorf("insights = %d, want 50", len(rc.Insights))
	}
	// Should keep the newest (last 50), so first should be "insight 5"
	if rc.Insights[0].Content != "insight 5" {
		t.Errorf("first insight = %q, want %q", rc.Insights[0].Content, "insight 5")
	}
	if rc.Insights[49].Content != "insight 54" {
		t.Errorf("last insight = %q, want %q", rc.Insights[49].Content, "insight 54")
	}
}
