![kube-pilot — The engineer that lives in your cluster](docs/banner.png)

# kube-pilot

> **This is a proof of concept.** It works — we've deployed a full multi-service app from scratch on a single k3s node — but it's early. Expect rough edges.

**An autonomous software agent that runs in your Kubernetes cluster with access to all your dev tools.**

There's no UI. You talk to it the way you already work — file a GitHub issue, and it picks it up, writes the code, builds the container, deploys it, verifies it's running, and closes the ticket. If it crashes, it reads the logs, fixes the bug, and redeploys. When an alert fires at 3am, it wakes up, queries the metrics, reads the logs, and fixes the problem before you check your phone. Need a new database or a whole Kubernetes cluster in the cloud? It provisions real infrastructure through Crossplane — VPCs, RDS instances, GKE clusters — all via GitOps, all from a GitHub issue.

```
You (GitHub issue): "Build a Go REST API for document storage. Deploy it to the cluster."

kube-pilot:
  1. Creates the repo, writes main.go + Dockerfile
  2. Commits and pushes to Gitea
  3. Creates a Tekton TaskRun → Kaniko builds the image → pushes to registry
  4. Writes Deployment + Service + Ingress manifests → commits to infra repo
  5. ArgoCD syncs → pods come up → ExternalDNS creates DNS → cert-manager issues TLS
  6. Curls the endpoint → 200 OK
  7. Comments "Done. docs-api running at docs-api.apps.example.com" → closes issue
```

```
Alertmanager fires: "HighPodRestarts — pod hello-world restarting frequently"

kube-pilot:
  1. Checks pod status with kubectl
  2. Queries Loki logs for errors (logcli '{namespace="default"} |= "error"')
  3. Queries Prometheus for restart count, CPU, memory metrics
  4. Reads events, describes the pod, checks for OOMKills
  5. Identifies root cause → fixes the code or config → redeploys
  6. Verifies the alert resolves
```

```
You (GitHub issue): "Provision a GKE Autopilot cluster in us-central1 for the staging environment."

kube-pilot:
  1. Checks installed Crossplane providers → installs provider-gcp-container
  2. Creates ExternalSecret to sync GCP credentials from Vault
  3. Creates ProviderConfig referencing the secret
  4. Writes GKE Cluster manifest → commits to infra repo → ArgoCD syncs
  5. Polls kubectl get managed — watches SYNCED/READY conditions
  6. Cluster comes up in ~9 minutes → READY=True
  7. Comments "Done. GKE cluster kube-pilot-spoke running in us-central1" → closes issue
```

---

## What makes this different

Code generation tools generate code. Then you copy it, build it, deploy it, debug it, and iterate manually. The feedback loop is broken.

kube-pilot closes the loop. It lives inside the cluster with direct access to every tool in your dev stack — git, CI/CD, container registry, deployment pipelines, kubectl, Prometheus, Grafana, Loki. You communicate with it through the tools you already use (GitHub, Slack*, Jira*), and it acts with all the tools in your cluster.

| Code generation tools | kube-pilot |
|----------------------|------------|
| Generates code | Generates code |
| You build it | Builds it (Tekton + Kaniko) |
| You deploy it | Deploys it (git push + ArgoCD) |
| You debug it | Queries Prometheus + Loki, reads logs, fixes, redeploys |
| You verify it | Curls endpoints, checks metrics, verifies alerts clear |
| You close the ticket | Closes the ticket |
| You wake up at 3am | Alertmanager wakes *it* up — it investigates and fixes autonomously |
| You provision infra manually | Provisions VPCs, databases, clusters via Crossplane — from a GitHub issue |

It's not an ops bot. It's not a chatbot with kubectl access. It's an autonomous engineer that happens to live inside your cluster — and it can provision the cloud infrastructure that cluster runs on.

### Why Kubernetes is the perfect reasoning surface

Kubernetes clusters are already built around the primitives that autonomous agents need: declarative state, observable outcomes, and deterministic tooling. Every action has a verifiable result — `kubectl get pods` tells you if the deploy worked, `curl /healthz` tells you if the service is alive, build logs tell you exactly what failed. There's no ambiguity.

This is what makes Kubernetes fundamentally different from a local dev environment as a substrate for automation. The entire dev stack — git, CI/CD, container builds, deployment, networking, observability — is API-addressable and composable. Secrets stay behind Vault where the agent never sees them — it creates ExternalSecret references, not raw credentials. An LLM doesn't need a GUI or IDE. It needs tools that take structured input and return structured output. That's what Kubernetes is.

kube-pilot doesn't bolt automation onto an existing workflow. It uses the cluster itself as the reasoning environment — every tool call produces observable state that feeds back into the next decision. With Crossplane, this extends beyond the cluster boundary — the agent provisions cloud infrastructure (VPCs, databases, entire Kubernetes clusters) using the same kubectl interface, the same GitOps workflow, and the same observable status conditions. The cluster isn't just where code runs. It's where the agent thinks — and from where it builds the rest of your infrastructure.

---

## How it works

```
  Issue opened             kube-pilot                     Cluster
  (GitHub/Gitea/Slack*)    (agent)
       │                       │
       │    webhook            │
       ├──────────────────────►│
       │                       │──── reads AGENTS.md (repo conventions)
       │                       │──── loads prior insights (cross-session memory)
       │                       │
       │                       │──── LLM decides what to do
       │                       │         │
       │                       │         ├── writes code
       │                       │         ├── git commit + push
       │                       │         ├── Tekton builds image
       │                       │         ├── updates infra repo
       │                       │         │
       │                       │         ▼
       │                       │     ArgoCD syncs ──────► Pods running
       │                       │         │
       │                       │         ├── kubectl get pods ✓
       │                       │         ├── curl /healthz ✓
       │                       │         │
       │   "Done" + close      │◄────────┘
       │◄──────────────────────│
```

Triggers can also come from **Alertmanager** — a firing alert (e.g. pod crash loop, high error rate) becomes a task that kube-pilot investigates autonomously. It queries Prometheus metrics, searches Loki logs, checks pod state, identifies the root cause, and fixes it — or escalates if it can't.

For infrastructure tasks, the flow extends to the cloud:

```
  Issue: "Spin up a GKE          kube-pilot                Cloud (GCP/AWS/Azure)
  cluster for staging"
       │                             │
       ├────────────────────────────►│
       │                             │──── installs Crossplane Provider
       │                             │──── creates ExternalSecret (Vault → creds)
       │                             │──── creates ProviderConfig
       │                             │──── writes Cluster manifest → git → ArgoCD
       │                             │         │
       │                             │         ▼
       │                             │     Crossplane ──────► GKE cluster provisioned
       │                             │         │
       │                             │         ├── kubectl get managed ✓
       │                             │         ├── SYNCED=True READY=True
       │                             │         │
       │   "Done" + close            │◄────────┘
       │◄────────────────────────────│
```

### The agent loop

kube-pilot is a tool-calling agent backed by an LLM. It receives a task, decides what tools to call (`exec`, `git_comment`, `read_file`, `create_pr`, etc.), executes them, observes the results, and iterates. If a build fails, it reads the logs and fixes the code. If a deployment crashes, it checks `kubectl describe` and adjusts the manifests. It runs up to 75 steps before giving up.

### What's in the box

One `helm install` gives you:

| Component | Role |
|-----------|------|
| **kube-pilot** | LLM-powered agent — webhook handler, tool-calling loop, shell access |
| **Gitea** | Git server + container registry (no external dependencies) |
| **Tekton** | Container image builds via Kaniko TaskRuns |
| **ArgoCD** | GitOps — watches infra repo, syncs manifests to cluster |
| **Vault** | Secrets storage (optional) |
| **External Secrets** | Vault → Kubernetes secret sync (optional) |
| **Traefik** | Ingress controller — routes external traffic to services (optional) |
| **cert-manager** | Automatic TLS certificates via Let's Encrypt (optional) |
| **ExternalDNS** | Automatic DNS records from Ingress resources (optional) |
| **Prometheus** | Metrics collection — agent can query PromQL (optional) |
| **Grafana** | Dashboards — agent can create/manage via API (optional) |
| **Loki** | Log aggregation — agent can query via logcli (optional) |
| **Alertmanager** | Alert routing — firing alerts become agent tasks (optional) |
| **Crossplane** | Cloud infrastructure provisioning — VPCs, databases, clusters via kubectl (optional) |

Everything runs in-cluster. No public URLs. No SaaS accounts. No Docker Hub. With Crossplane, kube-pilot reaches out to provision cloud infrastructure — but the control plane stays in your cluster.

---

## Features

### Repo-aware context
kube-pilot reads `AGENTS.md` from each repo before starting work. Tell it your conventions, your tech stack, your deployment patterns — it follows them.

```markdown
# AGENTS.md
- This is a Go 1.22 service using Chi router
- Tests run with `go test ./...`
- Container images go to the Gitea registry
- Deploy via ArgoCD, manifests in the infra repo under apps/
```

### Plan-first workflow
Label an issue `kube-pilot:plan-first` and the agent will post a plan as a comment and wait. Reply `@kube-pilot lgtm` to approve, then it executes.

### Cross-session memory
kube-pilot remembers what it learns. If it discovers that a repo needs a specific build flag, or that a service crashes without a particular env var, it saves that insight and uses it next time.

### Mid-flight context injection
If a second comment arrives while the agent is working, it gets injected into the running conversation — the agent adjusts its approach without restarting.

### Alert-driven automation
When Alertmanager fires an alert labeled `kube-pilot: "true"`, kube-pilot receives a webhook and autonomously investigates. It queries Prometheus for metrics, searches Loki for error logs, checks pod state with kubectl, and attempts to fix the issue — all without human intervention. Add the label to any PrometheusRule to opt in.

### Observability-aware debugging
kube-pilot has native access to your monitoring stack. When investigating any issue — whether from a GitHub issue or a firing alert — it can:
- Query **Prometheus** via PromQL for CPU, memory, error rates, and custom metrics
- Search **Loki** logs with `logcli` for errors, stack traces, and patterns
- Check **Alertmanager** for active alerts and manage silences with `amtool`
- Create and update **Grafana** dashboards via the API

### Cloud infrastructure provisioning
kube-pilot can provision and manage real cloud infrastructure — not just deploy apps. With Crossplane enabled, it can create VPCs, databases, Kubernetes clusters, storage buckets, and any other cloud resource — all through kubectl, all through GitOps.

Tested and validated: a single GitHub issue spun up a GKE Autopilot cluster on GCP in ~9 minutes. Credentials never touch a manifest — they flow through Vault → ExternalSecret → Crossplane ProviderConfig. The agent installs the right Crossplane Provider, wires up credentials, creates the resource, and polls until it's ready.

This is the foundation for **hub-and-spoke** — a single k3s node running kube-pilot can provision and manage entire fleets of production clusters across AWS, GCP, and Azure.

### Credential scrubbing
Before posting any comment or PR, kube-pilot scrubs known secrets and common credential patterns. Passwords, tokens, and API keys are redacted before they reach the LLM or any public output.

### Automatic retry with backoff
Rate-limited by the LLM provider? kube-pilot retries with exponential backoff and respects `Retry-After` headers. No dropped tasks.

### Failure recovery
If the agent crashes or hits an error, it posts a failure notice on the issue so nothing is silently orphaned.

---

## Quick start

### Prerequisites

- A Kubernetes cluster (k3s, kind, EKS, GKE — anything with 4GB+ RAM)
- `helm` and `kubectl`
- An LLM API key (Claude, OpenAI, or any OpenAI-compatible endpoint)

### Install

```bash
helm install kube-pilot ./charts/kube-pilot \
  --namespace kube-pilot --create-namespace \
  --set llm.apiKey="$ANTHROPIC_API_KEY" \
  --set gitea.gitea.admin.password="your-password"
```

kube-pilot bootstraps itself: creates git repos, registers webhooks, provisions Grafana API keys, and starts listening. No manual setup.

### Give it a task

```bash
kubectl port-forward svc/kube-pilot-gitea-http -n kube-pilot 3000:3000
```

Open `localhost:3000`, go to any repo, create an issue with the `kube-pilot` label:

> **Title:** Deploy a hello-world web server
>
> **Body:** Build a Go HTTP server that responds with `{"message":"hello"}` on port 8080. Deploy it to the default namespace.

Watch the issue. kube-pilot picks it up, does the work, and closes it when done.

---

## Demo: CloudDesk office suite

We built **CloudDesk** — a multi-service office productivity suite — to demonstrate kube-pilot deploying and managing a real application across multiple repos. The source code and sample issues are in the [`demo/`](demo/) directory.

| Service | What it does |
|---------|-------------|
| **auth-service** | JWT authentication — login, verify, logout |
| **docs-api** | Document storage — CRUD REST API |
| **notifications-worker** | Background job processor — webhook notifications |
| **web-gateway** | API gateway — routes traffic to backend services |

The [`demo/issues/`](demo/issues/) directory has five sample issues that progressively demonstrate what kube-pilot can do — from deploying a service from scratch, to adding rate limiting, to debugging a crashing endpoint, to coordinating changes across all four services.

See [`demo/README.md`](demo/README.md) for the full walkthrough.

---

## Configuration

### LLM provider

Any OpenAI-compatible API:

```yaml
llm:
  provider: anthropic        # or openai, ollama
  baseURL: https://api.anthropic.com
  model: claude-sonnet-4-6
  apiKey: ${LLM_API_KEY}
```

Currently tested with Claude (Anthropic). Should work with any OpenAI-compatible endpoint (GPT-4, Ollama, vLLM). For Ollama, point `baseURL` at your Ollama service and pick a model.

### Git provider

Bundled Gitea (default) or external GitHub:

```yaml
git:
  provider: gitea    # or github

# GitHub mode:
github:
  repos:
    - your-org/your-repo
  webhookSecret: ${WEBHOOK_SECRET}
```

### Cross-session memory

```yaml
context:
  enabled: true
  repo: kube-pilot/kube-pilot-context   # where insights are stored
  agents_file: AGENTS.md                # repo conventions file
```

### Ingress, TLS & DNS

Expose deployed services externally with automatic DNS and TLS:

```yaml
traefik:
  enabled: true

certManager:
  enabled: true
  acmeEmail: you@example.com

externalDNS:
  enabled: true
  provider:
    name: cloudflare      # or aws, google, azure, digitalocean
  domainFilters:
    - apps.example.com
  # Credentials are loaded from the external-dns-credentials secret.
  # If using Vault, store them at kube-pilot/dns:
  #   vault kv put secret/kube-pilot/dns CF_API_TOKEN=xxx        (Cloudflare)
  #   vault kv put secret/kube-pilot/dns AWS_ACCESS_KEY_ID=xxx AWS_SECRET_ACCESS_KEY=xxx  (Route53)

ingress:
  enabled: true
  className: traefik
  host: apps.example.com
  tls: true
  clusterIssuer: letsencrypt-prod
```

When ingress is enabled, kube-pilot automatically creates Ingress resources for deployed services with the pattern `<service-name>.<domain>`. cert-manager provisions TLS certificates via Let's Encrypt, and ExternalDNS creates DNS records — all automatically.

### Observability

Add Prometheus, Grafana, Loki, and Alertmanager for metrics, dashboards, logs, and alert-driven automation:

```yaml
kubePrometheusStack:
  enabled: true

loki:
  enabled: true

observability:
  enabled: true
  # URLs are auto-detected from subcharts. Override to use external services:
  # prometheus:
  #   url: http://my-prometheus:9090
  # grafana:
  #   url: http://my-grafana:3000
  #   apiKey: ${GRAFANA_API_KEY}
  # loki:
  #   url: http://my-loki:3100
  # alertmanager:
  #   url: http://my-alertmanager:9093
```

When observability is enabled:
- The agent can query **Prometheus** (PromQL via curl) and create **PrometheusRule** CRs for new alert rules
- The agent can search **Loki** logs using `logcli`
- The agent can manage **Grafana** dashboards via the API
- **Alertmanager** alerts labeled `kube-pilot: "true"` are routed to the agent's `/alertmanager-webhook` endpoint — firing alerts become autonomous investigation tasks

### Cloud infrastructure (Crossplane)

Provision cloud resources — VPCs, databases, Kubernetes clusters — directly from GitHub issues:

```yaml
crossplane:
  enabled: true
```

That's it. Crossplane installs with sensible defaults. The agent handles the rest:
1. Installs the right Provider for your cloud (AWS, GCP, Azure)
2. Creates a ProviderConfig with credentials from Vault (never in manifests)
3. Provisions resources and monitors them until READY

Store cloud credentials in Vault — the agent creates ExternalSecrets to sync them:
```bash
# GCP
vault kv put secret/crossplane/gcp creds=@gcp-sa-key.json

# AWS
vault kv put secret/crossplane/aws creds="$(printf '[default]\naws_access_key_id=%s\naws_secret_access_key=%s' $KEY $SECRET)"
```

The agent knows how to work with Crossplane Providers, ProviderConfigs, Managed Resources, Composite Resources (XRDs), Compositions, and Claims. It understands READY/SYNCED status conditions, knows where to find provider logs, and follows the same GitOps workflow as everything else — manifests go through git, ArgoCD syncs them.

### Toggle components

Everything is optional:

```yaml
gitea:
  enabled: true        # Bundled git + registry
argocd:
  enabled: true        # GitOps deployments
tekton:
  enabled: true        # CI/CD pipelines
vault:
  enabled: true        # Secrets management
externalSecrets:
  enabled: true        # Vault → k8s secret sync
traefik:
  enabled: false       # Ingress controller
certManager:
  enabled: false       # Automatic TLS certificates
externalDNS:
  enabled: false       # Automatic DNS from Ingress resources
kubePrometheusStack:
  enabled: false       # Prometheus + Grafana + Alertmanager
loki:
  enabled: false       # Log aggregation
crossplane:
  enabled: false       # Cloud infrastructure provisioning
```

---

## Safety model

kube-pilot is **read-only against the cluster** for persistent changes. All mutations go through git:

- Every change is auditable (git history)
- Every change is reversible (git revert)
- ArgoCD is the only thing that writes to the cluster
- Exception: Tekton TaskRuns (CI jobs), created directly by kube-pilot
- Cloud infrastructure changes go through the same GitOps flow — Crossplane manifests are committed to git, ArgoCD syncs them, Crossplane reconciles
- Cloud credentials never appear in manifests — they flow through Vault → ExternalSecret → ProviderConfig
- Credentials are scrubbed before reaching the LLM and before any public output (comments, PRs)
- Bot ignores its own webhook events (no self-triggering loops)

---

## Architecture

```
internal/
├── agent/          # LLM tool-calling loop, system prompt, tool execution
├── bootstrap/      # Zero-config setup — creates repos, webhooks, infra
├── config/         # YAML configuration parsing
├── context/        # Cross-session memory store (Gitea-backed)
├── controller/     # Webhook handler, issue routing, plan-first workflow
├── llm/            # OpenAI-compatible client with retry/backoff
└── tools/          # Gitea client, GitHub client, shell executor
```

**114 unit tests** across all packages. Every feature is tested.

---

## Roadmap

- [x] Autonomous build → deploy → verify loop
- [x] Repo-aware context (AGENTS.md)
- [x] Plan-first approval workflow
- [x] Cross-session memory
- [x] Mid-flight context injection
- [x] Credential scrubbing
- [x] Rate limit retry with backoff
- [x] Failure recovery notifications
- [x] GitHub App auth (comments as bot identity)
- [x] GitHub webhook mode
- [x] Vault + External Secrets for credential management
- [x] Traefik ingress controller
- [x] cert-manager (automatic TLS via Let's Encrypt)
- [x] ExternalDNS (automatic DNS from Ingress resources)

**Integrations:**
- [ ] Slack — receive tasks and post updates in channels
- [ ] Jira — pick up tickets, update status, link PRs
- [x] Alertmanager — firing alerts become agent tasks, kube-pilot investigates and fixes

**Observability:**
- [x] Prometheus — agent queries PromQL for metrics, creates PrometheusRule CRs for alerts
- [x] Grafana — agent creates/manages dashboards via API
- [x] Loki — agent queries logs via logcli
- [ ] Agent-internal metrics — step counts, success/failure rates, LLM latency

**Infrastructure:**
- [x] Crossplane — cloud infrastructure provisioning (VPCs, databases, clusters) via kubectl
- [ ] Multi-environment hub & spoke — central kube-pilot managing dev/staging/prod clusters via Crossplane
- [ ] Web UI for task history and observability
- [ ] Self-management (kube-pilot upgrades itself via ArgoCD)

*\* Planned — not yet implemented*

---

## Development

Most of kube-pilot's codebase was developed using [Claude Code](https://claude.com/claude-code) for budget reasons — running kube-pilot itself for every code change burns LLM tokens. kube-pilot has been tested end-to-end on its own cluster (writing code, building images, deploying services, and closing issues autonomously), but day-to-day development used Claude Code to keep API costs down.

---

## License

MIT
