// scanroots.go — M1.7: D3's ask-and-persist half. The greeting turn
// (greeting.go) asks where the user's code lives; this file captures the
// user's next real reply (in main.go's read loop) and persists it to user
// config so M2.5's `cortex scan` can consume it without a blind $HOME
// sweep. Deliberately NOT another LLM round-trip: the model already asked
// the question in its own reply (M1.7's golden-pinned greetingPrompt
// extension); the *answer* is the human's own next line of text, so
// capturing it is plain deterministic parsing, not something a scripted
// backend needs to be involved in. See STATE.md's Decisions Log entry for
// M1.7 for the full reasoning.
package main

import (
	"fmt"
	"regexp"
)

// scanRootPattern matches path-like tokens embedded anywhere in free text:
// a leading "~", "/", or "./"/"../" followed by any non-whitespace,
// non-comma run. This lets a reply like "my code lives at ~/eng and
// ~/code, thanks!" yield ["~/eng", "~/code"] without requiring the user to
// type a bare, structured list.
var scanRootPattern = regexp.MustCompile(`(~[^\s,]*|\.{1,2}/[^\s,]*|/[^\s,]+)`)

// parseScanRootsReply extracts candidate scan roots from a raw user reply.
// Trailing punctuation commonly left over from prose (a period or comma
// stuck to the end of a path) is trimmed. Returns nil (never persisted) when
// no path-like token is found — e.g. "no thanks" declines cleanly.
func parseScanRootsReply(reply string) []string {
	matches := scanRootPattern.FindAllString(reply, -1)
	seen := map[string]bool{}
	var roots []string
	for _, m := range matches {
		root := trimTrailingPunct(m)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots
}

func trimTrailingPunct(s string) string {
	for len(s) > 0 {
		last := s[len(s)-1]
		if last == '.' || last == ',' || last == '!' || last == '?' || last == ';' || last == ':' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// PersistScanRoots writes roots to the user config file at path via
// read-modify-write (configwrite.go): every other top-level field, and
// every other field under "scan" if one is ever added, survives untouched
// (GOAL.md §2). Only "scan.roots" is touched.
func PersistScanRoots(path string, roots []string) error {
	doc, err := readJSONDoc(path)
	if err != nil {
		return fmt.Errorf("failed to persist scan roots: %w", err)
	}
	if err := setJSONPath(doc, []string{"scan", "roots"}, roots); err != nil {
		return fmt.Errorf("failed to persist scan roots: %w", err)
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return fmt.Errorf("failed to persist scan roots: %w", err)
	}
	return nil
}

// MaybeCaptureScanRoots is called by the REPL read loop (main.go) with the
// user's next raw input after a first-run greeting. It is a no-op unless
// MaybeGreet just armed cs.awaitingScanRootsReply (i.e. this is that first
// reply). A reply with no path-like token is treated as a decline: the
// flag is still cleared (asked once, not re-asked) but nothing is
// persisted. Returns whether roots were persisted.
func (cs *CortexSession) MaybeCaptureScanRoots(reply string) (bool, error) {
	if !cs.awaitingScanRootsReply {
		return false, nil
	}
	cs.awaitingScanRootsReply = false
	roots := parseScanRootsReply(reply)
	if len(roots) == 0 {
		return false, nil
	}
	if err := PersistScanRoots(userConfigPath(), roots); err != nil {
		return false, err
	}
	return true, nil
}
