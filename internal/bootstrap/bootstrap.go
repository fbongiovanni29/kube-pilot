package bootstrap

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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

	// 6. Ensure Grafana API key for observability
	if cfg.Observability.Enabled && cfg.Observability.Grafana.URL != "" {
		if ensureGrafanaAPIKey(ctx, cfg, logger) {
			// Key was just created — restart the deployment so the pod picks it up via envFrom.
			// On the next startup, the key already exists so this is a one-time operation.
			logger.Info("bootstrap: restarting deployment to pick up new grafana API key")
			restartCmd := `kubectl rollout restart deployment/kube-pilot-kube-pilot -n kube-pilot`
			tools.Shell(ctx, restartCmd)
		}
	}

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

// ensureGrafanaAPIKey creates a Grafana service account and API token, stores it
// in Vault and the bootstrap k8s secret. Returns true if a new key was created
// (caller should restart the deployment to pick it up via envFrom).
func ensureGrafanaAPIKey(ctx context.Context, cfg *config.Config, logger *slog.Logger) bool {
	grafanaURL := cfg.Observability.Grafana.URL

	// Step 0: Wait for Grafana to be ready (up to 2 minutes)
	ready := false
	healthCmd := fmt.Sprintf(`curl -sf %s/api/health -o /dev/null 2>/dev/null`, grafanaURL)
	for i := 0; i < 24; i++ {
		result, _ := tools.Shell(ctx, healthCmd)
		if result != nil && result.ExitCode == 0 {
			ready = true
			break
		}
		logger.Info("bootstrap: waiting for grafana to be ready", "attempt", i+1)
		time.Sleep(5 * time.Second)
	}
	if !ready {
		logger.Warn("bootstrap: grafana not ready after 2 minutes, skipping API key setup")
		return false
	}

	// Step 1: Check if GRAFANA_API_KEY is already set in the bootstrap secret
	checkCmd := `kubectl get secret kube-pilot-kube-pilot-bootstrap -n kube-pilot -o jsonpath='{.data.GRAFANA_API_KEY}' 2>/dev/null`
	checkResult, _ := tools.Shell(ctx, checkCmd)
	if checkResult != nil && checkResult.Stdout != "" {
		logger.Info("bootstrap: grafana API key already exists")
		return false
	}

	// Step 2: Get Grafana admin password from the k8s secret created by kube-prometheus-stack
	getPassCmd := `kubectl get secret -n kube-pilot -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].data.admin-password}' 2>/dev/null | base64 -d`
	passResult, err := tools.Shell(ctx, getPassCmd)
	if err != nil || passResult.ExitCode != 0 || passResult.Stdout == "" {
		logger.Warn("bootstrap: could not read grafana admin password from secret", "error", err)
		return false
	}
	grafanaPass := passResult.Stdout

	// Step 3: Create Grafana service account (idempotent — 409 if exists)
	createSACmd := fmt.Sprintf(
		`curl -s -u admin:%s -X POST %s/api/serviceaccounts -H 'Content-Type: application/json' -d '{"name":"kube-pilot","role":"Admin"}'`,
		shellEscape(grafanaPass), grafanaURL)
	saResult, err := tools.Shell(ctx, createSACmd)
	if err != nil {
		logger.Warn("bootstrap: failed to create grafana service account", "error", err)
		return false
	}

	// Parse the SA ID — could be from a fresh create or we need to look it up
	var saResp struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(saResult.Stdout), &saResp); err != nil || saResp.ID == 0 {
		// Might be a conflict (already exists) — look it up
		listCmd := fmt.Sprintf(
			`curl -s -u admin:%s %s/api/serviceaccounts/search?query=kube-pilot`,
			shellEscape(grafanaPass), grafanaURL)
		listResult, err := tools.Shell(ctx, listCmd)
		if err != nil {
			logger.Warn("bootstrap: failed to list grafana service accounts", "error", err)
			return false
		}
		var listResp struct {
			ServiceAccounts []struct {
				ID int `json:"id"`
			} `json:"serviceAccounts"`
		}
		if err := json.Unmarshal([]byte(listResult.Stdout), &listResp); err != nil || len(listResp.ServiceAccounts) == 0 {
			logger.Warn("bootstrap: could not find grafana service account", "response", saResult.Stdout)
			return false
		}
		saResp.ID = listResp.ServiceAccounts[0].ID
	}

	// Step 4: Create a token for the service account
	createTokenCmd := fmt.Sprintf(
		`curl -s -u admin:%s -X POST %s/api/serviceaccounts/%d/tokens -H 'Content-Type: application/json' -d '{"name":"kube-pilot-bootstrap"}'`,
		shellEscape(grafanaPass), grafanaURL, saResp.ID)
	tokenResult, err := tools.Shell(ctx, createTokenCmd)
	if err != nil {
		logger.Warn("bootstrap: failed to create grafana token", "error", err)
		return false
	}

	var tokenResp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(tokenResult.Stdout), &tokenResp); err != nil || tokenResp.Key == "" {
		logger.Warn("bootstrap: failed to parse grafana token", "response", tokenResult.Stdout)
		return false
	}

	// Step 5: Store in Vault (if available)
	vaultCmd := fmt.Sprintf(
		`kubectl exec -n kube-pilot kube-pilot-vault-0 -- vault kv put secret/kube-pilot/grafana api_key=%s 2>/dev/null`,
		shellEscape(tokenResp.Key))
	vaultResult, _ := tools.Shell(ctx, vaultCmd)
	if vaultResult != nil && vaultResult.ExitCode == 0 {
		logger.Info("bootstrap: grafana API key stored in vault")
	}

	// Step 6: Patch the bootstrap secret so the next pod incarnation gets it via envFrom
	patchCmd := fmt.Sprintf(
		`kubectl patch secret kube-pilot-kube-pilot-bootstrap -n kube-pilot --type merge -p '{"stringData":{"GRAFANA_API_KEY":"%s"}}'`,
		tokenResp.Key)
	patchResult, err := tools.Shell(ctx, patchCmd)
	if err != nil || patchResult.ExitCode != 0 {
		logger.Warn("bootstrap: failed to patch bootstrap secret with grafana key", "error", err)
		return false
	}

	logger.Info("bootstrap: grafana API key ready")
	return true
}

func shellEscape(s string) string {
	// Single-quote the string, escaping any embedded single quotes
	// 'foo'\''bar' → foo'bar in shell
	var result string
	result += "'"
	for _, c := range s {
		if c == '\'' {
			result += "'\\''"
		} else {
			result += string(c)
		}
	}
	result += "'"
	return result
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
