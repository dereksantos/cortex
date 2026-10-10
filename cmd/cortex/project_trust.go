// project_trust.go — issue #129, round 7: `cortex project trust [add
// <root>|remove <root>|list]`, the operator's explicit, persisted,
// per-workspace trust decision for code-executing project commands.
//
// Trust lives in the USER-level config (~/.cortex/config.json via
// internal/userhome — redirectable in tests with $CORTEX_HOME) under
// project.trusted: a list of workspace root directories. It NEVER lives in
// the repository — not in the project's .cortex/config.json (mergeConfig
// drops the project-level copy, mergeProject), not in AGENTS.md — because
// the repository is the untrusted party and must not be able to mark
// itself trusted. Default: no entry, untrusted.
//
// Editing round-trips the WHOLE user config (every known field, plus
// unknown keys preserved byte-for-key via rawExtra) so adding a trust entry
// never clobbers the operator's backend/models/tools settings.
//
// Argument parsing mirrors scan.go/project.go's manual-switch style; the
// pure functions (trustLoad/trustSave/addTrust/removeTrust/trustList) are
// what's tested — runProjectTrustCLI is the thin os.Exit-driving wrapper
// dispatched from runProjectCLI, the same convention as runScanCLI.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/userhome"
)

// ErrProjectTrustUsage is returned (and printed) when `cortex project
// trust` gets an unrecognized argument.
var ErrProjectTrustUsage = errors.New("usage: cortex project trust <add <root>|remove <root>|list>")

// trustStore is the user config on disk, edited through its full shape:
// every known field round-trips, and rawExtra preserves top-level keys
// that are not part of the Config struct (forward-compatible: an
// unrecognized key must not be dropped by a trust edit).
type trustStore struct {
	Config
	rawExtra map[string]json.RawMessage
}

// trustLoad reads the user-level config as the trust store. A missing or
// empty file is an empty store, not an error (trust's safe default is
// "nothing trusted"); a malformed file is a hard error — silently dropping
// an operator's hand-edited config would lose their trust decisions.
func trustLoad() (*trustStore, string, error) {
	path, err := userhome.Path("config.json")
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve the user config path: %w", err)
	}
	data, rerr := os.ReadFile(path)
	if errors.Is(rerr, os.ErrNotExist) || len(data) == 0 {
		return &trustStore{rawExtra: map[string]json.RawMessage{}}, path, nil
	}
	if rerr != nil {
		return nil, "", fmt.Errorf("failed to read %s: %w", path, rerr)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(data, &full); err != nil {
		return nil, "", fmt.Errorf("failed to parse %s (a malformed user config is refused here rather than overwritten): %w", path, err)
	}
	store := &trustStore{rawExtra: map[string]json.RawMessage{}}
	for k, v := range full {
		if !knownConfigKey(k) {
			store.rawExtra[k] = v
		}
	}
	if err := json.Unmarshal(data, &store.Config); err != nil {
		return nil, "", fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return store, path, nil
}

// knownConfigKey reports whether k is a top-level key of the Config struct
// (the set trustLoad keeps out of rawExtra). Kept in sync with Config's
// JSON tags by the test TestTrustStorePreservesUnknownKeys.
var knownConfigKeys = map[string]bool{
	"backend":     true,
	"models":      true,
	"tools":       true,
	"temperature": true,
	"subagents":   true,
	"limits":      true,
	"network":     true,
	"serve":       true,
	"repl":        true,
	"discord":     true,
	"prompt":      true,
	"context":     true,
	"skills":      true,
	"project":     true,
}

func knownConfigKey(k string) bool { return knownConfigKeys[k] }

// trustSave persists the store back to the user config, creating the parent
// directory if needed. Known fields marshal from the Config struct;
// rawExtra keys are merged in (unknown keys win on a collision — there are
// none, by construction).
func trustSave(store *trustStore, path string) error {
	out, err := json.Marshal(store.Config)
	if err != nil {
		return fmt.Errorf("failed to marshal the user config: %w", err)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(out, &full); err != nil {
		return fmt.Errorf("failed to reparse the marshaled config: %w", err)
	}
	for k, v := range store.rawExtra {
		full[k] = v
	}
	data, err := json.Marshal(full)
	if err != nil {
		return fmt.Errorf("failed to marshal the user config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// trustList renders the trust list for `cortex project trust list` — one
// root per line, absolute and sorted, or a "nothing trusted" message.
func trustList(roots []string) string {
	if len(roots) == 0 {
		return "No workspaces trusted. Trust one: cortex project trust add <root>\n"
	}
	sorted := append([]string(nil), roots...)
	sort.Strings(sorted)
	var b strings.Builder
	fmt.Fprintf(&b, "Trusted workspaces (%d):\n", len(sorted))
	for _, r := range sorted {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	return b.String()
}

// addTrust upserts root (resolved to an absolute path, as typed at a
// shell) into the store's trust list. Idempotent: an existing identical
// entry is not duplicated.
func addTrust(store *trustStore, root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("add <root>")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to resolve root %s: %w", root, err)
	}
	for _, e := range store.Project.Trusted {
		if e == abs {
			return abs, nil
		}
	}
	store.Project.Trusted = append(store.Project.Trusted, abs)
	return abs, nil
}

// removeTrust drops root from the store's trust list. A root that is not
// on the list is an error (a typo'd remove must not silently succeed).
func removeTrust(store *trustStore, root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("remove <root>")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("failed to resolve root %s: %w", root, err)
	}
	kept := make([]string, 0, len(store.Project.Trusted))
	found := false
	for _, e := range store.Project.Trusted {
		if e == abs {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return fmt.Errorf("not trusted: %s (cortex project trust list)", abs)
	}
	store.Project.Trusted = kept
	return nil
}

// runProjectTrustCLI is the `cortex project trust <add|remove|list> ...`
// entry point (dispatched from runProjectCLI). Not directly unit-tested —
// matches the established convention of leaving the os.Exit-driving
// wrapper untested while its composed pure functions carry the coverage.
func runProjectTrustCLI(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "project trust:", ErrProjectTrustUsage)
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		store, _, err := trustLoad()
		if err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		fmt.Print(trustList(store.Project.Trusted))
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "project trust:", ErrProjectTrustUsage)
			os.Exit(1)
		}
		store, path, err := trustLoad()
		if err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		abs, err := addTrust(store, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		if err := trustSave(store, path); err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		fmt.Printf("trusted %s (user config: %s)\n", abs, path)
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "project trust:", ErrProjectTrustUsage)
			os.Exit(1)
		}
		store, path, err := trustLoad()
		if err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		if err := removeTrust(store, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		if err := trustSave(store, path); err != nil {
			fmt.Fprintln(os.Stderr, "project trust:", err)
			os.Exit(1)
		}
		fmt.Printf("untrusted %s (user config: %s)\n", resolveAbs(args[1]), path)
	default:
		fmt.Fprintln(os.Stderr, "project trust:", ErrProjectTrustUsage)
		os.Exit(1)
	}
}

// resolveAbs resolves root to an absolute path for a confirmation message,
// mirroring how addTrust/removeTrust normalize their arguments; a
// resolution failure (no CWD) falls back to the input as typed.
func resolveAbs(root string) string {
	if a, err := filepath.Abs(root); err == nil {
		return a
	}
	return root
}
