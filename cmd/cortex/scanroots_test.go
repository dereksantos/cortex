package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestParseScanRootsReplyExtractsPaths pins parseScanRootsReply's
// path-extraction heuristic: bare paths, paths embedded in prose, multiple
// paths, and trailing punctuation left over from a sentence.
func TestParseScanRootsReplyExtractsPaths(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  []string
	}{
		{"bare tilde path", "~/code", []string{"~/code"}},
		{"bare absolute path", "/Users/derek/eng", []string{"/Users/derek/eng"}},
		{"embedded in prose with trailing period", "my code lives at ~/eng.", []string{"~/eng"}},
		{"two paths in prose", "mostly ~/eng and ~/code, thanks!", []string{"~/eng", "~/code"}},
		{"relative path", "it's under ./projects", []string{"./projects"}},
		{"decline has no path", "no thanks", nil},
		{"empty reply", "", nil},
		{"duplicate paths dedup", "~/code, also ~/code again", []string{"~/code"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseScanRootsReply(tt.reply)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseScanRootsReply(%q) = %#v, want %#v", tt.reply, got, tt.want)
			}
		})
	}
}

// TestPersistScanRootsRoundTrip pins PersistScanRoots' read-modify-write
// invariant (GOAL.md §2): other top-level fields survive untouched, and the
// persisted "scan.roots" value round-trips.
func TestPersistScanRootsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"tools": {"allow_delete": false}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := PersistScanRoots(path, []string{"~/code", "~/projects"}); err != nil {
		t.Fatalf("PersistScanRoots: %v", err)
	}

	doc, err := readJSONDoc(path)
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	if !jsonEqual(t, doc["tools"], []byte(`{"allow_delete": false}`)) {
		t.Errorf("tools field mutated: %s", doc["tools"])
	}

	var scan struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(doc["scan"], &scan); err != nil {
		t.Fatalf("unmarshal scan: %v", err)
	}
	want := []string{"~/code", "~/projects"}
	if !reflect.DeepEqual(scan.Roots, want) {
		t.Errorf("scan.roots = %#v, want %#v", scan.Roots, want)
	}
}

// TestMaybeCaptureScanRootsNoopWhenNotAwaiting pins that a session which
// never had a first-run greeting (awaitingScanRootsReply unset) ignores any
// input passed to MaybeCaptureScanRoots — an ordinary chat message
// mentioning a path must never be mistaken for the scan-roots answer.
func TestMaybeCaptureScanRootsNoopWhenNotAwaiting(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	cs := &CortexSession{quiet: true}
	persisted, err := cs.MaybeCaptureScanRoots("~/code")
	if err != nil {
		t.Fatalf("MaybeCaptureScanRoots: %v", err)
	}
	if persisted {
		t.Fatal("MaybeCaptureScanRoots persisted = true when awaitingScanRootsReply was never armed, want false")
	}
	if pathExists(userConfigPath()) {
		t.Fatal("config file written despite awaitingScanRootsReply being unset")
	}
}

// TestMaybeCaptureScanRootsPersistsAndClearsFlag pins the happy path: once
// armed, a reply naming a path is persisted to user config and the flag is
// cleared so a second call (standing in for the next chat turn) is a no-op
// even if it also happens to mention a path.
func TestMaybeCaptureScanRootsPersistsAndClearsFlag(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	cs := &CortexSession{quiet: true, awaitingScanRootsReply: true}
	persisted, err := cs.MaybeCaptureScanRoots("sure, it's at ~/eng")
	if err != nil {
		t.Fatalf("MaybeCaptureScanRoots: %v", err)
	}
	if !persisted {
		t.Fatal("MaybeCaptureScanRoots persisted = false for a reply naming a path, want true")
	}
	if cs.awaitingScanRootsReply {
		t.Fatal("awaitingScanRootsReply still true after a captured reply, want cleared")
	}

	doc, err := readJSONDoc(userConfigPath())
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	var scan struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(doc["scan"], &scan); err != nil {
		t.Fatalf("unmarshal scan: %v", err)
	}
	if want := []string{"~/eng"}; !reflect.DeepEqual(scan.Roots, want) {
		t.Errorf("scan.roots = %#v, want %#v", scan.Roots, want)
	}

	// Second call must be a no-op: the flag was cleared, so even a
	// path-bearing reply here is an ordinary chat message, not the answer.
	persisted, err = cs.MaybeCaptureScanRoots("~/other")
	if err != nil {
		t.Fatalf("second MaybeCaptureScanRoots: %v", err)
	}
	if persisted {
		t.Fatal("second MaybeCaptureScanRoots persisted = true, want false (flag was cleared)")
	}
}

// TestMaybeCaptureScanRootsDeclineClearsFlagWithoutPersisting pins the
// decline path: a reply with no path-like token clears the flag (asked
// once, not re-asked forever) but writes nothing.
func TestMaybeCaptureScanRootsDeclineClearsFlagWithoutPersisting(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())

	cs := &CortexSession{quiet: true, awaitingScanRootsReply: true}
	persisted, err := cs.MaybeCaptureScanRoots("no thanks, not right now")
	if err != nil {
		t.Fatalf("MaybeCaptureScanRoots: %v", err)
	}
	if persisted {
		t.Fatal("MaybeCaptureScanRoots persisted = true for a decline reply, want false")
	}
	if cs.awaitingScanRootsReply {
		t.Fatal("awaitingScanRootsReply still true after a decline, want cleared")
	}
	if pathExists(userConfigPath()) {
		t.Fatal("config file written despite a decline reply")
	}
}

// TestMaybeGreetArmsAwaitingScanRootsReply pins M1.7's wiring into
// MaybeGreet (M1.5): a successful first-run greeting turn — driven through
// the same scripted-Sender-via-httptest harness as greeting_test.go — arms
// awaitingScanRootsReply so the REPL's next real input is captured.
func TestMaybeGreetArmsAwaitingScanRootsReply(t *testing.T) {
	t.Setenv("CORTEX_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	backend := newGreetingTestBackend(t)
	cs := newGreetingTestSession(t, backend)

	if err := MaybeGreet(t.Context(), cs); err != nil {
		t.Fatalf("MaybeGreet: %v", err)
	}
	if !cs.awaitingScanRootsReply {
		t.Fatal("awaitingScanRootsReply = false after a successful first-run greeting, want true")
	}

	// End to end: the REPL's next call with the human's actual answer
	// persists it, exactly as main.go's read loop will drive it.
	persisted, err := cs.MaybeCaptureScanRoots("~/eng")
	if err != nil {
		t.Fatalf("MaybeCaptureScanRoots: %v", err)
	}
	if !persisted {
		t.Fatal("MaybeCaptureScanRoots persisted = false after a first-run greeting, want true")
	}
	doc, err := readJSONDoc(userConfigPath())
	if err != nil {
		t.Fatalf("readJSONDoc: %v", err)
	}
	var scan struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(doc["scan"], &scan); err != nil {
		t.Fatalf("unmarshal scan: %v", err)
	}
	if want := []string{"~/eng"}; !reflect.DeepEqual(scan.Roots, want) {
		t.Errorf("scan.roots = %#v, want %#v", scan.Roots, want)
	}
}
