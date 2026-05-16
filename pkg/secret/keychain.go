// Package secret reads provider API keys from system secret stores.
//
// The Cortex coding harness needs an OpenRouter API key to drive the
// agent loop. Storing the key in an environment variable works but
// leaks it into every child process's environment — including the
// harness's run_shell tool, which sandboxes Env but reading from the
// parent process is still riskier than necessary. Reading from the
// macOS Keychain on demand keeps the key out of process environments
// entirely.
//
// Resolution order for OpenRouter:
//  1. macOS Keychain entry with service name "cortex-openrouter"
//     (darwin only; via /usr/bin/security)
//  2. OPEN_ROUTER_API_KEY environment variable
//
// The first available source wins. The key is fetched once per
// process via sync.Once and cached in memory; subsequent calls return
// the cached value. The key is NEVER written to logs — diagnostics
// only mention the source and length.
package secret

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ErrNotFound is returned when no source has the requested key.
var ErrNotFound = errors.New("openrouter api key not found")

// openRouterServiceName is the keychain service identifier the user
// stores the key under, per their durable convention:
//
//	security add-generic-password -s cortex-openrouter -a openrouter -w '<key>'
const openRouterServiceName = "cortex-openrouter"

// envOpenRouterKey is the env-var fallback (matches the project's
// existing underscore convention; see pkg/llm/openrouter.go).
const envOpenRouterKey = "OPEN_ROUTER_API_KEY"

var (
	openRouterOnce   sync.Once
	openRouterKey    string
	openRouterSource string
	openRouterErr    error
)

// OpenRouterKey returns the OpenRouter API key plus a short description
// of its source ("keychain" or "env"). The first successful resolution
// is cached for the process lifetime; subsequent calls are cheap.
//
// On error the returned key is empty. The error is never the raw
// keychain failure on systems where the entry is absent — that case
// silently falls through to the env-var lookup.
func OpenRouterKey() (string, string, error) {
	openRouterOnce.Do(func() {
		if key, err := LookupOpenRouterKey(); err == nil && key != "" {
			openRouterKey = key
			openRouterSource = "keychain"
			return
		}
		if v := strings.TrimSpace(os.Getenv(envOpenRouterKey)); v != "" {
			openRouterKey = v
			openRouterSource = "env"
			return
		}
		openRouterErr = ErrNotFound
	})
	return openRouterKey, openRouterSource, openRouterErr
}

// MustOpenRouterKey is OpenRouterKey but returns an error if no source
// has the key. Used by the harness constructor; preferred over a panic.
func MustOpenRouterKey() (string, string, error) {
	key, src, err := OpenRouterKey()
	if err != nil {
		return "", "", fmt.Errorf("%w (tried keychain %q then env %q)", err, openRouterServiceName, envOpenRouterKey)
	}
	return key, src, nil
}

// resetForTest clears the sync.Once cache. Tests use this to exercise
// both the keychain and env paths in one binary run. NOT exported.
func resetForTest() {
	openRouterOnce = sync.Once{}
	openRouterKey = ""
	openRouterSource = ""
	openRouterErr = nil
}
