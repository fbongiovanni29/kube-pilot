package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/fbongiovanni29/kube-pilot/internal/bootstrap"
	"github.com/fbongiovanni29/kube-pilot/internal/config"
	"github.com/fbongiovanni29/kube-pilot/internal/controller"
	"github.com/fbongiovanni29/kube-pilot/internal/llm"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Create LLM client (OpenAI-compatible — works with Claude, GPT, Ollama, etc.)
	client := llm.NewOpenAICompat(llm.OpenAICompatConfig{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		Timeout: cfg.LLM.Timeout,
	})

	// Create Gitea client if using Gitea as git provider
	var gitea *tools.GiteaClient
	if cfg.Git.Provider == "gitea" {
		gitea = tools.NewGiteaClient(cfg.Gitea.URL, cfg.Gitea.AdminUser, cfg.Gitea.AdminPassword)
		logger.Info("using gitea as git provider", "url", cfg.Gitea.URL)
	}

	// Bootstrap infrastructure (idempotent — creates repos, webhooks, labels, secrets)
	bootstrap.Run(context.Background(), cfg, gitea, logger)

	// Set up GitHub App auth if configured
	if cfg.Git.Provider == "github" && cfg.GitHub.AppID != "" {
		keyPath := cfg.GitHub.PrivateKeyPath
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			logger.Error("failed to read github app private key", "path", keyPath, "error", err)
			os.Exit(1)
		}
		ghApp, err := tools.NewGitHubAppAuth(cfg.GitHub.AppID, cfg.GitHub.InstallationID, keyData, logger)
		if err != nil {
			logger.Error("failed to create github app auth", "error", err)
			os.Exit(1)
		}
		ghApp.StartTokenRefresh(context.Background())
		logger.Info("github app auth configured", "app_id", cfg.GitHub.AppID)
	}

	// Set up webhook handler
	webhook := controller.NewWebhookHandler(cfg, client, gitea, logger)

	mux := http.NewServeMux()
	mux.Handle("/webhook", webhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	logger.Info("kube-pilot starting",
		"address", cfg.Server.Address,
		"git_provider", cfg.Git.Provider,
		"model", cfg.LLM.Model,
	)

	if err := http.ListenAndServe(cfg.Server.Address, mux); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
