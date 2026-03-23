package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/fbongiovanni29/kube-pilot/internal/config"
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

const systemPromptGitHub = `
GitHub (source code hosting):
- The source repo is on GitHub. Use the gh CLI and git_comment/git_close_issue tools.
- Clone repos: gh repo clone <owner>/<repo> (GH_TOKEN is pre-set)
- Use gh CLI for API operations: gh api repos/<owner>/<repo>/...
- NEVER print, echo, or expose $GH_TOKEN in any output
`

const systemPromptSuffix = `
Available CLI tools: kubectl, helm, git, curl, gh, argocd (via kubectl), logcli, amtool, and any standard CLI tool.

## Build & Deploy

You have full shell access. Use it to build, deploy, and operate services end-to-end.

Building images:
- Create a Tekton TaskRun with Kaniko to build and push container images
- The container registry is Gitea in-cluster (use --insecure and --skip-tls-verify for Kaniko)
- Registry auth secret "gitea-registry-auth" exists in both kube-pilot and default namespaces
- Poll the TaskRun status with kubectl until it succeeds or fails
- If the build fails, read the logs, fix the issue, and retry

Deploying:
- Infrastructure repo (in Gitea): clone it, add/update manifests in apps/<app-name>/, commit, push
- ArgoCD watches the infra repo and syncs automatically
- To create a new ArgoCD Application: kubectl apply an Application manifest pointing to apps/<app-name>/ in the infra repo
- To force immediate sync: kubectl patch the ArgoCD Application with a sync operation
- Verify with kubectl get pods, kubectl logs, kubectl describe

Rollback:
- Revert the image tag in the infra repo deployment manifest, commit, push
- ArgoCD will sync the rollback automatically

Important:
- Use git_comment and git_close_issue tools to interact with issues (don't curl for that)
- Configure git before committing: git config --global user.email "kube-pilot@local" && git config --global user.name "kube-pilot"

Rules:
- ALL persistent cluster changes go through git → ArgoCD (kubectl apply is fine for one-shot Tekton TaskRuns)
- For secrets, create ExternalSecret resources that reference Vault
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
	ingressConfig       *config.IngressConfig
	observabilityConfig *config.ObservabilityConfig
	crossplaneConfig    *config.CrossplaneConfig
	inbox               chan string // mid-flight messages injected between steps
	workDir        string     // unique temp directory for this agent's shell commands
}

// New creates a new Agent.
func New(client llm.Client, gitea *tools.GiteaClient, giteaInfo *GiteaInfo, logger *slog.Logger, opts ...Option) *Agent {
	// Create a unique temp directory for this agent's shell commands
	// to prevent working directory collisions between concurrent agents.
	workDir, err := os.MkdirTemp("", "kube-pilot-agent-*")
	if err != nil {
		logger.Error("failed to create agent workdir, using default", "error", err)
		workDir = ""
	}

	a := &Agent{
		client:    client,
		gitea:     gitea,
		giteaInfo: giteaInfo,
		logger:    logger,
		maxSteps:  75,
		inbox:     make(chan string, 10),
		workDir:   workDir,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Cleanup removes the agent's temporary working directory.
func (a *Agent) Cleanup() {
	if a.workDir != "" {
		os.RemoveAll(a.workDir)
	}
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

// WithIngressConfig tells the agent how to expose services via Ingress.
func WithIngressConfig(cfg *config.IngressConfig) Option {
	return func(a *Agent) { a.ingressConfig = cfg }
}

// WithObservabilityConfig tells the agent how to query metrics, logs, and alerts.
func WithObservabilityConfig(cfg *config.ObservabilityConfig) Option {
	return func(a *Agent) { a.observabilityConfig = cfg }
}

// WithCrossplaneConfig tells the agent how to provision cloud infrastructure via Crossplane.
func WithCrossplaneConfig(cfg *config.CrossplaneConfig) Option {
	return func(a *Agent) { a.crossplaneConfig = cfg }
}

// knownSecrets returns secret values that should be scrubbed from public output.
func (a *Agent) knownSecrets() []string {
	var secrets []string
	if a.giteaInfo != nil {
		secrets = append(secrets, a.giteaInfo.Password)
	}
	// Also check environment for any leaked values
	for _, env := range []string{"GITEA_PASSWORD", "GITHUB_TOKEN", "GH_TOKEN", "API_KEY", "LLM_API_KEY", "GRAFANA_API_KEY"} {
		if v := os.Getenv(env); v != "" {
			secrets = append(secrets, v)
		}
	}
	return secrets
}

// Inject sends a message into a running agent's conversation.
// The message will be picked up between steps and added as a user message.
// Safe to call from any goroutine. Non-blocking (drops if inbox is full).
func (a *Agent) Inject(msg string) {
	select {
	case a.inbox <- msg:
	default:
		a.logger.Warn("agent inbox full, message dropped")
	}
}


func (a *Agent) systemPrompt() string {
	prompt := systemPromptBase
	if a.giteaInfo != nil {
		host := strings.TrimPrefix(strings.TrimPrefix(a.giteaInfo.URL, "http://"), "https://")
		prompt += fmt.Sprintf(systemPromptGitea, a.giteaInfo.URL, host, a.giteaInfo.URL)
	} else {
		prompt += systemPromptGitHub
	}
	if a.repoContext != "" {
		prompt += fmt.Sprintf("\n## Repository Context (from AGENTS.md)\n%s\n", a.repoContext)
	}
	if a.projectContext != "" {
		prompt += fmt.Sprintf("\n## Prior Insights (from previous runs)\n%s\n", a.projectContext)
	}
	if a.ingressConfig != nil && a.ingressConfig.Enabled {
		prompt += fmt.Sprintf(`
## Ingress & DNS

Services should be exposed externally via Ingress resources. When deploying a service:
- Create an Ingress resource in the app's manifests (apps/<app-name>/ingress.yaml)
- Use ingressClassName: %s
- Services get a hostname of <service-name>.%s
- ExternalDNS will automatically create DNS records from Ingress resources
`, a.ingressConfig.ClassName, a.ingressConfig.Domain)
		if a.ingressConfig.TLSEnabled && a.ingressConfig.ClusterIssuer != "" {
			prompt += fmt.Sprintf(`- Enable TLS: add the annotation cert-manager.io/cluster-issuer: %s
- Add a tls block with the host and a secretName of <service-name>-tls
`, a.ingressConfig.ClusterIssuer)
		}
	}
	if a.observabilityConfig != nil && a.observabilityConfig.Enabled {
		prompt += `
## Observability

The cluster has monitoring and logging infrastructure. Use these tools proactively:
- When **investigating failures**: check Loki logs first (fastest signal), then Prometheus metrics for broader patterns
- When **deploying a service**: verify it's emitting metrics and logs after deploy
- When **debugging performance**: query Prometheus for CPU, memory, and request latency
- When **creating alerts**: write PrometheusRule CRs (committed through git like everything else)
`

		if a.observabilityConfig.Prometheus.URL != "" {
			prompt += fmt.Sprintf(`
### Prometheus (metrics)
Endpoint: %s

Instant query (current value):
  curl -s '%s/api/v1/query' --data-urlencode 'query=up{namespace="default"}'

Range query (over time):
  curl -s '%s/api/v1/query_range' --data-urlencode 'query=rate(http_requests_total[5m])' --data-urlencode 'start=2024-01-01T00:00:00Z' --data-urlencode 'end=2024-01-01T01:00:00Z' --data-urlencode 'step=60s'

Useful PromQL patterns:
- Pod CPU usage: sum(rate(container_cpu_usage_seconds_total{namespace="<ns>",pod=~"<app>.*"}[5m])) by (pod)
- Pod memory: container_memory_working_set_bytes{namespace="<ns>",pod=~"<app>.*"}
- Pod restarts: kube_pod_container_status_restarts_total{namespace="<ns>"}
- HTTP request rate: sum(rate(http_requests_total{namespace="<ns>"}[5m])) by (service)
- HTTP error rate: sum(rate(http_requests_total{code=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))
- Available targets: curl -s '%s/api/v1/targets' | jq '.data.activeTargets[] | {job: .labels.job, health: .health}'

Creating alert rules — commit a PrometheusRule CR to the infra repo:
  apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: <app>-alerts
    labels:
      release: kube-pilot   # must match Prometheus label selector
  spec:
    groups:
    - name: <app>.rules
      rules:
      - alert: HighErrorRate
        expr: sum(rate(http_requests_total{code=~"5..",namespace="<ns>"}[5m])) / sum(rate(http_requests_total{namespace="<ns>"}[5m])) > 0.05
        for: 5m
        labels:
          severity: warning
          kube-pilot: "true"   # add this label to route alerts to kube-pilot for auto-investigation
        annotations:
          summary: "High error rate in <app>"
          description: "Error rate is {{ $value | humanizePercentage }} over the last 5 minutes"
`, a.observabilityConfig.Prometheus.URL, a.observabilityConfig.Prometheus.URL, a.observabilityConfig.Prometheus.URL, a.observabilityConfig.Prometheus.URL)
		}

		if a.observabilityConfig.Loki.URL != "" {
			prompt += fmt.Sprintf(`
### Loki (logs)
Endpoint: %s

Query recent logs:
  logcli --addr=%s query '{namespace="<ns>",pod=~"<app>.*"}' --limit=100 --since=1h

Search for errors:
  logcli --addr=%s query '{namespace="<ns>"} |= "error"' --limit=50 --since=1h

Query with time range:
  logcli --addr=%s query '{namespace="<ns>"}' --from="2024-01-01T00:00:00Z" --to="2024-01-01T01:00:00Z"

Tail live logs:
  logcli --addr=%s query '{namespace="<ns>"}' --tail

Parse JSON logs and filter:
  logcli --addr=%s query '{namespace="<ns>"} | json | level="error"' --limit=50

Discover available labels:
  logcli --addr=%s labels
  logcli --addr=%s labels namespace

LogQL tips:
- Stream selectors: {namespace="default", container="app"}
- Line filter: |= "error"  (contains), != "debug"  (excludes), |~ "err|warn"  (regex)
- JSON parsing: | json | status_code >= 500
- Log volume: sum(count_over_time({namespace="<ns>"}[5m])) by (pod)
`, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL, a.observabilityConfig.Loki.URL)
		}

		if a.observabilityConfig.Alertmanager.URL != "" {
			prompt += fmt.Sprintf(`
### Alertmanager (alerts)
Endpoint: %s

List all active alerts:
  amtool --alertmanager.url=%s alert

List alerts filtered by label:
  amtool --alertmanager.url=%s alert query alertname=HighErrorRate

Silence an alert (suppress notifications while investigating):
  amtool --alertmanager.url=%s silence add alertname=<name> --duration=1h --comment="investigating — kube-pilot"

List active silences:
  amtool --alertmanager.url=%s silence query

Remove a silence:
  amtool --alertmanager.url=%s silence expire <silence-id>

Check alert routing (debug which receiver an alert matches):
  amtool --alertmanager.url=%s config routes test alertname=HighErrorRate severity=critical
`, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL, a.observabilityConfig.Alertmanager.URL)
		}

		if a.observabilityConfig.Grafana.URL != "" {
			prompt += fmt.Sprintf(`
### Grafana (dashboards)
Endpoint: %s

Discover datasources (need UIDs for dashboard panels):
  curl -s -H 'Authorization: Bearer $GRAFANA_API_KEY' %s/api/datasources | jq '.[] | {name, type, uid}'

List existing dashboards:
  curl -s -H 'Authorization: Bearer $GRAFANA_API_KEY' %s/api/search | jq '.[] | {title, uid, url}'

Get a dashboard by UID:
  curl -s -H 'Authorization: Bearer $GRAFANA_API_KEY' %s/api/dashboards/uid/<uid>

Create or update a dashboard:
  curl -s -X POST -H 'Authorization: Bearer $GRAFANA_API_KEY' -H 'Content-Type: application/json' \
    %s/api/dashboards/db -d '{
      "dashboard": {
        "title": "My App Overview",
        "panels": [
          {
            "title": "Request Rate",
            "type": "timeseries",
            "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0},
            "datasource": {"type": "prometheus", "uid": "<prometheus-datasource-uid>"},
            "targets": [{"expr": "sum(rate(http_requests_total{namespace=\"default\"}[5m]))", "legendFormat": "{{pod}}"}]
          },
          {
            "title": "Error Rate",
            "type": "stat",
            "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0},
            "datasource": {"type": "prometheus", "uid": "<prometheus-datasource-uid>"},
            "targets": [{"expr": "sum(rate(http_requests_total{code=~\"5..\"}[5m])) / sum(rate(http_requests_total[5m]))"}],
            "fieldConfig": {"defaults": {"unit": "percentunit", "thresholds": {"steps": [{"value": 0, "color": "green"}, {"value": 0.01, "color": "yellow"}, {"value": 0.05, "color": "red"}]}}}
          }
        ]
      },
      "overwrite": true
    }'

Dashboard tips:
- Always look up the Prometheus datasource UID first (it's NOT "prometheus" — it's a generated UID)
- Panel types: timeseries (graphs), stat (single number), table, gauge, logs (for Loki)
- For Loki panels, use datasource type "loki" and targets with LogQL queries
- NEVER print, echo, or expose $GRAFANA_API_KEY in any output
`, a.observabilityConfig.Grafana.URL, a.observabilityConfig.Grafana.URL, a.observabilityConfig.Grafana.URL, a.observabilityConfig.Grafana.URL, a.observabilityConfig.Grafana.URL)
		}
	}

	if a.crossplaneConfig != nil && a.crossplaneConfig.Enabled {
		prompt += `
## Crossplane (Cloud Infrastructure)

Crossplane is installed on this cluster. It lets you provision and manage cloud infrastructure (VPCs, databases, clusters, buckets, etc.) using kubectl — no Terraform, no CLI credentials on your machine.

### Providers
Crossplane Providers add support for a cloud (AWS, GCP, Azure, etc.). To install one:
  kubectl apply -f - <<EOF
  apiVersion: pkg.crossplane.io/v1
  kind: Provider
  metadata:
    name: provider-aws-ec2
  spec:
    package: xpkg.upbound.io/upbound/provider-aws-ec2:v1
  EOF

Check installed providers:
  kubectl get providers

Wait for a provider to become healthy:
  kubectl wait provider/provider-aws-ec2 --for=condition=Healthy --timeout=120s

### ProviderConfigs (credential binding)
Each provider needs a ProviderConfig that references a credentials Secret:
  apiVersion: aws.upbound.io/v1beta1
  kind: ProviderConfig
  metadata:
    name: default
  spec:
    credentials:
      source: Secret
      secretRef:
        namespace: crossplane-system
        name: aws-credentials
        key: creds

Credentials MUST come from Vault / ExternalSecret — never put raw keys in manifests.

### Managed Resources
Create cloud resources by applying a manifest:
  apiVersion: ec2.aws.upbound.io/v1beta1
  kind: VPC
  metadata:
    name: my-vpc
  spec:
    forProvider:
      region: us-east-1
      cidrBlock: 10.0.0.0/16

Check status of all managed resources:
  kubectl get managed

Inspect a specific resource:
  kubectl describe vpc.ec2.aws.upbound.io/my-vpc

### Composite Resources (XRDs, Compositions, Claims)
- XRDs define a custom API (e.g. CompositeNetwork)
- Compositions map that API to concrete managed resources
- Claims let namespaced users request infrastructure

List XRDs:          kubectl get xrd
List Compositions:  kubectl get compositions
List Claims:        kubectl get claim --all-namespaces

### Workflow
1. Check installed providers: kubectl get providers
2. Install the needed provider if missing (apply a Provider CR)
3. Ensure a ProviderConfig exists with valid credentials
4. Create the managed resource manifest, commit to git (ArgoCD syncs it)
5. Monitor: kubectl get managed — watch READY and SYNCED conditions
6. Debug: kubectl describe <resource> — check status.conditions and events

### Status patterns
- READY=True, SYNCED=True → resource is provisioned and in sync
- READY=False, SYNCED=True → provider accepted the spec but cloud hasn't finished provisioning (wait)
- SYNCED=False → spec error or credential issue — check kubectl describe and provider pod logs:
    kubectl logs -n crossplane-system -l pkg.crossplane.io/revision -c package-runtime --tail=50

### Rules
- ALL persistent infrastructure goes through git → ArgoCD (same as app manifests)
- Cloud credentials MUST be stored in Vault and injected via ExternalSecret — never in manifests
- Provisioning is async — after applying, poll READY/SYNCED conditions before reporting success
- Do NOT use provider-kubernetes or provider-helm — Crossplane is for non-Kubernetes cloud resources only
`
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
		// Drain inbox — inject any mid-flight messages as user messages
		for {
			select {
			case msg := <-a.inbox:
				a.logger.Info("injecting mid-flight message")
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: fmt.Sprintf("[Update from user while you are working]\n\n%s\n\nAcknowledge this update and adjust your approach if needed.", msg),
				})
			default:
				goto drained
			}
		}
	drained:

		// Compact context if it's getting too large
		before := len(messages)
		messages = compactMessages(messages)
		if len(messages) < before {
			a.logger.Info("compacted context", "before", before, "after", len(messages))
		}

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

	result, err := tools.ShellInDir(ctx, cmd, a.workDir)
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

// credentialPatterns matches common credential patterns that should be redacted.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key|apikey|auth)[\s]*[=:]\s*\S+`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`(?i)(ghp_|gho_|ghu_|ghs_|ghr_)[A-Za-z0-9_]{36,}`),
}

// scrubCredentials redacts potential credentials from text.
func scrubCredentials(text string, extraSecrets []string) string {
	for _, secret := range extraSecrets {
		if secret != "" && len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "***REDACTED***")
		}
	}
	for _, pat := range credentialPatterns {
		text = pat.ReplaceAllString(text, "***REDACTED***")
	}
	return text
}

// maxMessageTokenEstimate gives a rough char count estimate for a message.
// Used for context window management to prevent blowout.
func messageSize(m llm.Message) int {
	n := len(m.Content)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Arguments) + len(tc.Function.Name)
	}
	return n
}

// compactMessages trims the middle of the conversation when it gets too long,
// keeping the system prompt, initial task, and recent messages.
// maxChars is the approximate character budget.
const maxContextChars = 400_000 // ~100k tokens

func compactMessages(messages []llm.Message) []llm.Message {
	total := 0
	for _, m := range messages {
		total += messageSize(m)
	}

	if total <= maxContextChars {
		return messages
	}

	// Keep: system (0), initial task (1), last 20 messages
	keepTail := 20
	if keepTail > len(messages)-2 {
		keepTail = len(messages) - 2
	}

	head := messages[:2] // system + initial task
	tail := messages[len(messages)-keepTail:]

	compacted := make([]llm.Message, 0, len(head)+1+len(tail))
	compacted = append(compacted, head...)
	compacted = append(compacted, llm.Message{
		Role:    llm.RoleUser,
		Content: "[Earlier conversation messages were compacted to save context. Continue from the recent messages below.]",
	})
	compacted = append(compacted, tail...)
	return compacted
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

	// Scrub potential credentials before posting to a public comment
	secrets := a.knownSecrets()
	params.Body = scrubCredentials(params.Body, secrets)

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

	// Scrub credentials from PR title and body
	secrets := a.knownSecrets()
	params.Title = scrubCredentials(params.Title, secrets)
	params.Body = scrubCredentials(params.Body, secrets)

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
