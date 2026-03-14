package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type Notification struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Target  string `json:"target"`
}

type QueueResponse struct {
	Notifications []Notification `json:"notifications"`
}

func main() {
	queueURL := envOrDefault("QUEUE_URL", "http://localhost:9090/queue/notifications")
	webhookURL := envOrDefault("WEBHOOK_URL", "http://localhost:9091/webhook")
	pollInterval := 5 * time.Second

	log.Printf("notifications-worker starting — queue=%s webhook=%s interval=%s", queueURL, webhookURL, pollInterval)

	for {
		count, err := pollAndDispatch(queueURL, webhookURL)
		if err != nil {
			log.Printf("poll error: %v", err)
		} else if count > 0 {
			log.Printf("dispatched %d notifications", count)
		}
		time.Sleep(pollInterval)
	}
}

func pollAndDispatch(queueURL, webhookURL string) (int, error) {
	resp, err := http.Get(queueURL)
	if err != nil {
		return 0, fmt.Errorf("queue fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("queue returned status %d", resp.StatusCode)
	}

	var queue QueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&queue); err != nil {
		return 0, fmt.Errorf("decode error: %w", err)
	}

	dispatched := 0
	for _, n := range queue.Notifications {
		if err := sendWebhook(webhookURL, n); err != nil {
			log.Printf("webhook failed for %s: %v", n.ID, err)
			continue
		}
		dispatched++
	}
	return dispatched, nil
}

func sendWebhook(url string, n Notification) error {
	payload, err := json.Marshal(map[string]string{
		"notification_id": n.ID,
		"type":            n.Type,
		"message":         n.Message,
		"target":          n.Target,
		"sent_at":         time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("post failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
