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

	"github.com/fbongiovanni29/kube-pilot/internal/config"
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
