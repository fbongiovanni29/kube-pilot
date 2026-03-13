package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/fbongiovanni29/kube-pilot/internal/agent"
	"github.com/fbongiovanni29/kube-pilot/internal/config"
	kpctx "github.com/fbongiovanni29/kube-pilot/internal/context"
	"github.com/fbongiovanni29/kube-pilot/internal/llm"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

// WebhookHandler handles webhook events from GitHub or Gitea.
type WebhookHandler struct {
	cfg          *config.Config
	client       llm.Client
	gitea        *tools.GiteaClient // nil when using github
	contextStore *kpctx.Store       // nil when context is disabled
	logger       *slog.Logger
}

// NewWebhookHandler creates a new webhook handler.
func NewWebhookHandler(cfg *config.Config, client llm.Client, gitea *tools.GiteaClient, logger *slog.Logger) *WebhookHandler {
	h := &WebhookHandler{
		cfg:    cfg,
		client: client,
		gitea:  gitea,
		logger: logger,
	}

	// Initialize context store if enabled
	if cfg.Context.Enabled && cfg.Context.Repo != "" && gitea != nil {
		h.contextStore = kpctx.NewStore(gitea, cfg.Context.Repo)
		logger.Info("context store enabled", "repo", cfg.Context.Repo)
	}

	return h
}

type ghLabel struct {
	Name string `json:"name"`
}

// issueEvent represents a GitHub/Gitea issue webhook payload.
type issueEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number int       `json:"number"`
		Title  string    `json:"title"`
		Body   string    `json:"body"`
		State  string    `json:"state"`
		Labels []ghLabel `json:"labels"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// issueCommentEvent represents a GitHub/Gitea issue comment webhook payload.
type issueCommentEvent struct {
	Action  string `json:"action"`
	Comment struct {
		Body string `json:"body"`
		User struct {
			Login    string `json:"login"`
			Username string `json:"username"` // Gitea uses username
		} `json:"user"`
	} `json:"comment"`
	Issue struct {
		Number int       `json:"number"`
		Title  string    `json:"title"`
		Body   string    `json:"body"`
		Labels []ghLabel `json:"labels"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// ServeHTTP handles incoming webhook requests.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify signature — works for both GitHub (X-Hub-Signature-256) and Gitea (same header)
	secret := h.webhookSecret()
	if secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			// Gitea also uses X-Gitea-Signature
			sig = "sha256=" + r.Header.Get("X-Gitea-Signature")
		}
		if !verifySignature(body, sig, secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Detect event type — GitHub uses X-GitHub-Event, Gitea uses X-Gitea-Event
	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		event = r.Header.Get("X-Gitea-Event")
	}

	h.logger.Info("webhook received", "event", event, "provider", h.cfg.Git.Provider)

	switch event {
	case "issues":
		h.handleIssueEvent(body)
	case "issue_comment":
		h.handleIssueCommentEvent(body)
	default:
		h.logger.Debug("ignoring event", "type", event)
	}

	w.WriteHeader(http.StatusOK)
}

func (h *WebhookHandler) webhookSecret() string {
	if h.cfg.Git.Provider == "gitea" {
		return h.cfg.Gitea.WebhookSecret
	}
	return h.cfg.GitHub.WebhookSecret
}

func (h *WebhookHandler) handleIssueEvent(body []byte) {
	var evt issueEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		h.logger.Error("parse issue event", "error", err)
		return
	}

	h.logger.Info("issue event", "action", evt.Action, "repo", evt.Repository.FullName, "issue", evt.Issue.Number, "labels", len(evt.Issue.Labels))

	// GitHub sends "labeled", Gitea sends "label"
	if evt.Action != "opened" && evt.Action != "labeled" && evt.Action != "label" {
		return
	}

	if !h.isWatchedRepo(evt.Repository.FullName) {
		return
	}

	// Accept both kube-pilot and kube-pilot:plan-first labels
	if !hasLabel(evt.Issue.Labels, "kube-pilot") && !hasLabel(evt.Issue.Labels, "kube-pilot:plan-first") {
		return
	}

	h.logger.Info("processing issue",
		"repo", evt.Repository.FullName,
		"issue", evt.Issue.Number,
		"title", evt.Issue.Title,
	)

	// Determine task suffix based on plan-first label
	taskSuffix := "Handle this issue. When done, comment on the issue with a summary and close it."
	if isPlanFirst(evt.Issue.Labels) {
		taskSuffix = "Analyze this issue and post a plan as a comment. DO NOT execute changes. Only plan.\nPrefix your plan comment with the HTML comment <!-- kube-pilot:plan --> on the first line for machine identification."
	}

	task := fmt.Sprintf(`%s issue #%d in %s

Title: %s

Body:
%s

%s`,
		h.providerName(), evt.Issue.Number, evt.Repository.FullName,
		evt.Issue.Title, evt.Issue.Body,
		taskSuffix,
	)

	go h.runAgent(task, evt.Repository.FullName)
}

func (h *WebhookHandler) handleIssueCommentEvent(body []byte) {
	var evt issueCommentEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		h.logger.Error("parse comment event", "error", err)
		return
	}

	if evt.Action != "created" {
		return
	}

	if !h.isWatchedRepo(evt.Repository.FullName) {
		return
	}

	user := evt.Comment.User.Login
	if user == "" {
		user = evt.Comment.User.Username // Gitea fallback
	}

	// Phase 2: Check for plan-first approval
	if isPlanFirst(evt.Issue.Labels) && isApproval(evt.Comment.Body) {
		h.logger.Info("plan approved",
			"repo", evt.Repository.FullName,
			"issue", evt.Issue.Number,
			"approver", user,
		)

		plan := h.findPlanComment(evt.Repository.FullName, evt.Issue.Number)
		if plan == "" {
			h.logger.Warn("approval received but no plan comment found",
				"repo", evt.Repository.FullName,
				"issue", evt.Issue.Number,
			)
			return
		}

		task := fmt.Sprintf(`%s issue #%d in %s

Title: %s

Body:
%s

Execute the following approved plan. The plan was approved by @%s.

Approved plan:
%s

When done, comment on the issue with a summary and close it.`,
			h.providerName(), evt.Issue.Number, evt.Repository.FullName,
			evt.Issue.Title, evt.Issue.Body,
			user, plan,
		)

		go h.runAgent(task, evt.Repository.FullName)
		return
	}

	// Standard @kube-pilot mention handling
	if !strings.Contains(evt.Comment.Body, "@kube-pilot") {
		return
	}

	h.logger.Info("processing comment",
		"repo", evt.Repository.FullName,
		"issue", evt.Issue.Number,
		"from", user,
	)

	// Get full issue context
	issueDetails := h.getIssueDetails(evt.Repository.FullName, evt.Issue.Number)

	task := fmt.Sprintf(`%s issue #%d in %s

Issue context:
%s

New comment from @%s:
%s

Respond to this comment. If it's a request, handle it. Comment on the issue with your response.`,
		h.providerName(), evt.Issue.Number, evt.Repository.FullName,
		issueDetails,
		user, evt.Comment.Body,
	)

	go h.runAgent(task, evt.Repository.FullName)
}

func (h *WebhookHandler) getIssueDetails(repoFullName string, issueNumber int) string {
	if h.cfg.Git.Provider == "gitea" && h.gitea != nil {
		parts := strings.SplitN(repoFullName, "/", 2)
		if len(parts) == 2 {
			details, err := h.gitea.GetIssue(context.Background(), parts[0], parts[1], issueNumber)
			if err == nil {
				return details
			}
		}
	}
	// Fallback to GitHub CLI
	details, _ := tools.GitHubGetIssue(context.Background(), repoFullName, issueNumber)
	return details
}

func (h *WebhookHandler) providerName() string {
	if h.cfg.Git.Provider == "gitea" {
		return "Gitea"
	}
	return "GitHub"
}

func (h *WebhookHandler) runAgent(task, repoFullName string) {
	var giteaInfo *agent.GiteaInfo
	if h.cfg.Git.Provider == "gitea" {
		giteaInfo = &agent.GiteaInfo{
			URL:      h.cfg.Gitea.URL,
			User:     h.cfg.Gitea.AdminUser,
			Password: h.cfg.Gitea.AdminPassword,
		}
	}

	// Build agent options
	var opts []agent.Option

	// Phase 1: Fetch AGENTS.md from the triggering repo
	repoCtx := h.fetchAgentsFile(repoFullName)
	if repoCtx != "" {
		opts = append(opts, agent.WithRepoContext(repoCtx))
		h.logger.Info("repo context loaded", "repo", repoFullName)
	}

	// Phase 3: Load cross-session context
	if h.contextStore != nil {
		projectCtx := h.loadProjectContext(repoFullName)

		// Phase 4: Append initiative context for this repo
		initiativeCtx := h.loadInitiativeContext(repoFullName)
		if initiativeCtx != "" {
			if projectCtx != "" {
				projectCtx += "\n\n## Related Initiatives\n" + initiativeCtx
			} else {
				projectCtx = "## Related Initiatives\n" + initiativeCtx
			}
		}

		if projectCtx != "" {
			opts = append(opts, agent.WithProjectContext(projectCtx))
			h.logger.Info("project context loaded", "repo", repoFullName)
		}
		opts = append(opts, agent.WithContextStore(h.contextStore))
	}

	a := agent.New(h.client, h.gitea, giteaInfo, h.logger, opts...)
	result, err := a.Run(context.Background(), task)
	if err != nil {
		h.logger.Error("agent failed", "error", err)
		return
	}
	h.logger.Info("agent completed", "result", result)
}

// fetchAgentsFile reads the AGENTS.md (or configured agents_file) from a repo.
// Returns "" if the file doesn't exist or can't be read.
func (h *WebhookHandler) fetchAgentsFile(repoFullName string) string {
	agentsFile := h.cfg.Context.AgentsFile
	if agentsFile == "" {
		agentsFile = "AGENTS.md"
	}

	if h.cfg.Git.Provider == "gitea" && h.gitea != nil {
		parts := strings.SplitN(repoFullName, "/", 2)
		if len(parts) == 2 {
			content, err := h.gitea.GetFileContent(context.Background(), parts[0], parts[1], agentsFile, "")
			if err != nil {
				h.logger.Debug("failed to fetch agents file", "repo", repoFullName, "error", err)
				return ""
			}
			return content
		}
	}

	// GitHub fallback
	content, err := tools.GitHubGetFileContent(context.Background(), repoFullName, agentsFile, "")
	if err != nil {
		h.logger.Debug("failed to fetch agents file", "repo", repoFullName, "error", err)
		return ""
	}
	return content
}

// loadProjectContext loads cross-session insights for a repo.
func (h *WebhookHandler) loadProjectContext(repoFullName string) string {
	if h.contextStore == nil {
		return ""
	}
	rc, err := h.contextStore.LoadRepoContext(context.Background(), repoFullName)
	if err != nil {
		h.logger.Debug("failed to load project context", "repo", repoFullName, "error", err)
		return ""
	}
	if len(rc.Insights) == 0 {
		return ""
	}
	data, _ := json.MarshalIndent(rc.Insights, "", "  ")
	return string(data)
}

// loadInitiativeContext finds initiatives related to a repo and formats them for the system prompt.
func (h *WebhookHandler) loadInitiativeContext(repoFullName string) string {
	if h.contextStore == nil {
		return ""
	}
	initiatives, err := h.contextStore.FindInitiativesForRepo(context.Background(), repoFullName)
	if err != nil {
		h.logger.Debug("failed to load initiative context", "repo", repoFullName, "error", err)
		return ""
	}
	if len(initiatives) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, init := range initiatives {
		sb.WriteString(fmt.Sprintf("### %s\n%s\nResources:\n", init.Name, init.Description))
		for _, r := range init.Resources {
			if r.Ref != "" {
				sb.WriteString(fmt.Sprintf("- [%s] %s\n", r.Type, r.Ref))
			} else if r.URL != "" {
				sb.WriteString(fmt.Sprintf("- [%s] %s\n", r.Type, r.URL))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// isPlanFirst checks if the issue has the kube-pilot:plan-first label.
func isPlanFirst(labels []ghLabel) bool {
	return hasLabel(labels, "kube-pilot:plan-first")
}

// approvalPattern matches common approval phrases after @kube-pilot mention.
var approvalPattern = regexp.MustCompile(`(?i)@kube-pilot\s+(lgtm|approved|approve|go ahead|proceed|ship it|do it)`)

// isApproval checks if a comment body contains an approval command.
func isApproval(body string) bool {
	return approvalPattern.MatchString(body)
}

// findPlanComment scans issue comments for the plan marker and returns the plan content.
func (h *WebhookHandler) findPlanComment(repoFullName string, issueNumber int) string {
	var commentsJSON string

	if h.cfg.Git.Provider == "gitea" && h.gitea != nil {
		parts := strings.SplitN(repoFullName, "/", 2)
		if len(parts) == 2 {
			var err error
			commentsJSON, err = h.gitea.GetIssueComments(context.Background(), parts[0], parts[1], issueNumber)
			if err != nil {
				h.logger.Error("failed to fetch comments for plan", "error", err)
				return ""
			}
		}
	} else {
		var err error
		commentsJSON, err = tools.GitHubGetIssueComments(context.Background(), repoFullName, issueNumber)
		if err != nil {
			h.logger.Error("failed to fetch comments for plan", "error", err)
			return ""
		}
	}

	// Parse comments and find the plan marker
	var comments []struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(commentsJSON), &comments); err != nil {
		h.logger.Error("failed to parse comments", "error", err)
		return ""
	}

	const planMarker = "<!-- kube-pilot:plan -->"
	for i := len(comments) - 1; i >= 0; i-- {
		if strings.Contains(comments[i].Body, planMarker) {
			return comments[i].Body
		}
	}
	return ""
}

func (h *WebhookHandler) isWatchedRepo(repo string) bool {
	// For Gitea, all repos in the Gitea instance are watched
	if h.cfg.Git.Provider == "gitea" {
		return true
	}
	for _, r := range h.cfg.GitHub.Repos {
		if r == repo {
			return true
		}
	}
	return false
}

func hasLabel(labels []ghLabel, name string) bool {
	for _, l := range labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

func verifySignature(payload []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hmac.Equal(sig, mac.Sum(nil))
}
