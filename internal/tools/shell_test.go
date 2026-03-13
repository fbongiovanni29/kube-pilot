package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShellBasic(t *testing.T) {
	result, err := Shell(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Shell() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello")
	}
}

func TestShellNonZeroExit(t *testing.T) {
	result, err := Shell(context.Background(), "exit 42")
	if err != nil {
		t.Fatalf("Shell() error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestShellStderr(t *testing.T) {
	result, err := Shell(context.Background(), "echo oops >&2; exit 1")
	if err != nil {
		t.Fatalf("Shell() error: %v", err)
	}
	if !strings.Contains(result.Stderr, "oops") {
		t.Errorf("Stderr = %q, want to contain %q", result.Stderr, "oops")
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
}

func TestShellTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := Shell(ctx, "sleep 10")
	if err != nil {
		t.Fatalf("Shell() error: %v", err)
	}
	// Should have a non-zero exit code from timeout
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit code from timeout")
	}
}

func TestShellOutputTruncation(t *testing.T) {
	// Generate output larger than maxOutput (100KB)
	result, err := Shell(context.Background(), "yes | head -n 200000")
	if err != nil {
		t.Fatalf("Shell() error: %v", err)
	}
	if !strings.Contains(result.Stdout, "(truncated)") {
		t.Error("expected truncated output for large stdout")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`hello`, `"hello"`},
		{`it's`, `"it's"`},
		{`say "hi"`, `"say \"hi\""`},
		{`line\nbreak`, `"line\\nbreak"`},
	}

	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
