# kube-pilot

**AI that builds, deploys, and operates software on Kubernetes.**

Open an issue. kube-pilot writes the code, builds the image, deploys it, and verifies it's running. If it crashes, kube-pilot reads the logs, fixes the bug, and redeploys. No human in the loop.

```
You: "Build a Go API that returns server health at /healthz. Deploy it."

kube-pilot:
  1. Writes main.go + Dockerfile
  2. Commits to Gitea
  3. Kicks off a Tekton pipeline → builds image → pushes to registry
  4. Writes k8s manifests → commits to infra repo
  5. ArgoCD deploys it
  6. Curls /healthz → 200 OK
  7. Comments "Done. Service running at health-api.default.svc:8080" → closes issue
```

## Why this exists

AI coding tools generate code. Then you copy it, build it, deploy it, debug it, and iterate manually. The feedback loop is broken.

kube-pilot closes the loop. It has everything it needs inside the cluster: a git server, a container registry, a CI system, a deployment pipeline, and `kubectl`. It writes code, ships it, watches it run, and fixes it when it breaks. The cluster is the AI's development environment.

## What's in the box

One `helm install` gives you the entire platform:

| Component | What it does |
|-----------|-------------|
| **kube-pilot** | AI agent — receives tasks, calls LLM, executes tools |
| **Gitea** | Git server + container registry (no GitHub/DockerHub dependency) |
| **Tekton** | CI/CD pipelines as Kubernetes CRDs |
| **ArgoCD** | GitOps — syncs git repos to the cluster |
| **Vault** | Secrets storage |
| **External Secrets** | Syncs Vault secrets into Kubernetes |

Everything runs in-cluster. No external dependencies. No public URLs. No SaaS accounts.

## Quick start

### Prerequisites

- A Kubernetes cluster (k3s, kind, EKS, GKE — anything)
- `helm` and `kubectl`
- An LLM API key (Claude, OpenAI, or any OpenAI-compatible endpoint)

### Install

```bash
helm install kube-pilot ./charts/kube-pilot \
  --namespace kube-pilot --create-namespace \
  --set llm.apiKey="$ANTHROPIC_API_KEY" \
  --set gitea.gitea.admin.password="your-password"
```

That's it. kube-pilot creates its own git repos, registers its own webhooks, and starts listening for issues.

### Give it a task

Port-forward to Gitea:

```bash
kubectl port-forward svc/kube-pilot-gitea-http -n kube-pilot 3000:3000
```

Open `localhost:3000`, go to `kube-pilot/infra`, create an issue with the `kube-pilot` label:

> **Title:** Deploy a hello-world web server
>
> **Body:** Build a simple Go HTTP server that responds with "hello world" on port 8080. Deploy it to the default namespace.

kube-pilot picks it up, does the work, comments with a summary, and closes the issue.

## How it works

```
                    ┌─────────────┐
                    │  Gitea      │
                    │  (issues)   │
                    └──────┬──────┘
                           │ webhook
                           v
                    ┌─────────────┐
                    │  kube-pilot │◄──── LLM (Claude/GPT/Ollama)
                    └──┬───┬───┬──┘
                       │   │   │
              ┌────────┘   │   └────────┐
              v            v            v
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │  Gitea   │ │  Tekton  │ │ kubectl  │
        │  (git +  │ │  (build) │ │ (read)   │
        │ registry)│ │          │ │          │
        └────┬─────┘ └──────────┘ └──────────┘
             │
             v
        ┌──────────┐
        │  ArgoCD  │──── syncs ───► cluster
        └──────────┘
```

1. **Issue created** in Gitea (or GitHub) with `kube-pilot` label
2. **Webhook fires** to kube-pilot
3. **LLM decides** what to do — writes code, runs commands, iterates
4. **Code committed** to Gitea repos
5. **Tekton builds** container images, pushes to Gitea's registry
6. **ArgoCD syncs** manifests to the cluster
7. **kube-pilot verifies** the deployment, posts results, closes the issue

### Safety model

kube-pilot is **read-only** against the cluster. It can `kubectl get` and `kubectl logs`, but it cannot `kubectl apply`. All mutations go through git, which means:

- Every change is auditable (git history)
- Every change is reversible (git revert)
- ArgoCD is the only thing that writes to the cluster
- The one exception: Tekton PipelineRuns (CI jobs), which kube-pilot creates directly

## Configuration

### LLM provider

kube-pilot works with any OpenAI-compatible API:

```yaml
llm:
  provider: anthropic          # or openai, ollama
  baseURL: https://api.anthropic.com
  model: claude-sonnet-4-6
  apiKey: ${LLM_API_KEY}
```

### Git provider

Bundled Gitea (default) or external GitHub:

```yaml
git:
  provider: gitea    # or github

# GitHub mode (polling, no public URL needed):
github:
  mode: poll
  pollInterval: 30s
  repos:
    - your-org/your-repo
```

### Toggle components

Everything is optional:

```yaml
gitea:
  enabled: true       # Bundled git + registry
vault:
  enabled: true       # Secrets management
argocd:
  enabled: true       # GitOps deployments
tekton:
  enabled: true       # CI/CD pipelines
externalSecrets:
  enabled: true       # Vault → k8s secret sync
externalDNS:
  enabled: false      # DNS automation
```

## Model agnostic

kube-pilot talks to any OpenAI-compatible endpoint. Tested with:

- **Claude** (Anthropic) — via OpenAI compatibility layer
- **GPT-4** (OpenAI)
- **Ollama** (local models) — run the LLM on the same cluster

For Ollama, set `baseURL: http://ollama.default.svc:11434` and pick your model.

## Roadmap

- [ ] GitHub polling mode (no webhook/public URL needed)
- [ ] Crossplane for provisioning cloud resources
- [ ] Multi-cluster (hub manages spoke clusters)
- [ ] Self-management (kube-pilot upgrades itself via ArgoCD)
- [ ] Web UI for task history and observability
- [ ] Guardrails and approval workflows for production changes

## License

MIT
