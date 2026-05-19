package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellPolicy_Check covers the four-case matrix:
// - empty policy permits all
// - deny pattern rejects matching commands, accepts others
// - non-empty allow requires a match
// - deny takes precedence over allow when both match
func TestShellPolicy_Check(t *testing.T) {
	tests := []struct {
		name    string
		policy  ShellPolicy
		command string
		wantErr bool
	}{
		{
			name:    "empty policy allows anything",
			policy:  ShellPolicy{},
			command: "rm -rf /",
			wantErr: false,
		},
		{
			name:    "deny pattern rejects match",
			policy:  ShellPolicy{Deny: []string{"rm -rf"}},
			command: "rm -rf /tmp/foo",
			wantErr: true,
		},
		{
			name:    "deny pattern lets non-match through",
			policy:  ShellPolicy{Deny: []string{"rm -rf"}},
			command: "ls -la",
			wantErr: false,
		},
		{
			name:    "non-empty allow requires match",
			policy:  ShellPolicy{Allow: []string{"go test"}},
			command: "go build ./...",
			wantErr: true,
		},
		{
			name:    "allow pattern lets matching command through",
			policy:  ShellPolicy{Allow: []string{"go test"}},
			command: "go test ./...",
			wantErr: false,
		},
		{
			name:    "deny wins over allow when both match",
			policy:  ShellPolicy{Allow: []string{"go"}, Deny: []string{"go test"}},
			command: "go test ./...",
			wantErr: true,
		},
		{
			name:    "empty patterns in slices are skipped",
			policy:  ShellPolicy{Allow: []string{"", "npm"}, Deny: []string{""}},
			command: "npm test",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Check(tt.command)
			if tt.wantErr && err == nil {
				t.Errorf("Check(%q) → nil, want error", tt.command)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Check(%q) → %v, want nil", tt.command, err)
			}
		})
	}
}

// TestLoadShellPolicy_PerWorkdirWins ensures the per-workdir file
// takes precedence over a global $HOME file.
func TestLoadShellPolicy_PerWorkdirWins(t *testing.T) {
	tmpHome := t.TempDir()
	tmpWork := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writePolicy(t, filepath.Join(tmpHome, ".cortex", "shell-policy.json"),
		ShellPolicy{Deny: []string{"global-only"}})
	writePolicy(t, filepath.Join(tmpWork, ".cortex", "shell-policy.json"),
		ShellPolicy{Deny: []string{"workdir-only"}})

	got := LoadShellPolicy(tmpWork)
	if len(got.Deny) != 1 || got.Deny[0] != "workdir-only" {
		t.Errorf("workdir policy didn't win; got Deny=%v", got.Deny)
	}
}

// TestLoadShellPolicy_FallsBackToHome ensures the global file fires
// when no workdir policy exists.
func TestLoadShellPolicy_FallsBackToHome(t *testing.T) {
	tmpHome := t.TempDir()
	tmpWork := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writePolicy(t, filepath.Join(tmpHome, ".cortex", "shell-policy.json"),
		ShellPolicy{Deny: []string{"home-only"}})
	// No file under tmpWork/.cortex/.

	got := LoadShellPolicy(tmpWork)
	if len(got.Deny) != 1 || got.Deny[0] != "home-only" {
		t.Errorf("home policy didn't apply; got Deny=%v", got.Deny)
	}
}

// TestLoadShellPolicy_EmptyDefault confirms the resolver falls all
// the way through to the zero-value when nothing is configured.
func TestLoadShellPolicy_EmptyDefault(t *testing.T) {
	tmpHome := t.TempDir()
	tmpWork := t.TempDir()
	t.Setenv("HOME", tmpHome)

	got := LoadShellPolicy(tmpWork)
	if !got.IsEmpty() {
		t.Errorf("expected empty policy; got %+v", got)
	}
}

// TestLoadShellPolicy_MalformedJSONIgnored — a broken policy file is
// silently skipped (resolver falls through). Better than blowing up
// the REPL at startup over a user typo.
func TestLoadShellPolicy_MalformedJSONIgnored(t *testing.T) {
	tmpWork := t.TempDir()
	cortexDir := filepath.Join(tmpWork, ".cortex")
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cortexDir, "shell-policy.json"),
		[]byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// No HOME fallback either, so the resolver should hit the empty
	// default rather than panicking on parse error.
	t.Setenv("HOME", "")

	got := LoadShellPolicy(tmpWork)
	if !got.IsEmpty() {
		t.Errorf("expected empty policy on malformed json; got %+v", got)
	}
}

func writePolicy(t *testing.T, path string, p ShellPolicy) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// _ helper to keep strings import alive if future tests use it.
var _ = strings.Contains
