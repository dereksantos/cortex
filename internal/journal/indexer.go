package journal

import (
	"fmt"
	"io"
	"log"
)

// Indexer tails one writer-class, projecting each entry past the cursor and
// advancing the cursor after each successful projection.
//
// The indexer is the *only* component allowed to write to derived state
// (SQLite tables) — capture and cognition modes write entries to the
// journal; the indexer projects them. This preserves CQRS: journal is the
// write side, derived state is regeneratable.
type Indexer struct {
	classDir  string
	cursor    *Cursor
	registry  *Registry
	onUnknown UnknownPolicy
	logger    *log.Logger // nil → log.Default()
}

// IndexerOpts configures an Indexer.
type IndexerOpts struct {
	// ClassDir is the writer-class directory, e.g. ".cortex/journal/capture".
	ClassDir string
	// Registry holds projectors for {type, v} pairs the indexer will see.
	Registry *Registry
	// OnUnknown controls what happens when an entry's {type, v} has no
	// registered projector. Default is UnknownLogAndSkip.
	OnUnknown UnknownPolicy
	// Logger receives unknown-type log lines. nil → log.Default().
	Logger *log.Logger
}

// NewIndexer constructs an Indexer for a writer-class.
func NewIndexer(opts IndexerOpts) *Indexer {
	if opts.Registry == nil {
		opts.Registry = NewRegistry()
	}
	return &Indexer{
		classDir:  opts.ClassDir,
		cursor:    OpenCursor(opts.ClassDir),
		registry:  opts.Registry,
		onUnknown: opts.OnUnknown,
		logger:    opts.Logger,
	}
}

// RunOnce reads entries past the current cursor to the journal tail,
// dispatching each to its projector. Returns the number of entries
// projected (or acknowledged via UnknownLogAndSkip) and the first error
// encountered. The cursor advances past every successfully handled entry —
// an error stops the run *before* the failing entry's cursor advance, so
// re-running RunOnce will retry it.
func (ix *Indexer) RunOnce() (int, error) {
	last, err := ix.cursor.Get()
	if err != nil {
		return 0, fmt.Errorf("indexer: cursor get: %w", err)
	}
	r, err := NewReader(ix.classDir)
	if err != nil {
		return 0, fmt.Errorf("indexer: reader: %w", err)
	}
	defer r.Close()

	projected := 0
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return projected, fmt.Errorf("indexer: read: %w", err)
		}
		if e.Offset <= last {
			continue
		}
		proj, ok := ix.registry.Lookup(e.Type, e.V)
		if !ok {
			switch ix.onUnknown {
			case UnknownError:
				return projected, fmt.Errorf(
					"indexer: unknown entry type %s/v%d at offset %d",
					e.Type, e.V, e.Offset)
			case UnknownLogAndSkip:
				ix.log("journal: skipping unknown %s/v%d at offset %d",
					e.Type, e.V, e.Offset)
				// fall through to advance cursor
			}
		} else if err := proj(e); err != nil {
			return projected, fmt.Errorf(
				"indexer: project %s/v%d offset %d: %w",
				e.Type, e.V, e.Offset, err)
		}
		if err := ix.cursor.Set(e.Offset); err != nil {
			return projected, fmt.Errorf("indexer: cursor set: %w", err)
		}
		projected++
	}
	return projected, nil
}

// Cursor returns the indexer's cursor for inspection (e.g. in `cortex
// journal verify`).
func (ix *Indexer) Cursor() *Cursor { return ix.cursor }

func (ix *Indexer) log(format string, args ...any) {
	if ix.logger != nil {
		ix.logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}
