package tools

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// GitHubAppAuth handles GitHub App authentication.
// It generates installation tokens from the App's private key and
// sets GH_TOKEN so the gh CLI authenticates as the app (bot).
type GitHubAppAuth struct {
	appID          string
	installationID string
	privateKey     *rsa.PrivateKey
	logger         *slog.Logger

	mu           sync.Mutex
	token        string
	tokenExpires time.Time
}

// NewGitHubAppAuth creates a new GitHub App authenticator.
// privateKeyPEM is the raw PEM-encoded private key content.
func NewGitHubAppAuth(appID, installationID string, privateKeyPEM []byte, logger *slog.Logger) (*GitHubAppAuth, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		parsed, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: %w (pkcs8: %w)", err, err2)
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
	}

	return &GitHubAppAuth{
		appID:          appID,
		installationID: installationID,
		privateKey:     key,
		logger:         logger,
	}, nil
}

// Token returns a valid installation token, refreshing if needed.
func (g *GitHubAppAuth) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Return cached token if it has >5 min remaining
	if g.token != "" && time.Until(g.tokenExpires) > 5*time.Minute {
		return g.token, nil
	}

	token, expires, err := g.refreshToken(ctx)
	if err != nil {
		return "", err
	}

	g.token = token
	g.tokenExpires = expires
	os.Setenv("GH_TOKEN", token)
	g.logger.Info("github app token refreshed", "expires", expires.Format(time.RFC3339))

	return token, nil
}

// EnsureToken refreshes the token if needed and sets GH_TOKEN.
func (g *GitHubAppAuth) EnsureToken(ctx context.Context) error {
	_, err := g.Token(ctx)
	return err
}

// StartTokenRefresh starts a background goroutine that keeps the token fresh.
func (g *GitHubAppAuth) StartTokenRefresh(ctx context.Context) {
	// Do initial refresh
	if err := g.EnsureToken(ctx); err != nil {
		g.logger.Error("initial github app token refresh failed", "error", err)
	}

	go func() {
		ticker := time.NewTicker(50 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := g.EnsureToken(ctx); err != nil {
					g.logger.Error("github app token refresh failed", "error", err)
				}
			}
		}
	}()
}

func (g *GitHubAppAuth) refreshToken(ctx context.Context) (string, time.Time, error) {
	jwt, err := g.generateJWT()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate JWT: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", g.installationID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("request installation token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		return "", time.Time{}, fmt.Errorf("installation token request failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", time.Time{}, fmt.Errorf("parse token response: %w", err)
	}

	return result.Token, result.ExpiresAt, nil
}

func (g *GitHubAppAuth) generateJWT() (string, error) {
	now := time.Now()
	header := base64URLEncode([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payload := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":"%s"}`,
		now.Add(-60*time.Second).Unix(),
		now.Add(10*time.Minute).Unix(),
		g.appID,
	)
	encodedPayload := base64URLEncode([]byte(payload))

	signingInput := header + "." + encodedPayload
	hash := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}

	return signingInput + "." + base64URLEncode(sig), nil
}

func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}
