package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: anthropic
  base_url: https://api.anthropic.com
  api_key: sk-test
  model: claude-sonnet-4-6
git:
  provider: gitea
gitea:
  url: http://gitea:3000
  admin_user: admin
  admin_password: secret
server:
  address: ":9090"
cluster:
  infra_repo: kube-pilot/infra
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("LLM.Provider = %q, want %q", cfg.LLM.Provider, "anthropic")
	}
	if cfg.LLM.BaseURL != "https://api.anthropic.com" {
		t.Errorf("LLM.BaseURL = %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.APIKey != "sk-test" {
		t.Errorf("LLM.APIKey = %q", cfg.LLM.APIKey)
	}
	if cfg.LLM.Model != "claude-sonnet-4-6" {
		t.Errorf("LLM.Model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.Timeout != 120*time.Second {
		t.Errorf("LLM.Timeout = %v, want 120s", cfg.LLM.Timeout)
	}
	if cfg.Git.Provider != "gitea" {
		t.Errorf("Git.Provider = %q, want %q", cfg.Git.Provider, "gitea")
	}
	if cfg.Gitea.URL != "http://gitea:3000" {
		t.Errorf("Gitea.URL = %q", cfg.Gitea.URL)
	}
	if cfg.Gitea.AdminUser != "admin" {
		t.Errorf("Gitea.AdminUser = %q", cfg.Gitea.AdminUser)
	}
	if cfg.Server.Address != ":9090" {
		t.Errorf("Server.Address = %q", cfg.Server.Address)
	}
	if cfg.Cluster.InfraRepo != "kube-pilot/infra" {
		t.Errorf("Cluster.InfraRepo = %q", cfg.Cluster.InfraRepo)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: openai
  model: gpt-4
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Server.Address != ":8080" {
		t.Errorf("default Server.Address = %q, want %q", cfg.Server.Address, ":8080")
	}
	if cfg.LLM.Timeout != 120*time.Second {
		t.Errorf("default LLM.Timeout = %v, want 120s", cfg.LLM.Timeout)
	}
	if cfg.Git.Provider != "github" {
		t.Errorf("default Git.Provider = %q, want %q", cfg.Git.Provider, "github")
	}
}

func TestLoadEnvExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "expanded-key")
	t.Setenv("TEST_GITEA_PASS", "expanded-pass")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  api_key: ${TEST_API_KEY}
gitea:
  admin_password: ${TEST_GITEA_PASS}
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.LLM.APIKey != "expanded-key" {
		t.Errorf("LLM.APIKey = %q, want %q", cfg.LLM.APIKey, "expanded-key")
	}
	if cfg.Gitea.AdminPassword != "expanded-pass" {
		t.Errorf("Gitea.AdminPassword = %q, want %q", cfg.Gitea.AdminPassword, "expanded-pass")
	}
}

func TestLoadGitHubConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
git:
  provider: github
github:
  mode: poll
  poll_interval: 30s
  webhook_secret: gh-secret
  repos:
    - org/repo1
    - org/repo2
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Git.Provider != "github" {
		t.Errorf("Git.Provider = %q", cfg.Git.Provider)
	}
	if cfg.GitHub.Mode != "poll" {
		t.Errorf("GitHub.Mode = %q", cfg.GitHub.Mode)
	}
	if cfg.GitHub.WebhookSecret != "gh-secret" {
		t.Errorf("GitHub.WebhookSecret = %q", cfg.GitHub.WebhookSecret)
	}
	if len(cfg.GitHub.Repos) != 2 {
		t.Errorf("GitHub.Repos len = %d, want 2", len(cfg.GitHub.Repos))
	}
}

func TestLoadObservabilityConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: anthropic
  model: claude-sonnet-4-6
observability:
  enabled: true
  prometheus:
    url: http://prometheus:9090
  grafana:
    url: http://grafana:3000
    api_key: gf-key-123
  loki:
    url: http://loki:3100
  alertmanager:
    url: http://alertmanager:9093
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.Observability.Enabled {
		t.Error("Observability.Enabled = false, want true")
	}
	if cfg.Observability.Prometheus.URL != "http://prometheus:9090" {
		t.Errorf("Prometheus.URL = %q", cfg.Observability.Prometheus.URL)
	}
	if cfg.Observability.Grafana.URL != "http://grafana:3000" {
		t.Errorf("Grafana.URL = %q", cfg.Observability.Grafana.URL)
	}
	if cfg.Observability.Grafana.APIKey != "gf-key-123" {
		t.Errorf("Grafana.APIKey = %q", cfg.Observability.Grafana.APIKey)
	}
	if cfg.Observability.Loki.URL != "http://loki:3100" {
		t.Errorf("Loki.URL = %q", cfg.Observability.Loki.URL)
	}
	if cfg.Observability.Alertmanager.URL != "http://alertmanager:9093" {
		t.Errorf("Alertmanager.URL = %q", cfg.Observability.Alertmanager.URL)
	}
}

func TestLoadObservabilityDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: openai
  model: gpt-4
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Observability.Enabled {
		t.Error("Observability.Enabled = true by default, want false")
	}
}

func TestLoadCrossplaneConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: anthropic
  model: claude-sonnet-4-6
crossplane:
  enabled: true
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.Crossplane.Enabled {
		t.Error("Crossplane.Enabled = false, want true")
	}
}

func TestLoadAgentSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: anthropic
  model: claude-sonnet-4-6
agent:
  system_prompt: |
    You are a custom agent.
    Be careful with tokens.
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Agent.SystemPrompt == "" {
		t.Error("Agent.SystemPrompt is empty, want non-empty")
	}
	if !strings.Contains(cfg.Agent.SystemPrompt, "custom agent") {
		t.Errorf("Agent.SystemPrompt = %q, want to contain 'custom agent'", cfg.Agent.SystemPrompt)
	}
}

func TestLoadCrossplaneDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
llm:
  provider: openai
  model: gpt-4
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Crossplane.Enabled {
		t.Error("Crossplane.Enabled = true by default, want false")
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`{{{not yaml`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
