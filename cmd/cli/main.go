package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/fbongiovanni29/kube-pilot/internal/agent"
	"github.com/fbongiovanni29/kube-pilot/internal/config"
	"github.com/fbongiovanni29/kube-pilot/internal/llm"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

// CLI mode: run kube-pilot with a one-shot task from the command line.
// Usage: kube-pilot-cli --config config.yaml "check what's running in the cluster"
func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" {
		fmt.Fprintln(os.Stderr, "Usage: kube-pilot-cli --config config.yaml \"<task>\"")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	client := llm.NewOpenAICompat(llm.OpenAICompatConfig{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		Timeout: cfg.LLM.Timeout,
	})

	// Create Gitea client if configured
	var gitea *tools.GiteaClient
	var giteaInfo *agent.GiteaInfo
	if cfg.Git.Provider == "gitea" {
		gitea = tools.NewGiteaClient(cfg.Gitea.URL, cfg.Gitea.AdminUser, cfg.Gitea.AdminPassword)
		giteaInfo = &agent.GiteaInfo{
			URL:      cfg.Gitea.URL,
			User:     cfg.Gitea.AdminUser,
			Password: cfg.Gitea.AdminPassword,
		}
	}

	a := agent.New(client, gitea, giteaInfo, logger)
	result, err := a.Run(context.Background(), task)
	if err != nil {
		logger.Error("agent failed", "error", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
