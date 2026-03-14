package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChatBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}

		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "gpt-4" {
			t.Errorf("model = %q", req.Model)
		}
		if len(req.Messages) != 1 {
			t.Errorf("messages len = %d", len(req.Messages))
		}

		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
			}{
				{Message: struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				}{Content: "Hello!"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "gpt-4",
	})

	resp, err := client.Chat(context.Background(), []Message{
		{Role: RoleUser, Content: "hi"},
	}, nil)

	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls len = %d", len(resp.ToolCalls))
	}
}

func TestChatWithToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
			}{
				{Message: struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				}{
					Content: "I'll run that.",
					ToolCalls: []ToolCall{
						{
							ID: "call_1",
							// Intentionally omit Type to test auto-fill
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "exec",
								Arguments: `{"command":"echo hi"}`,
							},
						},
					},
				}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	resp, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "run echo"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("ID = %q", tc.ID)
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q, want %q (should be auto-filled)", tc.Type, "function")
	}
	if tc.Function.Name != "exec" {
		t.Errorf("Function.Name = %q", tc.Function.Name)
	}
}

func TestChatAPIError(t *testing.T) {
	// Non-retryable error (400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	_, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestChatRetryOn429(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
			}{
				{Message: struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				}{Content: "recovered"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	resp, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want %q", resp.Content, "recovered")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("attempts = %d, want 3", atomic.LoadInt32(&attempts))
	}
}

func TestChatRetryExhausted(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	_, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// 1 initial + 3 retries = 4
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("attempts = %d, want 4", atomic.LoadInt32(&attempts))
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*1e9 {
		t.Errorf("parseRetryAfter(5) = %v", got)
	}
	if got := parseRetryAfter("0"); got != 0 {
		t.Errorf("parseRetryAfter(0) = %v, want 0", got)
	}
	if got := parseRetryAfter(""); got != -1 {
		t.Errorf("parseRetryAfter('') = %v, want -1", got)
	}
	if got := parseRetryAfter("invalid"); got != -1 {
		t.Errorf("parseRetryAfter('invalid') = %v, want -1", got)
	}
}

func TestChatErrorInBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Error: &struct {
				Message string `json:"message"`
			}{Message: "invalid model"},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "bad"})
	_, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	_, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestChatNoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no auth header when apiKey is empty")
		}
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
			}{
				{Message: struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				}{Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "ollama"})
	resp, err := client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q", resp.Content)
	}
}

func TestChatWithTools(t *testing.T) {
	var gotTools []Tool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotTools = req.Tools

		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				} `json:"message"`
			}{
				{Message: struct {
					Content   string     `json:"content"`
					ToolCalls []ToolCall `json:"tool_calls,omitempty"`
				}{Content: "ok"}},
			},
		})
	}))
	defer srv.Close()

	client := NewOpenAICompat(OpenAICompatConfig{BaseURL: srv.URL, Model: "test"})
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "exec", Description: "run cmd"}},
	}
	client.Chat(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, tools)

	if len(gotTools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(gotTools))
	}
	if gotTools[0].Function.Name != "exec" {
		t.Errorf("tool name = %q", gotTools[0].Function.Name)
	}
}
