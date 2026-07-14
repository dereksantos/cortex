//go:build darwin

package secret

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// securityBinary is the absolute path to macOS's `security` CLI.
// Hardcoded so the lookup never falls back to PATH (which would let a
// malicious binary on PATH intercept the call). This file is darwin-only
// so the path is stable.
const securityBinary = "/usr/bin/security"

// LookupOpenRouterKey shells `security find-generic-password -s
// cortex-openrouter -w` and returns the key on success. On any failure
// (entry missing, security not installed, permission denied) returns
// "" with an error so callers can fall back to the env-var path
// without surfacing the keychain error to users — keychain-absent is
// a normal state on a fresh machine.
//
// The returned key is NOT logged here; callers that want to record
// resolution behavior should log only `len(key)` and the source.
func LookupOpenRouterKey() (string, error) {
	cmd := exec.Command(securityBinary, "find-generic-password", "-s", openRouterServiceName, "-w")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Don't include stderr in the wrapped error — `security`
		// prints the key-not-found message to stderr in plaintext,
		// which would noisily appear in any log line that includes
		// the error. The bare exit-status error is enough signal
		// for fallback logic.
		return "", fmt.Errorf("security exited: %w", err)
	}

	key := strings.TrimRight(stdout.String(), "\n\r")
	if key == "" {
		return "", fmt.Errorf("security returned empty key for service %q", openRouterServiceName)
	}
	return key, nil
}

// darwinStore implements Store via the macOS `security` CLI. It is
// never exercised by automated tests directly (see meta_test.go) —
// operators verify it manually.
type darwinStore struct{}

// NewStore returns the darwin Store implementation.
func NewStore() Store { return darwinStore{} }

// Get implements Store by shelling `security find-generic-password`.
func (darwinStore) Get(service string) (string, error) {
	cmd := exec.Command(securityBinary, "find-generic-password", "-s", service, "-w")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Don't include stderr: `security` prints its not-found message
		// to stderr in plaintext, which would noisily appear in any log
		// line that includes the error.
		return "", fmt.Errorf("%w: security exited: %v", ErrNotFound, err)
	}

	value := strings.TrimRight(stdout.String(), "\n\r")
	if value == "" {
		return "", fmt.Errorf("%w: security returned empty value for service %q", ErrNotFound, service)
	}
	return value, nil
}

// Set implements Store by shelling `security add-generic-password -U`
// (the -U flag updates an existing entry in place rather than erroring
// on a duplicate).
func (darwinStore) Set(service, account, value string) error {
	cmd := exec.Command(securityBinary, "add-generic-password", "-U", "-s", service, "-a", account, "-w", value)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Deliberately omit stderr from the wrapped error: on some
		// configurations `security` echoes the -w value into
		// diagnostic output, which would leak the secret into logs.
		return fmt.Errorf("security add-generic-password exited: %w", err)
	}
	return nil
}
