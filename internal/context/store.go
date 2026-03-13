package context

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fbongiovanni29/kube-pilot/internal/tools"
	"gopkg.in/yaml.v3"
)

const maxInsightsPerRepo = 50

// RepoContext holds cross-session learnings for a repository.
type RepoContext struct {
	Repo      string    `json:"repo"`
	UpdatedAt time.Time `json:"updated_at"`
	Insights  []Insight `json:"insights"`
}

// Insight is a single learned fact about a repository.
type Insight struct {
	Category  string    `json:"category"`            // "pattern", "deployment", "failure", "convention"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	IssueRef  string    `json:"issue_ref,omitempty"`
}

// Store persists repo context in a dedicated Gitea repo.
type Store struct {
	gitea     *tools.GiteaClient
	ctxOwner  string // owner of the context repo
	ctxRepo   string // name of the context repo
}

// NewStore creates a context store backed by a Gitea repo.
// contextRepo should be in "owner/repo" format.
func NewStore(gitea *tools.GiteaClient, contextRepo string) *Store {
	owner, repo := splitRepo(contextRepo)
	return &Store{
		gitea:    gitea,
		ctxOwner: owner,
		ctxRepo:  repo,
	}
}

// LoadRepoContext loads stored insights for a repo.
// Returns an empty RepoContext if none exists.
func (s *Store) LoadRepoContext(ctx context.Context, repoFullName string) (*RepoContext, error) {
	filepath := repoContextPath(repoFullName)
	content, err := s.gitea.GetFileContent(ctx, s.ctxOwner, s.ctxRepo, filepath, "")
	if err != nil {
		return nil, fmt.Errorf("load context: %w", err)
	}
	if content == "" {
		return &RepoContext{Repo: repoFullName}, nil
	}

	var rc RepoContext
	if err := json.Unmarshal([]byte(content), &rc); err != nil {
		return nil, fmt.Errorf("parse context: %w", err)
	}
	return &rc, nil
}

// SaveRepoContext saves repo context, pruning to maxInsightsPerRepo.
func (s *Store) SaveRepoContext(ctx context.Context, rc *RepoContext) error {
	// Prune oldest insights if over cap
	if len(rc.Insights) > maxInsightsPerRepo {
		rc.Insights = rc.Insights[len(rc.Insights)-maxInsightsPerRepo:]
	}
	rc.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}

	filepath := repoContextPath(rc.Repo)
	encoded := base64.StdEncoding.EncodeToString(data)

	// Try to get existing file SHA for update
	sha, err := s.getFileSHA(ctx, filepath)
	if err != nil {
		return fmt.Errorf("get file sha: %w", err)
	}

	msg := fmt.Sprintf("update context for %s", rc.Repo)
	return s.gitea.UpdateFileContent(ctx, s.ctxOwner, s.ctxRepo, filepath, encoded, sha, msg)
}

// AddInsight appends an insight to a repo's context and saves it.
func (s *Store) AddInsight(ctx context.Context, repoFullName, category, content, issueRef string) error {
	rc, err := s.LoadRepoContext(ctx, repoFullName)
	if err != nil {
		return err
	}

	rc.Insights = append(rc.Insights, Insight{
		Category:  category,
		Content:   content,
		CreatedAt: time.Now(),
		IssueRef:  issueRef,
	})

	return s.SaveRepoContext(ctx, rc)
}

// getFileSHA gets the SHA of an existing file (needed for Gitea updates).
// Returns "" if the file doesn't exist.
func (s *Store) getFileSHA(ctx context.Context, filepath string) (string, error) {
	// Use the contents API to get metadata including SHA
	type fileResponse struct {
		SHA string `json:"sha"`
	}

	data, status, err := s.gitea.Do(ctx, "GET", fmt.Sprintf("/repos/%s/%s/contents/%s", s.ctxOwner, s.ctxRepo, filepath), nil)
	if err != nil {
		return "", err
	}
	if status == 404 {
		return "", nil
	}
	if status >= 300 {
		return "", fmt.Errorf("get file metadata: HTTP %d", status)
	}

	var fr fileResponse
	if err := json.Unmarshal(data, &fr); err != nil {
		return "", err
	}
	return fr.SHA, nil
}

func repoContextPath(repoFullName string) string {
	return fmt.Sprintf("repos/%s.json", repoFullName)
}

func splitRepo(fullName string) (string, string) {
	for i, c := range fullName {
		if c == '/' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return fullName, fullName
}

// Initiative links related resources across tools.
type Initiative struct {
	Name        string     `json:"name" yaml:"name"`
	Description string     `json:"description" yaml:"description"`
	Resources   []Resource `json:"resources" yaml:"resources"`
}

// Resource is a linked item in an initiative.
type Resource struct {
	Type string `json:"type" yaml:"type"`                   // "issue", "pr", "jira", "slack"
	Ref  string `json:"ref,omitempty" yaml:"ref,omitempty"` // e.g. "myorg/api-service#15"
	URL  string `json:"url,omitempty" yaml:"url,omitempty"` // e.g. "https://jira.example.com/browse/PROJ-42"
}

// LoadInitiative reads an initiative from the context repo.
// Returns nil, nil if not found.
func (s *Store) LoadInitiative(ctx context.Context, name string) (*Initiative, error) {
	filepath := fmt.Sprintf("initiatives/%s.yaml", name)
	content, err := s.gitea.GetFileContent(ctx, s.ctxOwner, s.ctxRepo, filepath, "")
	if err != nil {
		return nil, fmt.Errorf("load initiative: %w", err)
	}
	if content == "" {
		return nil, nil
	}

	var init Initiative
	if err := yaml.Unmarshal([]byte(content), &init); err != nil {
		return nil, fmt.Errorf("parse initiative: %w", err)
	}
	return &init, nil
}

// SaveInitiative saves an initiative to the context repo.
func (s *Store) SaveInitiative(ctx context.Context, initiative *Initiative) error {
	data, err := yaml.Marshal(initiative)
	if err != nil {
		return fmt.Errorf("marshal initiative: %w", err)
	}

	filepath := fmt.Sprintf("initiatives/%s.yaml", initiative.Name)
	encoded := base64.StdEncoding.EncodeToString(data)

	sha, err := s.getFileSHA(ctx, filepath)
	if err != nil {
		return fmt.Errorf("get file sha: %w", err)
	}

	msg := fmt.Sprintf("update initiative %s", initiative.Name)
	return s.gitea.UpdateFileContent(ctx, s.ctxOwner, s.ctxRepo, filepath, encoded, sha, msg)
}

// ListInitiatives lists all initiative names in the context repo.
func (s *Store) ListInitiatives(ctx context.Context) ([]string, error) {
	path := fmt.Sprintf("/repos/%s/%s/contents/initiatives", s.ctxOwner, s.ctxRepo)
	data, status, err := s.gitea.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("list initiatives: %w", err)
	}
	if status == 404 {
		return nil, nil
	}
	if status >= 300 {
		return nil, fmt.Errorf("list initiatives: HTTP %d", status)
	}

	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse initiative list: %w", err)
	}

	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name, ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name, ".yaml"))
		}
	}
	return names, nil
}

// FindInitiativesForRepo lists all initiatives and returns those with resources referencing the given repo.
func (s *Store) FindInitiativesForRepo(ctx context.Context, repoFullName string) ([]*Initiative, error) {
	names, err := s.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}

	var matched []*Initiative
	for _, name := range names {
		init, err := s.LoadInitiative(ctx, name)
		if err != nil {
			continue
		}
		if init == nil {
			continue
		}
		for _, r := range init.Resources {
			if strings.HasPrefix(r.Ref, repoFullName) {
				matched = append(matched, init)
				break
			}
		}
	}
	return matched, nil
}

// LinkInitiative adds a resource to an initiative, creating it if it doesn't exist.
func (s *Store) LinkInitiative(ctx context.Context, initiativeName string, resource Resource) error {
	init, err := s.LoadInitiative(ctx, initiativeName)
	if err != nil {
		return err
	}
	if init == nil {
		init = &Initiative{
			Name: initiativeName,
		}
	}

	// Dedup by type+ref+url
	for _, r := range init.Resources {
		if r.Type == resource.Type && r.Ref == resource.Ref && r.URL == resource.URL {
			return nil // already linked
		}
	}

	init.Resources = append(init.Resources, resource)
	return s.SaveInitiative(ctx, init)
}
