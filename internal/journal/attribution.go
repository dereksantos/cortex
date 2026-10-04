package journal

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dereksantos/cortex/internal/userhome"
)

// TypeAttributionCommit is the entry type for one observed `git commit` and
// whether it carried the configured attribution trailer (issue #146, the
// follow-up to #94 item 4 / PR #127). #127 recorded the fact only for
// loop-firing commits (loop.run's `attributed` field); this class records it
// for every commit path the harness sees — the agent's own `bash` git commits
// and `cortex change commit` — so compliance is measured rather than assumed.
//
// One entry type carries both stages of the fact:
//
//   - the INTENT, as the backstop classified it (Outcome — added, or why it
//     was left alone), written before the command runs; and
//   - the COMPLIANCE FACT, written after the commit succeeded: the resulting
//     SHA and whether `git log -1 --format=%B` actually contains the trailer
//     (TrailerPresent, with Verified true).
//
// A reader wanting "did this commit carry the trailer?" reads the entry with
// Verified set; an entry with Verified false is an intent that never got
// confirmed (the command failed, or nothing was journaled after it).
//
// Written to the user-level journal — rooted under internal/userhome,
// independent of any project's .cortex/journal — matching loop.go's and
// landscape.go's convention: the event describes agent activity across all
// of the machine's projects, and a scratch temp repo (the bash tool can
// commit in one) has no project journal to land in. Journal writes stay
// local (principle 6; journal.AssertLocalOnly).
const TypeAttributionCommit = "attribution.commit"

// Attribution outcome values: what the commit-attribution backstop decided
// about a command it recognized as a git commit. The names mirror the six
// cases internal/tools/attribution.go distinguishes.
const (
	// AttributionOutcomeAdded: the backstop spliced --trailer='…' into the
	// command (one simple `git commit …` without the trailer).
	AttributionOutcomeAdded = "added"
	// AttributionOutcomeAlreadyPresent: the command already carried the
	// trailer text, so it was left unchanged.
	AttributionOutcomeAlreadyPresent = "already_present"
	// AttributionOutcomeSkippedUnparseable: the command mentions git commit
	// but isn't one simple command the backstop dares rewrite (a pipeline,
	// a chain, a redirection, `git -C dir commit`, …).
	AttributionOutcomeSkippedUnparseable = "skipped_unparseable"
	// AttributionOutcomeSkippedAmend: `--amend` — the amended message may
	// already be attributed, or may not be the agent's.
	AttributionOutcomeSkippedAmend = "skipped_amend"
	// AttributionOutcomeSkippedStdin: the message comes from stdin (-F - /
	// --file=-), so a trailer flag would be ignored anyway.
	AttributionOutcomeSkippedStdin = "skipped_stdin"
	// AttributionOutcomeDisabled: attribution is off (no trailer resolves),
	// recorded so the "off" periods are visible in the record too.
	AttributionOutcomeDisabled = "disabled"
)

// AttributionCommitPayload is one observed commit-attribution event. Session
// and Turn place it in the agent transcript (the same coordinate pair
// capture.event carries); Project is the workspace it was made in ("" when
// the observer knows only the working directory). Command is the verbatim
// command as it was observed — for OutcomeAdded that is the REWRITTEN
// command, the one that actually ran.
type AttributionCommitPayload struct {
	// SessionID is the agent session the commit came from ("" for a
	// non-session caller such as the `cortex change commit` CLI).
	SessionID string `json:"session_id,omitempty"`
	// Turn is the 1-based turn ordinal within SessionID (0 when unknown or
	// when there is no session).
	Turn int `json:"turn,omitempty"`
	// Project is the workspace root the commit was made in.
	Project string `json:"project,omitempty"`
	// Outcome is one of the AttributionOutcome* constants.
	Outcome string `json:"outcome"`
	// Command is the observed command line (the rewritten one for
	// OutcomeAdded). Never the commit message body beyond what the command
	// itself carries.
	Command string `json:"command,omitempty"`
	// SHA is the resulting commit's hash, recorded only by the
	// post-commit verification write ("" on the intent write).
	SHA string `json:"sha,omitempty"`
	// TrailerPresent reports whether the commit message actually contains
	// the configured trailer — the compliance fact, read off
	// `git log -1 --format=%B`, never inferred from the intent. It is
	// meaningful only when Verified is true.
	TrailerPresent bool `json:"trailer_present,omitempty"`
	// Verified marks the post-commit write: SHA and TrailerPresent were
	// read back from the repository. False marks an intent-only write.
	Verified bool `json:"verified,omitempty"`
}

// NewAttributionCommitEntry builds a journal entry for one attribution event.
// Outcome is required — an event without one says nothing measurable.
func NewAttributionCommitEntry(p AttributionCommitPayload) (*Entry, error) {
	if p.Outcome == "" {
		return nil, fmt.Errorf("journal: attribution.commit requires Outcome")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal attribution.commit: %w", err)
	}
	return &Entry{Type: TypeAttributionCommit, V: 1, Payload: data}, nil
}

// ParseAttributionCommit decodes an attribution.commit entry's payload.
func ParseAttributionCommit(e *Entry) (*AttributionCommitPayload, error) {
	if e.Type != TypeAttributionCommit {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeAttributionCommit)
	}
	var p AttributionCommitPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse attribution.commit: %w", err)
	}
	return &p, nil
}

// AppendAttributionCommit opens the user-level attribution journal and appends
// one attribution.commit event. Like AppendLandscapeScan, this is the single
// write path shared by every observer (the bash tool in internal/tools and
// `cortex change commit` in cmd/cortex, which cannot import each other) so
// there is one class-dir convention rather than two. FsyncPerBatch matches
// the convention for regeneratable/telemetry events (docs/journal.md
// principle 4): a missing receipt costs a data point, not a commit.
func AppendAttributionCommit(p AttributionCommitPayload) error {
	dir, err := userhome.Path("journal", "attribution")
	if err != nil {
		return fmt.Errorf("journal: resolve user-level attribution journal dir: %w", err)
	}
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		return fmt.Errorf("journal: open user-level attribution journal: %w", err)
	}
	defer w.Close()

	entry, err := NewAttributionCommitEntry(p)
	if err != nil {
		return err
	}
	if _, err := w.Append(entry); err != nil {
		return fmt.Errorf("journal: append attribution.commit: %w", err)
	}
	return nil
}

// LatestAttributionCommits reads the user-level attribution journal (the same
// class dir AppendAttributionCommit writes) and returns every attribution.commit
// payload with its entry timestamp, oldest first, degrading quietly past a
// torn or malformed entry mid-stream — a best-effort audit read, mirroring
// LatestLandscapeScan's posture. It exists so the compliance question ("which
// of the agent's commits carried the trailer?") is answerable from one place.
func LatestAttributionCommits() ([]AttributionCommitPayload, []time.Time, error) {
	dir, err := userhome.Path("journal", "attribution")
	if err != nil {
		return nil, nil, fmt.Errorf("journal: resolve user-level attribution journal dir: %w", err)
	}
	r, err := NewReader(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("journal: open user-level attribution journal: %w", err)
	}
	defer r.Close()

	var payloads []AttributionCommitPayload
	var stamps []time.Time
	for {
		e, err := r.Next()
		if err != nil {
			break // EOF, or a torn tail — either way, stop with what we have.
		}
		if e.Type != TypeAttributionCommit {
			continue
		}
		p, err := ParseAttributionCommit(e)
		if err != nil {
			continue
		}
		payloads = append(payloads, *p)
		stamps = append(stamps, e.TS)
	}
	return payloads, stamps, nil
}
