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
//        kube-pilot-cli --config config.yaml --dump-prompt
func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	dumpPrompt := flag.Bool("dump-prompt", false, "Print the assembled system prompt and exit (no LLM call)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Build agent options from config (mirrors webhook.go createAgent logic)
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

	var opts []agent.Option
	if cfg.Ingress.Enabled {
		opts = append(opts, agent.WithIngressConfig(&cfg.Ingress))
	}
	if cfg.Observability.Enabled {
		opts = append(opts, agent.WithObservabilityConfig(&cfg.Observability))
	}
	if cfg.Crossplane.Enabled {
		opts = append(opts, agent.WithCrossplaneConfig(&cfg.Crossplane))
	}
	if cfg.Agent.SystemPrompt != "" {
		opts = append(opts, agent.WithSystemPrompt(cfg.Agent.SystemPrompt))
	}

	a := agent.New(nil, gitea, giteaInfo, logger, opts...)
	defer a.Cleanup()

	if *dumpPrompt {
		fmt.Println(a.DumpSystemPrompt())
		return
	}

	task := strings.Join(flag.Args(), " ")
	if task == "" {
		fmt.Fprintln(os.Stderr, "Usage: kube-pilot-cli --config config.yaml \"<task>\"")
		fmt.Fprintln(os.Stderr, "       kube-pilot-cli --config config.yaml --dump-prompt")
		os.Exit(1)
	}

	// Need an LLM client for actual task execution
	client := llm.NewOpenAICompat(llm.OpenAICompatConfig{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		Timeout: cfg.LLM.Timeout,
	})

	a = agent.New(client, gitea, giteaInfo, logger, opts...)
	defer a.Cleanup()

	result, err := a.Run(context.Background(), task)
	if err != nil {
		logger.Error("agent failed", "error", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
