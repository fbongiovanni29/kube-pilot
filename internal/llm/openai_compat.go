package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// OpenAICompatClient works with any OpenAI-compatible API:
// Anthropic (via proxy), OpenAI, Ollama, vLLM, LiteLLM, etc.
type OpenAICompatClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// OpenAICompatConfig holds configuration for the client.
type OpenAICompatConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// NewOpenAICompat creates a new OpenAI-compatible client.
func NewOpenAICompat(cfg OpenAICompatConfig) *OpenAICompatClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &OpenAICompatClient{
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

const maxRetries = 3

func (c *OpenAICompatClient) Chat(ctx context.Context, messages []Message, tools []Tool) (*Response, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, respBody, err := c.doRequest(ctx, body)
		if err != nil {
			lastErr = err
			if attempt < maxRetries {
				if !c.backoff(ctx, attempt, -1) {
					return nil, ctx.Err()
				}
			}
			continue
		}

		// Rate limited (429) or server error (5xx) — retry with backoff
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
			if attempt < maxRetries {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if !c.backoff(ctx, attempt, retryAfter) {
					return nil, ctx.Err()
				}
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		}

		return c.parseResponse(respBody)
	}

	return nil, fmt.Errorf("exhausted %d retries: %w", maxRetries, lastErr)
}

func (c *OpenAICompatClient) doRequest(ctx context.Context, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	return resp, respBody, nil
}

func (c *OpenAICompatClient) parseResponse(respBody []byte) (*Response, error) {
	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := chatResp.Choices[0]
	// Ensure all tool calls have type set (required by some providers)
	for i := range choice.Message.ToolCalls {
		if choice.Message.ToolCalls[i].Type == "" {
			choice.Message.ToolCalls[i].Type = "function"
		}
	}
	return &Response{
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
	}, nil
}

// backoff waits before a retry. Uses retryAfter if >= 0, otherwise exponential backoff.
// Returns false if context was cancelled.
func (c *OpenAICompatClient) backoff(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	wait := retryAfter
	if wait < 0 {
		wait = time.Duration(attempt+1) * 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
		return true
	}
}

// parseRetryAfter parses the Retry-After header value (seconds).
// Returns -1 if the header is missing or invalid.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return -1
	}
	secs, err := strconv.Atoi(val)
	if err != nil {
		return -1
	}
	return time.Duration(secs) * time.Second
}
