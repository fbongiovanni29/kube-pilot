package bootstrap

import "testing"

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		input      string
		wantOwner  string
		wantRepo   string
	}{
		{"kube-pilot/context", "kube-pilot", "context"},
		{"org/repo", "org", "repo"},
		{"noslash", "noslash", "noslash"},
	}
	for _, tt := range tests {
		owner, repo := splitRepo(tt.input)
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("splitRepo(%q) = (%q, %q), want (%q, %q)",
				tt.input, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestPortFromAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{":8080", "8080"},
		{"0.0.0.0:9090", "9090"},
		{"localhost:3000", "3000"},
		{"nocolon", "8080"},
	}
	for _, tt := range tests {
		got := portFromAddress(tt.input)
		if got != tt.want {
			t.Errorf("portFromAddress(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTrimScheme(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://gitea.local:3000", "gitea.local:3000"},
		{"https://gitea.local:3000", "gitea.local:3000"},
		{"gitea.local:3000", "gitea.local:3000"},
	}
	for _, tt := range tests {
		got := trimScheme(tt.input)
		if got != tt.want {
			t.Errorf("trimScheme(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
