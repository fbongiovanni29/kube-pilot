package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	kpctx "github.com/fbongiovanni29/kube-pilot/internal/context"
	"github.com/fbongiovanni29/kube-pilot/internal/llm"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

const systemPromptBase = `You are kube-pilot, an AI that builds, deploys, and operates software on Kubernetes.

You are not just an infrastructure tool. You are a full development platform. You can:
- Write application code in any language
- Build container images via Tekton pipelines
- Deploy services via git commits that ArgoCD syncs to the cluster
- Debug running services by reading logs, exec-ing into pods, and inspecting state
- Iterate autonomously: if a build fails, fix the code and retry
`

const systemPromptGitea = `
Gitea (git server + container registry):
- URL: %s
- Auth for API calls: curl -u $GITEA_USER:$GITEA_PASSWORD (env vars are pre-set, use them as-is — NEVER try to read or echo these values)
- Clone repos: git clone http://$GITEA_USER:$GITEA_PASSWORD@%s/<owner>/<repo>.git (replace the host from the URL)
- Container registry: %s
- NEVER print, echo, log, or expose $GITEA_PASSWORD in any output
`

const systemPromptSuffix = `
Other tools:
- Tekton: CI/CD — create PipelineRuns and TaskRuns (kubectl apply) to build, test, and push images
- ArgoCD: GitOps — syncs git repos to the cluster automatically
- Vault + ExternalSecrets: secrets management
- kubectl, helm, git, curl, and any CLI tool via the shell

Workflow for building a service:
1. Create a Gitea repo: curl -s -X POST -u $GITEA_USER:$GITEA_PASSWORD -H "Content-Type: application/json" -d '{"name":"<repo>","auto_init":true,"default_branch":"main"}' $GITEA_URL/api/v1/user/repos
2. Clone it, write the code and Dockerfile, commit and push
3. Create a Tekton PipelineRun (kubectl apply) to build and push the image
4. Write Kubernetes manifests (Deployment, Service, etc.), commit to the infra repo
5. ArgoCD detects the change and deploys it
6. Verify the service is running — if not, read logs, fix, and redeploy

Important:
- Use git_comment and git_close_issue tools to interact with issues (don't curl for that)
- For everything else (creating repos, listing repos, etc.), use curl with the Gitea API
- Configure git before committing: git config --global user.email "kube-pilot@local" && git config --global user.name "kube-pilot"
- Environment variables $GITEA_URL, $GITEA_USER, $GITEA_PASSWORD are available in all shell commands

Rules:
- NEVER kubectl apply directly (except Tekton PipelineRuns/TaskRuns)
- ALL cluster changes go through git → ArgoCD
- For secrets, create ExternalSecret resources that reference Vault
- For DNS, create ExternalDNS resources
- Always explain what you're about to do before doing it
- If something fails: read logs, diagnose, fix the code, and retry
- If you can't fix it after 3 attempts, escalate to the user
- NEVER expose credentials in comments, logs, or output

When you're done, comment on the issue with a summary and close it.`

// GiteaInfo holds Gitea connection info for the system prompt.
type GiteaInfo struct {
	URL      string
	User     string
	Password string
}

// Agent runs the tool-calling loop against an LLM.
type Agent struct {
	client         llm.Client
	gitea          *tools.GiteaClient // nil when using github
	giteaInfo      *GiteaInfo
	logger         *slog.Logger
	maxSteps       int
	repoContext    string // content from AGENTS.md
	projectContext string // cross-session insights from context store
	contextStore   *kpctx.Store
}

// New creates a new Agent.
func New(client llm.Client, gitea *tools.GiteaClient, giteaInfo *GiteaInfo, logger *slog.Logger, opts ...Option) *Agent {
	a := &Agent{
		client:    client,
		gitea:     gitea,
		giteaInfo: giteaInfo,
		logger:    logger,
		maxSteps:  50,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Option configures an Agent.
type Option func(*Agent)

// WithRepoContext sets the AGENTS.md content for the agent.
func WithRepoContext(content string) Option {
	return func(a *Agent) { a.repoContext = content }
}

// WithProjectContext sets cross-session insights for the agent.
func WithProjectContext(content string) Option {
	return func(a *Agent) { a.projectContext = content }
}

// WithContextStore gives the agent access to the context store for saving insights.
func WithContextStore(store *kpctx.Store) Option {
	return func(a *Agent) { a.contextStore = store }
}

func (a *Agent) systemPrompt() string {
	prompt := systemPromptBase
	if a.giteaInfo != nil {
		host := strings.TrimPrefix(strings.TrimPrefix(a.giteaInfo.URL, "http://"), "https://")
		prompt += fmt.Sprintf(systemPromptGitea, a.giteaInfo.URL, host, a.giteaInfo.URL)
	}
	if a.repoContext != "" {
		prompt += fmt.Sprintf("\n## Repository Context (from AGENTS.md)\n%s\n", a.repoContext)
	}
	if a.projectContext != "" {
		prompt += fmt.Sprintf("\n## Prior Insights (from previous runs)\n%s\n", a.projectContext)
	}
	prompt += systemPromptSuffix
	if a.contextStore != nil {
		prompt += "\n\nBefore closing an issue, save any insights about this repo's patterns, conventions, or failure modes using save_insight."
	}
	return prompt
}

// toolDefs returns the tool definitions for the LLM.
func (a *Agent) toolDefs() []llm.Tool {
	defs := []llm.Tool{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "exec",
				Description: "Execute a shell command. Use for kubectl, git, helm, curl, or any CLI tool.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{
							"type":        "string",
							"description": "The shell command to execute",
						},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "git_comment",
				Description: "Post a comment on an issue in the git provider (GitHub or Gitea).",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"repo": map[string]interface{}{
							"type":        "string",
							"description": "Repository in owner/name format",
						},
						"issue_number": map[string]interface{}{
							"type":        "integer",
							"description": "Issue number",
						},
						"body": map[string]interface{}{
							"type":        "string",
							"description": "Comment body (markdown)",
						},
					},
					"required": []string{"repo", "issue_number", "body"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "git_close_issue",
				Description: "Close an issue in the git provider (GitHub or Gitea).",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"repo": map[string]interface{}{
							"type":        "string",
							"description": "Repository in owner/name format",
						},
						"issue_number": map[string]interface{}{
							"type":        "integer",
							"description": "Issue number",
						},
					},
					"required": []string{"repo", "issue_number"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "read_file",
				Description: "Read a file from a git repository.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"repo": map[string]interface{}{
							"type":        "string",
							"description": "Repository in owner/name format",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "File path within the repo",
						},
						"ref": map[string]interface{}{
							"type":        "string",
							"description": "Branch or commit ref (default: main)",
						},
					},
					"required": []string{"repo", "path"},
				},
			},
		},
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "create_pr",
				Description: "Create a pull request in a git repository.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"repo": map[string]interface{}{
							"type":        "string",
							"description": "Repository in owner/name format",
						},
						"title": map[string]interface{}{
							"type":        "string",
							"description": "Pull request title",
						},
						"body": map[string]interface{}{
							"type":        "string",
							"description": "Pull request description (markdown)",
						},
						"head": map[string]interface{}{
							"type":        "string",
							"description": "Source branch",
						},
						"base": map[string]interface{}{
							"type":        "string",
							"description": "Target branch (default: main)",
						},
					},
					"required": []string{"repo", "title", "head"},
				},
			},
		},
	}

	// Add context store tools if available
	if a.contextStore != nil {
		defs = append(defs,
			llm.Tool{
				Type: "function",
				Function: llm.ToolFunction{
					Name:        "link_initiative",
					Description: "Link a resource (issue, PR, Jira ticket, Slack thread) to a named initiative for cross-tool correlation.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"initiative": map[string]interface{}{
								"type":        "string",
								"description": "Initiative name (e.g. migrate-to-postgres)",
							},
							"resource_type": map[string]interface{}{
								"type":        "string",
								"description": "Resource type",
								"enum":        []string{"issue", "pr", "jira", "slack"},
							},
							"ref": map[string]interface{}{
								"type":        "string",
								"description": "Resource reference (e.g. myorg/api-service#15). Use for issues and PRs.",
							},
							"url": map[string]interface{}{
								"type":        "string",
								"description": "Resource URL. Use for Jira tickets and Slack threads.",
							},
						},
						"required": []string{"initiative", "resource_type"},
					},
				},
			},
			llm.Tool{
				Type: "function",
				Function: llm.ToolFunction{
					Name:        "save_insight",
					Description: "Save a learned insight about a repo's patterns, conventions, or failure modes for future reference.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"repo": map[string]interface{}{
								"type":        "string",
								"description": "Repository in owner/name format",
							},
							"category": map[string]interface{}{
								"type":        "string",
								"description": "Insight category: pattern, deployment, failure, or convention",
								"enum":        []string{"pattern", "deployment", "failure", "convention"},
							},
							"content": map[string]interface{}{
								"type":        "string",
								"description": "The insight to save",
							},
						},
						"required": []string{"repo", "category", "content"},
					},
				},
			},
			llm.Tool{
				Type: "function",
				Function: llm.ToolFunction{
					Name:        "read_context",
					Description: "Read stored insights about a repo from previous agent runs.",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"repo": map[string]interface{}{
								"type":        "string",
								"description": "Repository in owner/name format",
							},
						},
						"required": []string{"repo"},
					},
				},
			},
		)
	}

	return defs
}

// Run executes the agent loop for a given task.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: a.systemPrompt()},
		{Role: llm.RoleUser, Content: task},
	}

	defs := a.toolDefs()

	for step := 0; step < a.maxSteps; step++ {
		a.logger.Info("agent step", "step", step+1)

		resp, err := a.client.Chat(ctx, messages, defs)
		if err != nil {
			return "", fmt.Errorf("llm chat (step %d): %w", step+1, err)
		}

		// If no tool calls, the agent is done
		if len(resp.ToolCalls) == 0 {
			a.logger.Info("agent completed", "steps", step+1)
			return resp.Content, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			result, err := a.executeTool(ctx, tc)
			if err != nil {
				result = fmt.Sprintf("Error: %s", err.Error())
			}

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	return "", fmt.Errorf("agent exceeded max steps (%d)", a.maxSteps)
}

func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall) (string, error) {
	switch tc.Function.Name {
	case "exec":
		return a.execShell(ctx, tc.Function.Arguments)
	case "git_comment":
		return a.execGitComment(ctx, tc.Function.Arguments)
	case "git_close_issue":
		return a.execGitCloseIssue(ctx, tc.Function.Arguments)
	case "read_file":
		return a.execReadFile(ctx, tc.Function.Arguments)
	case "create_pr":
		return a.execCreatePR(ctx, tc.Function.Arguments)
	case "link_initiative":
		return a.execLinkInitiative(ctx, tc.Function.Arguments)
	case "save_insight":
		return a.execSaveInsight(ctx, tc.Function.Arguments)
	case "read_context":
		return a.execReadContext(ctx, tc.Function.Arguments)
	// Keep backwards compat with old tool names
	case "github_comment":
		return a.execGitComment(ctx, tc.Function.Arguments)
	case "github_close_issue":
		return a.execGitCloseIssue(ctx, tc.Function.Arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", tc.Function.Name)
	}
}

func (a *Agent) execShell(ctx context.Context, args string) (string, error) {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse exec args: %w", err)
	}

	a.logger.Info("exec", "command", params.Command)

	// Inject Gitea env vars so the agent can use $GITEA_URL, $GITEA_USER, $GITEA_PASSWORD
	cmd := params.Command
	if a.giteaInfo != nil {
		prefix := fmt.Sprintf("export GITEA_URL=%s GITEA_USER=%s GITEA_PASSWORD=%s; ",
			shellQuote(a.giteaInfo.URL),
			shellQuote(a.giteaInfo.User),
			shellQuote(a.giteaInfo.Password))
		cmd = prefix + cmd
	}

	result, err := tools.Shell(ctx, cmd)
	if err != nil {
		return "", err
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

func shellQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (a *Agent) execGitComment(ctx context.Context, args string) (string, error) {
	var params struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse git_comment args: %w", err)
	}

	a.logger.Info("git_comment", "repo", params.Repo, "issue", params.IssueNumber)

	// Use Gitea API if available, otherwise fall back to GitHub CLI
	if a.gitea != nil {
		parts := strings.SplitN(params.Repo, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid repo format: %s", params.Repo)
		}
		err := a.gitea.Comment(ctx, parts[0], parts[1], params.IssueNumber, params.Body)
		if err != nil {
			return "", err
		}
		return "Comment posted successfully.", nil
	}

	err := tools.GitHubComment(ctx, params.Repo, params.IssueNumber, params.Body)
	if err != nil {
		return "", err
	}
	return "Comment posted successfully.", nil
}

func (a *Agent) execGitCloseIssue(ctx context.Context, args string) (string, error) {
	var params struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse git_close_issue args: %w", err)
	}

	a.logger.Info("git_close_issue", "repo", params.Repo, "issue", params.IssueNumber)

	if a.gitea != nil {
		parts := strings.SplitN(params.Repo, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid repo format: %s", params.Repo)
		}
		err := a.gitea.CloseIssue(ctx, parts[0], parts[1], params.IssueNumber)
		if err != nil {
			return "", err
		}
		return "Issue closed successfully.", nil
	}

	err := tools.GitHubCloseIssue(ctx, params.Repo, params.IssueNumber)
	if err != nil {
		return "", err
	}
	return "Issue closed successfully.", nil
}

func (a *Agent) execReadFile(ctx context.Context, args string) (string, error) {
	var params struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Ref  string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse read_file args: %w", err)
	}

	a.logger.Info("read_file", "repo", params.Repo, "path", params.Path, "ref", params.Ref)

	if a.gitea != nil {
		parts := strings.SplitN(params.Repo, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid repo format: %s", params.Repo)
		}
		content, err := a.gitea.GetFileContent(ctx, parts[0], parts[1], params.Path, params.Ref)
		if err != nil {
			return "", err
		}
		if content == "" {
			return "File not found.", nil
		}
		return content, nil
	}

	content, err := tools.GitHubGetFileContent(ctx, params.Repo, params.Path, params.Ref)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "File not found.", nil
	}
	return content, nil
}

func (a *Agent) execCreatePR(ctx context.Context, args string) (string, error) {
	var params struct {
		Repo  string `json:"repo"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse create_pr args: %w", err)
	}
	if params.Base == "" {
		params.Base = "main"
	}

	a.logger.Info("create_pr", "repo", params.Repo, "title", params.Title, "head", params.Head, "base", params.Base)

	if a.gitea != nil {
		parts := strings.SplitN(params.Repo, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid repo format: %s", params.Repo)
		}
		err := a.gitea.CreatePullRequest(ctx, parts[0], parts[1], params.Title, params.Body, params.Head, params.Base)
		if err != nil {
			return "", err
		}
		return "Pull request created successfully.", nil
	}

	err := tools.GitHubCreatePullRequest(ctx, params.Repo, params.Title, params.Body, params.Head, params.Base)
	if err != nil {
		return "", err
	}
	return "Pull request created successfully.", nil
}

func (a *Agent) execSaveInsight(ctx context.Context, args string) (string, error) {
	if a.contextStore == nil {
		return "Context store not configured.", nil
	}

	var params struct {
		Repo     string `json:"repo"`
		Category string `json:"category"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse save_insight args: %w", err)
	}

	a.logger.Info("save_insight", "repo", params.Repo, "category", params.Category)

	// issueRef is extracted from the task context by the agent; empty here
	err := a.contextStore.AddInsight(ctx, params.Repo, params.Category, params.Content, "")
	if err != nil {
		return "", err
	}
	return "Insight saved.", nil
}

func (a *Agent) execLinkInitiative(ctx context.Context, args string) (string, error) {
	if a.contextStore == nil {
		return "Context store not configured.", nil
	}

	var params struct {
		Initiative   string `json:"initiative"`
		ResourceType string `json:"resource_type"`
		Ref          string `json:"ref"`
		URL          string `json:"url"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse link_initiative args: %w", err)
	}

	a.logger.Info("link_initiative", "initiative", params.Initiative, "type", params.ResourceType)

	resource := kpctx.Resource{
		Type: params.ResourceType,
		Ref:  params.Ref,
		URL:  params.URL,
	}
	err := a.contextStore.LinkInitiative(ctx, params.Initiative, resource)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Resource linked to initiative %q.", params.Initiative), nil
}

func (a *Agent) execReadContext(ctx context.Context, args string) (string, error) {
	if a.contextStore == nil {
		return "Context store not configured.", nil
	}

	var params struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("parse read_context args: %w", err)
	}

	a.logger.Info("read_context", "repo", params.Repo)

	rc, err := a.contextStore.LoadRepoContext(ctx, params.Repo)
	if err != nil {
		return "", err
	}

	if len(rc.Insights) == 0 {
		return "No stored insights for this repo.", nil
	}

	data, _ := json.MarshalIndent(rc.Insights, "", "  ")
	return string(data), nil
}
