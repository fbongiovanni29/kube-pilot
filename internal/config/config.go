package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the operator configuration.
type Config struct {
	LLM     LLMConfig     `yaml:"llm"`
	Git     GitConfig     `yaml:"git"`
	Gitea   GiteaConfig   `yaml:"gitea"`
	GitHub  GitHubConfig  `yaml:"github"`
	Server  ServerConfig  `yaml:"server"`
	Cluster ClusterConfig `yaml:"cluster"`
	Context ContextConfig `yaml:"context"`
}

// ContextConfig holds settings for the cross-session context system.
type ContextConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Repo       string `yaml:"repo"`        // e.g. "kube-pilot/kube-pilot-context"
	AgentsFile string `yaml:"agents_file"` // e.g. "AGENTS.md"
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	Provider string        `yaml:"provider"` // openai, anthropic, ollama
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`
	Timeout  time.Duration `yaml:"timeout"`
}

// GitConfig selects the git provider.
type GitConfig struct {
	Provider string `yaml:"provider"` // gitea or github
}

// GiteaConfig holds Gitea integration settings.
type GiteaConfig struct {
	URL           string `yaml:"url"`
	AdminUser     string `yaml:"admin_user"`
	AdminPassword string `yaml:"admin_password"`
	WebhookSecret string `yaml:"webhook_secret"`
}

// GitHubConfig holds GitHub integration settings.
type GitHubConfig struct {
	Mode           string   `yaml:"mode"` // poll or webhook
	PollInterval   string   `yaml:"poll_interval"`
	WebhookSecret  string   `yaml:"webhook_secret"`
	Repos          []string `yaml:"repos"` // Repos to watch
	AppID          string   `yaml:"app_id"`
	InstallationID string   `yaml:"installation_id"`
	PrivateKeyPath string   `yaml:"private_key_path"`
}

// ServerConfig holds the webhook server settings.
type ServerConfig struct {
	Address string `yaml:"address"`
}

// ClusterConfig holds cluster-related settings.
type ClusterConfig struct {
	InfraRepo   string `yaml:"infra_repo"`   // Git repo for infra manifests
	RegistryURL string `yaml:"registry_url"` // Container registry URL (e.g. "registry.local:5000")
}

// Load reads config from a YAML file with env var expansion.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}
	if cfg.LLM.Timeout == 0 {
		cfg.LLM.Timeout = 120 * time.Second
	}
	if cfg.Git.Provider == "" {
		cfg.Git.Provider = "github"
	}
	if cfg.Context.AgentsFile == "" {
		cfg.Context.AgentsFile = "AGENTS.md"
	}

	return &cfg, nil
}
