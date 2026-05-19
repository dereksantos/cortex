// Package harness — user-configurable shell policy.
//
// The iter-7 refactor dropped the binary allowlist in favour of real
// bash -c semantics. Some users still want guardrails — "don't let
// the model run curl", or conversely "only ever let it run npm". The
// shell policy is the user-controlled surface for that.
//
// Resolution order:
//
//  1. <workdir>/.cortex/shell-policy.json (per-project, takes precedence)
//  2. $HOME/.cortex/shell-policy.json     (global default)
//  3. empty policy (allow everything; the default)
//
// Schema:
//
//	{
//	  "deny":  ["rm -rf", "curl ", "wget "],
//	  "allow": ["npm ", "node ", "git "]
//	}
//
// Matching is substring against the full bash command string. The
// user picks the granularity — "curl" matches curl-anything; "curl
// http://safe.example" matches only that prefix. Empty Allow means
// "no allowlist enforcement" (deny still applies); non-empty Allow
// means commands must match at least one pattern.
package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ShellPolicy is the user-configurable allow/deny set for run_shell.
// Empty zero-value allows everything — that's the default when no
// config file is present.
type ShellPolicy struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// LoadShellPolicy walks the resolution order described in the package
// doc and returns the first matching policy (per-workdir → global →
// empty). Missing files are not errors; the empty policy means "no
// guardrails", which is the deliberate default.
//
// $HOME is read from the os env (not config) so users can override
// with HOME=... for testing.
func LoadShellPolicy(workdir string) ShellPolicy {
	if workdir != "" {
		if p, ok := tryLoadShellPolicy(filepath.Join(workdir, ".cortex", "shell-policy.json")); ok {
			return p
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		if p, ok := tryLoadShellPolicy(filepath.Join(home, ".cortex", "shell-policy.json")); ok {
			return p
		}
	}
	return ShellPolicy{}
}

// tryLoadShellPolicy reads + parses one path. Returns (policy, true)
// on success; (zero, false) on any read or parse failure (file
// missing, malformed JSON, etc.). Failures are silent — the resolver
// falls through to the next path or the empty default.
func tryLoadShellPolicy(path string) (ShellPolicy, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ShellPolicy{}, false
	}
	var p ShellPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		return ShellPolicy{}, false
	}
	return p, true
}

// Check returns nil when command is permitted by this policy, or an
// error explaining the violation otherwise. Substring match against
// the full bash command string. Deny is checked first (any match →
// reject); then Allow is enforced only if non-empty (command must
// match ≥1 pattern). An empty policy permits everything.
func (p ShellPolicy) Check(command string) error {
	for _, pat := range p.Deny {
		if pat == "" {
			continue
		}
		if strings.Contains(command, pat) {
			return fmt.Errorf("shell policy: command matches deny pattern %q", pat)
		}
	}
	if len(p.Allow) > 0 {
		matched := false
		for _, pat := range p.Allow {
			if pat == "" {
				continue
			}
			if strings.Contains(command, pat) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("shell policy: command matches no allow pattern (configured: %v)", p.Allow)
		}
	}
	return nil
}

// IsEmpty reports whether the policy has no rules — used for diag
// output ("no policy active" vs "N rules active").
func (p ShellPolicy) IsEmpty() bool {
	return len(p.Allow) == 0 && len(p.Deny) == 0
}
