package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

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

func TestAgentWorkDirIsolation(t *testing.T) {
	a1 := New(&mockClient{}, nil, nil, testLogger())
	a2 := New(&mockClient{}, nil, nil, testLogger())
	defer a1.Cleanup()
	defer a2.Cleanup()

	if a1.workDir == "" {
		t.Fatal("agent1 workDir is empty")
	}
	if a2.workDir == "" {
		t.Fatal("agent2 workDir is empty")
	}
	if a1.workDir == a2.workDir {
		t.Errorf("two agents got the same workDir: %s", a1.workDir)
	}

	// Verify both dirs exist
	if _, err := os.Stat(a1.workDir); err != nil {
		t.Errorf("agent1 workDir doesn't exist: %v", err)
	}
	if _, err := os.Stat(a2.workDir); err != nil {
		t.Errorf("agent2 workDir doesn't exist: %v", err)
	}

	// Verify dirs have the kube-pilot-agent prefix
	if !strings.Contains(filepath.Base(a1.workDir), "kube-pilot-agent") {
		t.Errorf("workDir %q doesn't have expected prefix", a1.workDir)
	}
}

func TestAgentCleanup(t *testing.T) {
	a := New(&mockClient{}, nil, nil, testLogger())
	dir := a.workDir
	if dir == "" {
		t.Fatal("workDir is empty")
	}

	a.Cleanup()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("workDir %q still exists after Cleanup", dir)
	}
}

func TestAgentExecUsesWorkDir(t *testing.T) {
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
							Arguments: `{"command":"pwd"}`,
						},
					},
				},
			},
			{Content: "Done."},
		},
	}

	a := New(client, nil, nil, testLogger())
	defer a.Cleanup()

	result, err := a.Run(context.Background(), "print working dir")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result != "Done." {
		t.Errorf("result = %q", result)
	}
	// The exec tool should have used the agent's workDir
	// We can't easily check the pwd output from the mock, but we verify
	// the agent was created with a valid workDir
	if a.workDir == "" {
		t.Error("expected agent to have a workDir set")
	}
}

func TestScrubCredentials(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secrets []string
		want   string
	}{
		{
			name:    "gitea password literal",
			input:   "I cloned with password=mysecret123 from the repo",
			secrets: []string{"mysecret123"},
			want:    "I cloned with ***REDACTED*** from the repo",
		},
		{
			name:    "bearer token",
			input:   "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc",
			secrets: nil,
			want:    "Authorization: ***REDACTED***",
		},
		{
			name:    "github token",
			input:   "Used token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn to authenticate",
			secrets: nil,
			want:    "Used token ***REDACTED*** to authenticate",
		},
		{
			name:    "password in key=value",
			input:   "password=hunter2 in config",
			secrets: nil,
			want:    "***REDACTED*** in config",
		},
		{
			name:    "no secrets in clean text",
			input:   "Deployed successfully to production",
			secrets: nil,
			want:    "Deployed successfully to production",
		},
		{
			name:    "short secret ignored",
			input:   "abc",
			secrets: []string{"ab"},
			want:    "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubCredentials(tt.input, tt.secrets)
			if got != tt.want {
				t.Errorf("scrubCredentials() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompactMessagesUnderLimit(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "response"},
	}
	result := compactMessages(messages)
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no compaction), got %d", len(result))
	}
}

func TestCompactMessagesOverLimit(t *testing.T) {
	// Build a conversation that exceeds maxContextChars
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "initial task"},
	}

	// Add enough messages to exceed the limit
	bigContent := strings.Repeat("x", 20000)
	for i := 0; i < 30; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: bigContent,
		})
	}

	result := compactMessages(messages)

	// Should be compacted: 2 head + 1 compaction marker + 20 tail = 23
	if len(result) != 23 {
		t.Errorf("expected 23 messages after compaction, got %d", len(result))
	}

	// First two should be preserved
	if result[0].Role != llm.RoleSystem {
		t.Error("expected first message to be system")
	}
	if result[1].Role != llm.RoleUser {
		t.Error("expected second message to be initial task")
	}

	// Third should be compaction marker
	if !strings.Contains(result[2].Content, "compacted") {
		t.Error("expected compaction marker message")
	}
}

func TestKnownSecrets(t *testing.T) {
	a := New(&mockClient{}, nil, &GiteaInfo{
		URL:      "http://gitea.local",
		User:     "admin",
		Password: "super-secret-pass",
	}, testLogger())
	defer a.Cleanup()

	secrets := a.knownSecrets()
	found := false
	for _, s := range secrets {
		if s == "super-secret-pass" {
			found = true
		}
	}
	if !found {
		t.Error("expected knownSecrets to include giteaInfo password")
	}
}

func TestMessageSize(t *testing.T) {
	m := llm.Message{
		Role:    llm.RoleAssistant,
		Content: "hello",
		ToolCalls: []llm.ToolCall{
			{
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      "exec",
					Arguments: `{"command":"ls"}`,
				},
			},
		},
	}

	size := messageSize(m)
	// "hello" (5) + "exec" (4) + arguments (16) = 25
	if size != 25 {
		t.Errorf("messageSize() = %d, want 25", size)
	}
}
