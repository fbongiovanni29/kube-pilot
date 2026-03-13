package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

const (
	defaultShellTimeout = 120 * time.Second
	maxOutputBytes      = 100_000
)

// ShellResult holds the output of a shell command.
type ShellResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Shell executes a command in a bash shell and returns the result.
func Shell(ctx context.Context, command string) (*ShellResult, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultShellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &ShellResult{
		Stdout:   truncate(stdout.String(), maxOutputBytes),
		Stderr:   truncate(stderr.String(), maxOutputBytes),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			return nil, fmt.Errorf("command timed out after %v", defaultShellTimeout)
		} else {
			return nil, fmt.Errorf("exec error: %w", err)
		}
	}

	return result, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (truncated)"
}
