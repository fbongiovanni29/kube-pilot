# kube-pilot

## What this repo is

kube-pilot is an AI agent that operates as a remote senior engineer with full Kubernetes cluster access. It receives work via GitHub/Gitea issue webhooks, plans and executes changes, and reports back on the issue.

## Architecture

```
cmd/
  cli/         — One-shot CLI mode (local dev/testing)
  operator/    — Webhook server (production)
internal/
  agent/       — LLM tool-calling loop, system prompt, tool dispatch
  config/      — YAML config with env var expansion
  context/     — Cross-session memory (repo insights + initiative correlation)
  controller/  — Webhook handler (issue/comment routing, plan-first workflow)
  llm/         — OpenAI-compatible LLM client interface
  tools/       — Gitea REST API client, GitHub CLI wrapper, shell executor
charts/        — Helm chart bundling Gitea, ArgoCD, Tekton, Vault, ExternalSecrets
```

## Key patterns

- **Dual-provider**: All git operations work against both Gitea (REST API) and GitHub (`gh` CLI). Check `cfg.Git.Provider` or `a.gitea != nil` to branch.
- **Tool-calling loop**: The agent iterates LLM calls until no tool calls are returned. Tools: `exec`, `git_comment`, `git_close_issue`, `read_file`, `create_pr`, `save_insight`, `read_context`, `link_initiative`.
- **Async webhook handling**: `runAgent()` is called in a goroutine. HTTP response returns immediately.
- **Plan-first workflow**: Issues labeled `kube-pilot:plan-first` get a plan comment (marked with `<!-- kube-pilot:plan -->`) and wait for `@kube-pilot lgtm` approval before executing.
- **Context system**: Agent reads this file (AGENTS.md) before starting work. Cross-session insights stored in the `kube-pilot-context` repo. Initiatives link related Jira/Slack/PR/issue threads.

## Rules

- NEVER `kubectl apply` directly (except Tekton PipelineRuns/TaskRuns). All cluster changes go through git + ArgoCD.
- Secrets go through Vault + ExternalSecrets. Never hardcode credentials.
- Use `git_comment`/`git_close_issue` tools for issue interaction, not curl.
- If a build or deploy fails, read logs, diagnose, fix, and retry. Escalate after 3 attempts.
- Always comment on the issue with a summary before closing.
- Save insights about repo patterns, failure modes, and conventions using `save_insight` before closing issues.

## Testing

```bash
go test ./...
```

All packages have tests. Use `httptest.NewServer` for Gitea API mocks. Agent tests use a `mockClient` implementing `llm.Client`.

## Config

See `config.example.yaml`. Key settings:
- `context.enabled: true` — enables cross-session memory
- `context.repo` — the Gitea repo for storing insights and initiatives
- `context.agents_file` — filename to read from repos (default: `AGENTS.md`)
