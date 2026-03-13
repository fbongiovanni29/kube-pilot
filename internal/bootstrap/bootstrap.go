package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fbongiovanni29/kube-pilot/internal/config"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

// Run performs one-time setup of infrastructure that kube-pilot depends on.
// It is idempotent — safe to call on every startup.
func Run(ctx context.Context, cfg *config.Config, gitea *tools.GiteaClient, logger *slog.Logger) {
	if cfg.Git.Provider != "gitea" || gitea == nil {
		logger.Info("bootstrap: skipping (not using gitea)")
		return
	}

	logger.Info("bootstrap: starting")

	// 1. Ensure context repo exists
	if cfg.Context.Enabled && cfg.Context.Repo != "" {
		ensureContextRepo(ctx, cfg, gitea, logger)
	}

	// 2. Ensure webhooks on all repos
	ensureWebhooks(ctx, cfg, gitea, logger)

	// 3. Ensure labels on all repos
	ensureLabels(ctx, cfg, gitea, logger)

	// 4. Ensure ArgoCD repo secret (via Gitea API to create the secret content,
	//    but we need kubectl for the k8s secret — use shell)
	ensureArgoRepoSecret(ctx, cfg, logger)

	// 5. Ensure Gitea registry auth secret for Tekton/Kaniko
	ensureRegistryAuthSecret(ctx, cfg, logger)

	logger.Info("bootstrap: complete")
}

func ensureContextRepo(ctx context.Context, cfg *config.Config, gitea *tools.GiteaClient, logger *slog.Logger) {
	owner, repo := splitRepo(cfg.Context.Repo)
	err := gitea.CreateRepo(ctx, repo, "Cross-session context store for kube-pilot")
	if err != nil {
		logger.Warn("bootstrap: failed to ensure context repo", "repo", cfg.Context.Repo, "error", err)
		return
	}
	logger.Info("bootstrap: context repo ready", "repo", cfg.Context.Repo)

	// CreateRepo is a user repo, but we want org repo if owner != admin user.
	// Since Gitea's CreateRepo with basic auth creates under the authenticated user,
	// and the user IS the owner (kube-pilot), this works for the kube-pilot/kube-pilot-context case.
	_ = owner
}

func ensureWebhooks(ctx context.Context, cfg *config.Config, gitea *tools.GiteaClient, logger *slog.Logger) {
	webhookURL := fmt.Sprintf("http://kube-pilot-kube-pilot.kube-pilot.svc:%s/webhook",
		portFromAddress(cfg.Server.Address))

	repos, err := listUserRepos(ctx, gitea)
	if err != nil {
		logger.Warn("bootstrap: failed to list repos", "error", err)
		return
	}

	for _, repo := range repos {
		if hasWebhook(ctx, gitea, repo, webhookURL) {
			continue
		}
		owner, name := splitRepo(repo)
		err := gitea.CreateWebhook(ctx, owner, name, webhookURL, cfg.Gitea.WebhookSecret)
		if err != nil {
			logger.Warn("bootstrap: failed to create webhook", "repo", repo, "error", err)
			continue
		}
		logger.Info("bootstrap: webhook created", "repo", repo)
	}
}

func ensureLabels(ctx context.Context, cfg *config.Config, gitea *tools.GiteaClient, logger *slog.Logger) {
	repos, err := listUserRepos(ctx, gitea)
	if err != nil {
		return
	}

	labels := []struct {
		Name        string
		Color       string
		Description string
	}{
		{"kube-pilot", "#00adef", "Trigger kube-pilot agent"},
		{"kube-pilot:plan-first", "#0075ca", "kube-pilot plans before executing"},
	}

	for _, repo := range repos {
		owner, name := splitRepo(repo)
		existing := getExistingLabels(ctx, gitea, owner, name)

		for _, label := range labels {
			if existing[label.Name] {
				continue
			}
			payload := map[string]string{
				"name":        label.Name,
				"color":       label.Color,
				"description": label.Description,
			}
			_, status, err := gitea.Do(ctx, "POST",
				fmt.Sprintf("/repos/%s/%s/labels", owner, name), payload)
			if err != nil || (status >= 300 && status != 409) {
				logger.Warn("bootstrap: failed to create label", "repo", repo, "label", label.Name, "error", err)
				continue
			}
			logger.Info("bootstrap: label created", "repo", repo, "label", label.Name)
		}
	}
}

func ensureArgoRepoSecret(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	// Create ArgoCD repository secret so ArgoCD can pull from Gitea
	repoURL := fmt.Sprintf("%s/kube-pilot/infra.git", cfg.Gitea.URL)

	cmd := fmt.Sprintf(`kubectl get secret gitea-repo -n kube-pilot 2>/dev/null && echo "exists" || \
kubectl apply -f - <<'ENDOFYAML'
apiVersion: v1
kind: Secret
metadata:
  name: gitea-repo
  namespace: kube-pilot
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: %s
  username: %s
  password: %s
ENDOFYAML`, repoURL, cfg.Gitea.AdminUser, cfg.Gitea.AdminPassword)

	result, err := tools.Shell(ctx, cmd)
	if err != nil {
		logger.Warn("bootstrap: failed to ensure argo repo secret", "error", err)
		return
	}
	if result.ExitCode != 0 {
		logger.Warn("bootstrap: argo repo secret failed", "stderr", result.Stderr)
		return
	}
	logger.Info("bootstrap: argo repo secret ready")
}

func ensureRegistryAuthSecret(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	// Create docker auth secret for Kaniko to push to Gitea's container registry
	host := trimScheme(cfg.Gitea.URL)
	auth := base64.StdEncoding.EncodeToString(
		[]byte(fmt.Sprintf("%s:%s", cfg.Gitea.AdminUser, cfg.Gitea.AdminPassword)))

	dockerConfig := fmt.Sprintf(`{"auths":{"%s":{"auth":"%s"}}}`, host, auth)
	encodedConfig := base64.StdEncoding.EncodeToString([]byte(dockerConfig))

	// Create in both kube-pilot and default namespaces (Tekton runs in default)
	for _, ns := range []string{"kube-pilot", "default"} {
		cmd := fmt.Sprintf(`kubectl get secret gitea-registry-auth -n %s 2>/dev/null && echo "exists" || \
kubectl apply -f - <<'ENDOFYAML'
apiVersion: v1
kind: Secret
metadata:
  name: gitea-registry-auth
  namespace: %s
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: %s
ENDOFYAML`, ns, ns, encodedConfig)

		result, err := tools.Shell(ctx, cmd)
		if err != nil {
			logger.Warn("bootstrap: failed to ensure registry auth", "namespace", ns, "error", err)
			continue
		}
		if result.ExitCode != 0 {
			logger.Warn("bootstrap: registry auth failed", "namespace", ns, "stderr", result.Stderr)
			continue
		}
		logger.Info("bootstrap: registry auth secret ready", "namespace", ns)
	}
}

// --- helpers ---

func listUserRepos(ctx context.Context, gitea *tools.GiteaClient) ([]string, error) {
	data, status, err := gitea.Do(ctx, "GET", "/user/repos?limit=50", nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("list repos: HTTP %d", status)
	}

	var repos []struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(data, &repos); err != nil {
		return nil, err
	}

	var names []string
	for _, r := range repos {
		names = append(names, r.FullName)
	}
	return names, nil
}

func hasWebhook(ctx context.Context, gitea *tools.GiteaClient, repo, targetURL string) bool {
	owner, name := splitRepo(repo)
	data, status, err := gitea.Do(ctx, "GET",
		fmt.Sprintf("/repos/%s/%s/hooks", owner, name), nil)
	if err != nil || status >= 300 {
		return false
	}

	var hooks []struct {
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &hooks); err != nil {
		return false
	}

	for _, h := range hooks {
		if h.Config.URL == targetURL {
			return true
		}
	}
	return false
}

func getExistingLabels(ctx context.Context, gitea *tools.GiteaClient, owner, name string) map[string]bool {
	data, status, err := gitea.Do(ctx, "GET",
		fmt.Sprintf("/repos/%s/%s/labels", owner, name), nil)
	if err != nil || status >= 300 {
		return nil
	}

	var labels []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil
	}

	m := make(map[string]bool)
	for _, l := range labels {
		m[l.Name] = true
	}
	return m
}

func splitRepo(fullName string) (string, string) {
	for i, c := range fullName {
		if c == '/' {
			return fullName[:i], fullName[i+1:]
		}
	}
	return fullName, fullName
}

func portFromAddress(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return "8080"
}

func trimScheme(url string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return url[len(prefix):]
		}
	}
	return url
}
