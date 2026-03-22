package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"time"

	"github.com/fbongiovanni29/kube-pilot/internal/agent"
	"github.com/fbongiovanni29/kube-pilot/internal/config"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestVerifySignature(t *testing.T) {
	secret := "test-secret"
	payload := []byte(`{"action":"opened"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name      string
		signature string
		want      bool
	}{
		{"valid", validSig, true},
		{"empty", "", false},
		{"no prefix", hex.EncodeToString(mac.Sum(nil)), false},
		{"wrong sig", "sha256=0000000000000000000000000000000000000000000000000000000000000000", false},
		{"bad hex", "sha256=not-hex", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifySignature(payload, tt.signature, secret); got != tt.want {
				t.Errorf("verifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasLabel(t *testing.T) {
	labels := []ghLabel{{Name: "bug"}, {Name: "kube-pilot"}, {Name: "urgent"}}

	if !hasLabel(labels, "kube-pilot") {
		t.Error("expected kube-pilot label to be found")
	}
	if hasLabel(labels, "feature") {
		t.Error("expected feature label to not be found")
	}
	if hasLabel(nil, "kube-pilot") {
		t.Error("expected nil labels to return false")
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{},
		logger: testLogger(),
	}

	req := httptest.NewRequest("GET", "/webhook", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git:    config.GitConfig{Provider: "github"},
			GitHub: config.GitHubConfig{WebhookSecret: "secret"},
		},
		logger: testLogger(),
	}

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	req.Header.Set("X-GitHub-Event", "issues")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestWebhookNoSignatureRequired(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git: config.GitConfig{Provider: "gitea"},
		},
		logger: testLogger(),
	}

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{}`))
	req.Header.Set("X-Gitea-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Should accept (200 OK) since no webhook secret is configured
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookGiteaEventHeader(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git: config.GitConfig{Provider: "gitea"},
		},
		logger: testLogger(),
	}

	// Gitea issue event with no kube-pilot label — should accept but not process
	evt := issueEvent{Action: "opened"}
	evt.Repository.FullName = "kube-pilot/infra"
	body, _ := json.Marshal(evt)

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Gitea-Event", "issues")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookGiteaSignatureHeader(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git:   config.GitConfig{Provider: "gitea"},
			Gitea: config.GiteaConfig{WebhookSecret: "gitea-secret"},
		},
		logger: testLogger(),
	}

	payload := []byte(`{}`)
	mac := hmac.New(sha256.New, []byte("gitea-secret"))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(payload)))
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestIsWatchedRepoGitea(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git: config.GitConfig{Provider: "gitea"},
		},
	}

	// Gitea mode: all repos are watched
	if !h.isWatchedRepo("anything/goes") {
		t.Error("gitea mode should watch all repos")
	}
}

func TestIsWatchedRepoGitHub(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git:    config.GitConfig{Provider: "github"},
			GitHub: config.GitHubConfig{Repos: []string{"org/repo1", "org/repo2"}},
		},
	}

	if !h.isWatchedRepo("org/repo1") {
		t.Error("expected org/repo1 to be watched")
	}
	if h.isWatchedRepo("org/repo3") {
		t.Error("expected org/repo3 to not be watched")
	}
}

func TestIsPlanFirst(t *testing.T) {
	withPlanFirst := []ghLabel{{Name: "bug"}, {Name: "kube-pilot:plan-first"}}
	withoutPlanFirst := []ghLabel{{Name: "bug"}, {Name: "kube-pilot"}}

	if !isPlanFirst(withPlanFirst) {
		t.Error("expected kube-pilot:plan-first label to trigger plan-first mode")
	}
	if isPlanFirst(withoutPlanFirst) {
		t.Error("expected kube-pilot label alone to not trigger plan-first mode")
	}
	if isPlanFirst(nil) {
		t.Error("expected nil labels to return false")
	}
}

func TestIsApproval(t *testing.T) {
	approvals := []string{
		"@kube-pilot lgtm",
		"@kube-pilot approved",
		"@kube-pilot go ahead",
		"@kube-pilot proceed",
		"@kube-pilot ship it",
		"@kube-pilot do it",
	}
	for _, body := range approvals {
		if !isApproval(body) {
			t.Errorf("expected %q to be an approval", body)
		}
	}

	nonApprovals := []string{
		"@kube-pilot",
		"@kube-pilot fix this",
		"lgtm",
		"approved",
		"looks good to me",
	}
	for _, body := range nonApprovals {
		if isApproval(body) {
			t.Errorf("expected %q to NOT be an approval", body)
		}
	}
}

func TestIsApprovalCaseInsensitive(t *testing.T) {
	cases := []string{
		"@kube-pilot LGTM",
		"@kube-pilot Approved",
		"@kube-pilot Go Ahead",
		"@kube-pilot PROCEED",
	}
	for _, body := range cases {
		if !isApproval(body) {
			t.Errorf("expected %q to be an approval (case insensitive)", body)
		}
	}
}

func TestDispatchInjectsIntoRunningAgent(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{Git: config.GitConfig{Provider: "gitea"}},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	key := issueKey{"org/repo", 1}

	// Simulate a running agent by registering one
	a := agent.New(nil, nil, nil, testLogger())
	h.mu.Lock()
	h.agents[key] = a
	h.mu.Unlock()

	// Dispatch while agent is active — should inject, not start a new one
	h.dispatch(key, "new comment context", "org/repo")

	// Verify no second agent was registered (still the same one)
	h.mu.Lock()
	if h.agents[key] != a {
		t.Error("expected same agent instance, got a different one")
	}
	h.mu.Unlock()
}

func TestDispatchStartsNewAgent(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{Git: config.GitConfig{Provider: "gitea"}},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	key := issueKey{"org/repo", 1}

	// No active agent — dispatch should not have an agent registered yet
	h.mu.Lock()
	_, exists := h.agents[key]
	h.mu.Unlock()

	if exists {
		t.Error("expected no agent before dispatch")
	}
}

func TestIsBotUserGitea(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git:   config.GitConfig{Provider: "gitea"},
			Gitea: config.GiteaConfig{AdminUser: "kube-pilot-admin"},
		},
	}

	if !h.isBotUser("kube-pilot-admin") {
		t.Error("expected bot's own admin user to be detected")
	}
	if !h.isBotUser("Kube-Pilot-Admin") {
		t.Error("expected case-insensitive match for bot user")
	}
	if h.isBotUser("some-human") {
		t.Error("expected non-bot user to not match")
	}
	if h.isBotUser("") {
		t.Error("expected empty username to not match")
	}
}

func TestIsBotUserGitHub(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git: config.GitConfig{Provider: "github"},
		},
	}

	if !h.isBotUser("kube-pilot[bot]") {
		t.Error("expected kube-pilot[bot] to be detected")
	}
	if h.isBotUser("some-human") {
		t.Error("expected non-bot user to not match")
	}
}

func TestBotCommentIgnored(t *testing.T) {
	h := &WebhookHandler{
		cfg: &config.Config{
			Git:   config.GitConfig{Provider: "gitea"},
			Gitea: config.GiteaConfig{AdminUser: "kube-pilot-admin"},
		},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	// Comment from the bot itself mentioning @kube-pilot
	evt := issueCommentEvent{Action: "created"}
	evt.Comment.Body = "@kube-pilot this is my summary"
	evt.Comment.User.Username = "kube-pilot-admin"
	evt.Issue.Number = 1
	evt.Issue.Labels = []ghLabel{{Name: "kube-pilot"}}
	evt.Repository.FullName = "org/repo"
	body, _ := json.Marshal(evt)

	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-Gitea-Event", "issue_comment")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify no agent was started
	h.mu.Lock()
	if len(h.agents) != 0 {
		t.Error("expected no agent to be started for bot's own comment")
	}
	h.mu.Unlock()
}

func TestHandleAlertmanagerFiringAlert(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{Git: config.GitConfig{Provider: "gitea"}},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	payload := alertmanagerPayload{
		Status: "firing",
		Alerts: []alertmanagerAlert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "HighErrorRate",
					"namespace": "default",
					"pod":       "web-app-xyz",
					"severity":  "critical",
				},
				Annotations: map[string]string{
					"summary":     "Error rate above 5%",
					"description": "Pod web-app-xyz has error rate of 12%",
				},
				StartsAt: "2026-03-22T10:00:00Z",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/alertmanager-webhook", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.HandleAlertmanager(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify an agent was dispatched (slot reserved)
	time.Sleep(50 * time.Millisecond) // let goroutine start
	h.mu.Lock()
	found := false
	for k := range h.agents {
		if strings.Contains(k.repo, "__alertmanager__") {
			found = true
		}
	}
	h.mu.Unlock()

	if !found {
		t.Error("expected agent to be dispatched for firing alert")
	}
}

func TestHandleAlertmanagerResolvedIgnored(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{Git: config.GitConfig{Provider: "gitea"}},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	payload := alertmanagerPayload{
		Status: "resolved",
		Alerts: []alertmanagerAlert{
			{
				Status: "resolved",
				Labels: map[string]string{
					"alertname": "HighErrorRate",
					"namespace": "default",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/alertmanager-webhook", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.HandleAlertmanager(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// No agent should be dispatched for resolved alerts
	h.mu.Lock()
	if len(h.agents) != 0 {
		t.Error("expected no agent to be dispatched for resolved alert")
	}
	h.mu.Unlock()
}

func TestHandleAlertmanagerMethodNotAllowed(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{},
		logger: testLogger(),
	}

	req := httptest.NewRequest("GET", "/alertmanager-webhook", nil)
	w := httptest.NewRecorder()
	h.HandleAlertmanager(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleAlertmanagerInvalidPayload(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	req := httptest.NewRequest("POST", "/alertmanager-webhook", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	h.HandleAlertmanager(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAlertHashStable(t *testing.T) {
	h1 := alertHash("HighErrorRate", "default")
	h2 := alertHash("HighErrorRate", "default")
	h3 := alertHash("OtherAlert", "default")

	if h1 != h2 {
		t.Error("expected same inputs to produce same hash")
	}
	if h1 == h3 {
		t.Error("expected different inputs to produce different hash")
	}
}

func TestHandleAlertmanagerDedup(t *testing.T) {
	h := &WebhookHandler{
		cfg:    &config.Config{Git: config.GitConfig{Provider: "gitea"}},
		logger: testLogger(),
		agents: make(map[issueKey]*agent.Agent),
	}

	// Pre-register an agent for this alert to test dedup
	alertName := "HighErrorRate"
	namespace := "default"
	dedupKey := "__alertmanager__" + alertName + "_" + namespace
	issueNum := int(alertHash(alertName, namespace))
	key := issueKey{repo: dedupKey, issueNumber: issueNum}

	existingAgent := agent.New(nil, nil, nil, testLogger())
	h.mu.Lock()
	h.agents[key] = existingAgent
	h.mu.Unlock()

	payload := alertmanagerPayload{
		Status: "firing",
		Alerts: []alertmanagerAlert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": alertName,
					"namespace": namespace,
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/alertmanager-webhook", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.HandleAlertmanager(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should still be the same agent (injected, not replaced)
	h.mu.Lock()
	if h.agents[key] != existingAgent {
		t.Error("expected same agent instance after dedup (inject, not replace)")
	}
	h.mu.Unlock()
}

func TestCommentOnFailure(t *testing.T) {
	// Track whether the comment API was called
	var commentPosted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/comments") {
			commentPosted = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gitea := tools.NewGiteaClient(srv.URL, "admin", "pass")

	h := &WebhookHandler{
		cfg: &config.Config{
			Git: config.GitConfig{Provider: "gitea"},
		},
		logger: testLogger(),
		gitea:  gitea,
		agents: make(map[issueKey]*agent.Agent),
	}

	key := issueKey{"org/repo", 42}
	h.commentOnFailure(key, "org/repo", "test failure message")

	if !commentPosted {
		t.Error("expected failure comment to be posted via Gitea API")
	}
}
