package journal

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LocalOnly is the design invariant that journal contents (file content,
// LLM outputs, user prompts, source code) never leave the local machine
// by default. Any code path that would upload, sync, or otherwise
// transmit data from a .cortex/journal/ path must explicitly call
// AssertLocalOnly with the path it intends to operate on — returning
// the error short-circuits the operation.
//
// This is a tripwire, not a sandbox. A determined caller can ignore the
// returned error and proceed. The point is to make outbound use of
// journal data require a deliberate decision visible in code review.
//
// See principle 6 in docs/journal.md.

// AssertLocalOnly returns a non-nil error if path is under a journal
// directory (any segment of the path contains "journal" surrounded by
// path separators or at a path boundary). Callers that genuinely need
// to share journal data — e.g. a future opt-in `cortex journal export
// --redacted` command — should bypass this with a documented rationale.
func AssertLocalOnly(path string) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(clean, "/")
	for _, p := range parts {
		if p == "journal" {
			return fmt.Errorf("journal: %s is under a journal directory — "+
				"local-only by design (principle 6 in docs/journal.md)", path)
		}
	}
	return nil
}
