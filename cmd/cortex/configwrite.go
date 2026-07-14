// configwrite.go — generic read-modify-write helpers over JSON config
// files. GOAL.md §2: "config files are user-authored: ALL config
// writes ... are read-modify-write over map[string]any / json.RawMessage,
// never marshal-through-struct. Unknown fields in the existing file
// must survive a write byte-for-byte modulo the touched key." Using
// json.RawMessage (rather than a deep map[string]any) means every
// untouched field's exact bytes — including nested formatting —
// round-trip unchanged; only the path actually written is re-marshaled.
//
// First consumer: M1.3's backend persist (bootstrap_persist.go).
// Reserved for M4.2's scoped role-binding writes (same invariant).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readJSONDoc parses path as a top-level JSON object of raw fields. A
// missing file yields an empty (non-nil) document rather than an error,
// so callers can read-modify-write a config file that doesn't exist yet.
func readJSONDoc(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	doc := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, nil
}

// writeJSONDoc marshals doc back to path, creating the parent directory
// if needed. The file is written user-only (0o600) since config may
// carry key material by convention elsewhere in the codebase.
func writeJSONDoc(path string, doc map[string]json.RawMessage) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", path, err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// setJSONPath sets doc[path[0]][path[1]]...[path[len-1]] = value,
// creating intermediate JSON objects as needed. Every sibling field at
// every level along the path is preserved byte-for-byte (each level is
// decoded only into map[string]json.RawMessage, never into a typed
// struct, so fields this package doesn't know about pass through
// untouched). path must be non-empty; intermediate path elements whose
// existing value isn't a JSON object is an error (refuses to clobber
// e.g. a string where an object is expected).
func setJSONPath(doc map[string]json.RawMessage, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("setJSONPath: empty path")
	}
	key := path[0]
	if len(path) == 1 {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for %q: %w", key, err)
		}
		doc[key] = raw
		return nil
	}
	inner := map[string]json.RawMessage{}
	if raw, ok := doc[key]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &inner); err != nil {
			return fmt.Errorf("failed to parse %q as an object: %w", key, err)
		}
	}
	if err := setJSONPath(inner, path[1:], value); err != nil {
		return err
	}
	raw, err := json.Marshal(inner)
	if err != nil {
		return fmt.Errorf("failed to marshal %q: %w", key, err)
	}
	doc[key] = raw
	return nil
}
