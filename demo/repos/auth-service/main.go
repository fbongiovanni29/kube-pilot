package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	secretKey  = []byte("clouddesk-demo-secret-change-me")
	sessions   = make(map[string]bool)
	sessionsMu sync.RWMutex
)

type Claims struct {
	Username string `json:"sub"`
	Exp      int64  `json:"exp"`
}

func main() {
	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/verify", handleVerify)
	http.HandleFunc("/logout", handleLogout)
	log.Println("auth-service listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if creds.Username == "" || creds.Password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}

	token, err := createToken(creds.Username)
	if err != nil {
		http.Error(w, "token creation failed", http.StatusInternalServerError)
		return
	}

	sessionsMu.Lock()
	sessions[token] = true
	sessionsMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := extractBearer(r)
	if token == "" {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	sessionsMu.RLock()
	active := sessions[token]
	sessionsMu.RUnlock()
	if !active {
		http.Error(w, "session revoked", http.StatusUnauthorized)
		return
	}

	claims, err := verifyToken(token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": claims.Username, "status": "valid"})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := extractBearer(r)
	if token == "" {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return
	}
	sessionsMu.Lock()
	delete(sessions, token)
	sessionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func createToken(username string) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := Claims{Username: username, Exp: time.Now().Add(24 * time.Hour).Unix()}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	sigInput := header + "." + payload
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(sigInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sigInput + "." + sig, nil
}

func verifyToken(token string) (*Claims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token")
	}
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, fmt.Errorf("invalid signature")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding")
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("invalid claims")
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
