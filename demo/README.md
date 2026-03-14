# CloudDesk Demo — kube-pilot in Action

This demo showcases kube-pilot autonomously building and deploying **CloudDesk**, a multi-repo office productivity suite, entirely from Gitea issues.

You file an issue. kube-pilot reads it, clones the repo, writes or modifies code, builds a container, deploys to Kubernetes, and reports back. No CI/CD pipelines to configure. No Dockerfiles to babysit. Just intent in, running services out.

## Architecture

```
                    ┌──────────────┐
                    │  web-gateway │  :8080
                    │  (router)    │
                    └──┬───────┬───┘
                       │       │
              ┌────────▼──┐ ┌─▼──────────┐
              │  auth-     │ │  docs-api  │  :8082
              │  service   │ │  (CRUD)    │
              │  :8081     │ └────────────┘
              └────────────┘
                    ┌──────────────────────┐
                    │  notifications-worker │
                    │  (background poller)  │
                    └──────────────────────┘
```

| Service                | Purpose                              | Port |
|------------------------|--------------------------------------|------|
| `web-gateway`          | API gateway, routes to backend services | 8080 |
| `auth-service`         | JWT auth — login, verify, logout     | 8081 |
| `docs-api`             | Document CRUD, in-memory store       | 8082 |
| `notifications-worker` | Polls a queue, fires webhook notifications | —    |

## Setup

### 1. Create the Gitea Repos

Create four repos in your Gitea instance under an org (e.g., `clouddesk`):

```bash
repos=("docs-api" "auth-service" "notifications-worker" "web-gateway")
for repo in "${repos[@]}"; do
  curl -X POST "http://gitea.local/api/v1/orgs/clouddesk/repos" \
    -H "Authorization: token $GITEA_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\": \"$repo\", \"auto_init\": false}"
done
```

### 2. Push the Starter Code

Each directory under `demo/repos/` is a ready-to-push Git repo:

```bash
for repo in "${repos[@]}"; do
  cd repos/$repo
  git init && git add -A && git commit -m "initial commit"
  git remote add origin "http://gitea.local/clouddesk/$repo.git"
  git push -u origin main
  cd ../..
done
```

### 3. File Issues and Watch kube-pilot Work

The `demo/issues/` directory contains five sample issues, ordered by complexity. Open them in Gitea by copy-pasting the title and body. kube-pilot will pick them up, plan the work, execute it, and close the issue when done.

Start with issue 01 and work forward:

| Issue | What It Demonstrates |
|-------|---------------------|
| `01-deploy-auth-service.md` | Full build-and-deploy from source |
| `02-add-rate-limiting.md` | Code modification on a running service |
| `03-fix-docs-api-crash.md` | Debugging a broken endpoint |
| `04-add-health-monitoring.md` | Cross-repo coordinated changes |
| `05-scale-and-optimize.md` | Operational tuning — scaling and resource limits |

## What to Watch For

- **Repo awareness**: kube-pilot reads each repo's `AGENTS.md` to understand conventions, build commands, and testing expectations before writing any code.
- **Autonomous builds**: kube-pilot builds container images from the Dockerfile, pushes to the cluster registry, and deploys — no pipeline config needed.
- **Iterative debugging**: When a build or deploy fails, kube-pilot reads logs, diagnoses the issue, and retries with a fix.
- **Issue lifecycle**: kube-pilot comments on the Gitea issue with progress updates, then closes it when the work is verified.

## Resetting the Demo

Delete the namespace and re-push the repos to start fresh:

```bash
kubectl delete namespace clouddesk
for repo in "${repos[@]}"; do
  cd repos/$repo && git push --force origin main && cd ../..
done
```
