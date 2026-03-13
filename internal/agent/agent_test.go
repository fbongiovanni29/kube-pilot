package agent

import (
	"context"
	"strings"
	"testing"

	"log/slog"
	"os"

	kpctx "github.com/fbongiovanni29/kube-pilot/internal/context"
	"github.com/fbongiovanni29/kube-pilot/internal/llm"
	"github.com/fbongiovanni29/kube-pilot/internal/tools"
)

// mockClient implements llm.Client for testing.
type mockClient struct {
	responses []llm.Response
	callCount int
}

func (m *mockClient) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.Response, error) {
	if m.callCount >= len(m.responses) {
		return &llm.Response{Content: "done"}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return &resp, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestAgentNoToolCalls(t *testing.T) {
	client := &mockClient{
		responses: []llm.Response{
			{Content: "All looks good, no action needed."},
		},
	}

	a := New(client, nil, nil, testLogger())
	result, err := a.Run(context.Background(), "check status")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "All looks good, no action needed." {
		t.Errorf("result = %q", result)
	}
	if client.callCount != 1 {
		t.Errorf("callCount = %d, want 1", client.callCount)
	}
}

func TestAgentWithExecTool(t *testing.T) {
	client := &mockClient{
		responses: []llm.Response{
			{
				Content: "Let me check.",
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "exec",
							Arguments: `{"command":"echo hello"}`,
						},
					},
				},
			},
			{Content: "The command returned 'hello'."},
		},
	}

	a := New(client, nil, nil, testLogger())
	result, err := a.Run(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "The command returned 'hello'." {
		t.Errorf("result = %q", result)
	}
	if client.callCount != 2 {
		t.Errorf("callCount = %d, want 2", client.callCount)
	}
}

func TestAgentMaxSteps(t *testing.T) {
	// LLM always returns a tool call — should hit max steps
	infiniteLoop := &mockClient{
		responses: make([]llm.Response, 100),
	}
	for i := range infiniteLoop.responses {
		infiniteLoop.responses[i] = llm.Response{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "exec",
						Arguments: `{"command":"echo loop"}`,
					},
				},
			},
		}
	}

	a := New(infiniteLoop, nil, nil, testLogger())
	a.maxSteps = 3

	_, err := a.Run(context.Background(), "infinite task")
	if err == nil {
		t.Fatal("expected max steps error")
	}
	if a.maxSteps != 3 {
		t.Errorf("maxSteps = %d", a.maxSteps)
	}
}

func TestAgentUnknownTool(t *testing.T) {
	client := &mockClient{
		responses: []llm.Response{
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "nonexistent_tool",
							Arguments: `{}`,
						},
					},
				},
			},
			{Content: "Got error, stopping."},
		},
	}

	a := New(client, nil, nil, testLogger())
	result, err := a.Run(context.Background(), "try unknown tool")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Agent should continue after tool error (error message sent back as tool result)
	if result != "Got error, stopping." {
		t.Errorf("result = %q", result)
	}
}

func TestToolDefs(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger())
	defs := a.toolDefs()
	// Base tools: exec, git_comment, git_close_issue, read_file, create_pr = 5
	if len(defs) < 5 {
		t.Fatalf("toolDefs() len = %d, want at least 5", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
		if d.Type != "function" {
			t.Errorf("tool %q type = %q, want %q", d.Function.Name, d.Type, "function")
		}
	}

	for _, name := range []string{"exec", "git_comment", "git_close_issue", "read_file", "create_pr"} {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestAgentBackwardsCompatToolNames(t *testing.T) {
	// Old tool names (github_comment, github_close_issue) should still work
	a := New(&mockClient{}, nil, nil, testLogger())

	// github_comment should route to execGitComment (which will fail on missing gitea/gh, but shouldn't panic)
	_, err := a.executeTool(context.Background(), llm.ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      "github_comment",
			Arguments: `{"repo":"org/repo","issue_number":1,"body":"test"}`,
		},
	})
	// Expected to fail (no gh CLI in test), but should not be "unknown tool"
	if err != nil && err.Error() == "unknown tool: github_comment" {
		t.Error("github_comment should be a valid backwards-compat tool name")
	}
}

func TestSystemPromptWithRepoContext(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger(), WithRepoContext("# My AGENTS.md\nUse Go 1.22"))
	prompt := a.systemPrompt()

	if !strings.Contains(prompt, "Repository Context") {
		t.Error("expected system prompt to contain 'Repository Context'")
	}
	if !strings.Contains(prompt, "# My AGENTS.md") {
		t.Error("expected system prompt to contain AGENTS.md content")
	}
}

func TestSystemPromptWithProjectContext(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger(), WithProjectContext("insights here"))
	prompt := a.systemPrompt()

	if !strings.Contains(prompt, "Prior Insights") {
		t.Error("expected system prompt to contain 'Prior Insights'")
	}
	if !strings.Contains(prompt, "insights here") {
		t.Error("expected system prompt to contain project context content")
	}
}

func TestSystemPromptWithoutContext(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger())
	prompt := a.systemPrompt()

	if strings.Contains(prompt, "Repository Context") {
		t.Error("expected system prompt to NOT contain 'Repository Context' without option")
	}
	if strings.Contains(prompt, "Prior Insights") {
		t.Error("expected system prompt to NOT contain 'Prior Insights' without option")
	}
}

func TestToolDefsWithContextStore(t *testing.T) {
	giteaClient := tools.NewGiteaClient("http://localhost", "u", "p")
	store := kpctx.NewStore(giteaClient, "owner/context-repo")

	a := New(&mockClient{}, nil, nil, testLogger(), WithContextStore(store))
	defs := a.toolDefs()

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}

	if !names["save_insight"] {
		t.Error("expected toolDefs to include 'save_insight' when context store is set")
	}
	if !names["read_context"] {
		t.Error("expected toolDefs to include 'read_context' when context store is set")
	}
}

func TestToolDefsWithoutContextStore(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger())
	defs := a.toolDefs()

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}

	if names["save_insight"] {
		t.Error("expected toolDefs to NOT include 'save_insight' without context store")
	}
	if names["read_context"] {
		t.Error("expected toolDefs to NOT include 'read_context' without context store")
	}
}
