package tools

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// shellenv.go keeps model-provider secrets out of the agent's shell.
//
// The bash tool runs `bash -c <command>` as a child of the cortex process, so
// by default it inherits cortex's whole environment — including the API key a
// config names via `key_env`. One `env` (or a stack trace that prints it)
// would copy the key into the tool result, the session transcript, and any
// trajectory published from it. The key is cortex's credential, not the
// workspace's: the shell never needs it, so it is removed from the child's
// environment rather than trusted not to be printed.
//
// Process-wide like Limits (limits.go): NewCortexSession sets it once from
// config before any tool call.

var (
	shellSecretMu  sync.RWMutex
	shellSecretEnv map[string]bool
)

// SetShellSecretEnv names the environment variables the bash tool must not
// pass to its shell. Empty names are ignored; nil/empty clears the set.
func SetShellSecretEnv(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	shellSecretMu.Lock()
	shellSecretEnv = set
	shellSecretMu.Unlock()
}

// ShellSecretEnv returns the configured names, sorted (for tests/diagnostics).
func ShellSecretEnv() []string {
	shellSecretMu.RLock()
	defer shellSecretMu.RUnlock()
	out := make([]string, 0, len(shellSecretEnv))
	for n := range shellSecretEnv {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// shellEnv is the environment for a bash-tool child: the process environment
// minus every configured secret. It returns nil (meaning "inherit unchanged",
// exec.Cmd's default) when nothing is configured, so the common case is
// byte-identical to before.
func shellEnv() []string {
	shellSecretMu.RLock()
	deny := shellSecretEnv
	shellSecretMu.RUnlock()
	if len(deny) == 0 {
		return nil
	}
	return scrubEnv(os.Environ(), deny)
}

// scrubEnv drops every KEY=VALUE entry whose KEY is in deny.
func scrubEnv(env []string, deny map[string]bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if deny[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
