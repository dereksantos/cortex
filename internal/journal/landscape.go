package journal

import (
	"encoding/json"
	"fmt"

	"github.com/dereksantos/cortex/internal/userhome"
)

// TypeLandscapeScan is the entry type for a completed landscape survey
// (harnesses/runtimes/projects found under the user's home + scan roots).
// It is written to the user-level journal — rooted under internal/userhome,
// independent of any project's .cortex/journal — by both `cortex scan`
// (cmd/cortex/scan.go) and the scan_landscape coder tool
// (internal/tools/scan_landscape.go). See GOAL.md §3 P2 / M2.7.
const TypeLandscapeScan = "landscape.scan"

// LandscapeScanPayload is names-and-counts only — never file contents, and
// never anything beyond the scanned roots themselves — extending M2.3's
// content-non-leak invariant to journal payloads.
type LandscapeScanPayload struct {
	Roots        []string `json:"roots,omitempty"`
	ToolCount    int      `json:"tool_count"`
	RuntimeCount int      `json:"runtime_count"`
	ProjectCount int      `json:"project_count"`
	Truncated    bool     `json:"truncated"`
}

// NewLandscapeScanEntry builds a journal entry for one landscape.scan event.
func NewLandscapeScanEntry(p LandscapeScanPayload) (*Entry, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal landscape.scan: %w", err)
	}
	return &Entry{Type: TypeLandscapeScan, V: 1, Payload: data}, nil
}

// ParseLandscapeScan decodes a landscape.scan entry's payload.
func ParseLandscapeScan(e *Entry) (*LandscapeScanPayload, error) {
	if e.Type != TypeLandscapeScan {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeLandscapeScan)
	}
	var p LandscapeScanPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse landscape.scan: %w", err)
	}
	return &p, nil
}

// AppendLandscapeScan opens the user-level landscape journal and appends one
// landscape.scan event. Both `cortex scan` and the scan_landscape coder tool
// call this directly (neither can import the other's package — cmd/cortex
// and internal/tools would cycle, per M2.6's Decisions note) so there is
// exactly one write path and class-dir convention for the event, rather than
// each caller inventing its own. FsyncPerBatch matches the existing
// convention for regeneratable/derived events (docs/journal.md principle 4)
// — a landscape.scan event is reproducible by re-running the scan.
func AppendLandscapeScan(p LandscapeScanPayload) error {
	dir, err := userhome.Path("journal", "landscape")
	if err != nil {
		return fmt.Errorf("journal: resolve user-level landscape journal dir: %w", err)
	}
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		return fmt.Errorf("journal: open user-level landscape journal: %w", err)
	}
	defer w.Close()

	entry, err := NewLandscapeScanEntry(p)
	if err != nil {
		return err
	}
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("journal: append landscape.scan: %w", err)
	}
	return nil
}
