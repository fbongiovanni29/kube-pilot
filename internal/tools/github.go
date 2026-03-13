package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// GitHubComment posts a comment on a GitHub issue or PR using the gh CLI.
func GitHubComment(ctx context.Context, repo string, issueNumber int, body string) error {
	cmd := fmt.Sprintf(`gh issue comment %d --repo %s --body %s`,
		issueNumber, repo, shellQuote(body))
	result, err := Shell(ctx, cmd)
	if err != nil {
		return fmt.Errorf("github comment: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("gh comment failed: %s", result.Stderr)
	}
	return nil
}

// GitHubCloseIssue closes a GitHub issue.
func GitHubCloseIssue(ctx context.Context, repo string, issueNumber int) error {
	cmd := fmt.Sprintf(`gh issue close %d --repo %s`, issueNumber, repo)
	result, err := Shell(ctx, cmd)
	if err != nil {
		return fmt.Errorf("github close: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("gh close failed: %s", result.Stderr)
	}
	return nil
}

// GitHubGetIssue fetches a GitHub issue's details.
func GitHubGetIssue(ctx context.Context, repo string, issueNumber int) (string, error) {
	cmd := fmt.Sprintf(`gh issue view %d --repo %s --json title,body,labels,state`, issueNumber, repo)
	result, err := Shell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("github get issue: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("gh view failed: %s", result.Stderr)
	}
	return result.Stdout, nil
}

// GitHubListIssues lists open issues for a repo.
func GitHubListIssues(ctx context.Context, repo string) (string, error) {
	cmd := fmt.Sprintf(`gh issue list --repo %s --json number,title,labels --limit 50`, repo)
	result, err := Shell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("github list issues: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("gh list failed: %s", result.Stderr)
	}
	return result.Stdout, nil
}

// GitHubGetFileContent fetches a file's raw content from a GitHub repo.
// Returns "" with nil error if the file does not exist.
func GitHubGetFileContent(ctx context.Context, repo, filepath, ref string) (string, error) {
	args := fmt.Sprintf(`gh api repos/%s/contents/%s`, repo, filepath)
	if ref != "" {
		args += fmt.Sprintf(` -f ref=%s`, ref)
	}
	args += ` --jq .content -H "Accept: application/vnd.github.v3+json"`
	result, err := Shell(ctx, args)
	if err != nil {
		return "", fmt.Errorf("github get file: %w", err)
	}
	if result.ExitCode != 0 {
		if strings.Contains(result.Stderr, "404") || strings.Contains(result.Stderr, "Not Found") {
			return "", nil
		}
		return "", fmt.Errorf("gh get file failed: %s", result.Stderr)
	}
	// GitHub API returns base64-encoded content; decode it
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(result.Stdout))
	if err != nil {
		// Fallback: might already be raw
		return result.Stdout, nil
	}
	return string(decoded), nil
}

// GitHubGetIssueComments fetches all comments on a GitHub issue.
func GitHubGetIssueComments(ctx context.Context, repo string, issueNumber int) (string, error) {
	cmd := fmt.Sprintf(`gh api repos/%s/issues/%d/comments --paginate`, repo, issueNumber)
	result, err := Shell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("github get comments: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("gh get comments failed: %s", result.Stderr)
	}
	return result.Stdout, nil
}

// GitHubCreatePullRequest creates a pull request on GitHub.
func GitHubCreatePullRequest(ctx context.Context, repo, title, body, head, base string) error {
	cmd := fmt.Sprintf(`gh pr create --repo %s --title %s --body %s --head %s --base %s`,
		repo, shellQuote(title), shellQuote(body), head, base)
	result, err := Shell(ctx, cmd)
	if err != nil {
		return fmt.Errorf("github create PR: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("gh create PR failed: %s", result.Stderr)
	}
	return nil
}

func shellQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
