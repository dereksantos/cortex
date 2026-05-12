package commands

import (
	"fmt"

	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/storage"
)

// JournalCommand dispatches journal subcommands. Subcommand bodies land in
// later slices per docs/journal-implementation-plan.md; F2 only provides
// the dispatch and stubs.
type JournalCommand struct{}

func init() {
	Register(&JournalCommand{})
}

// Name returns the command name used to invoke it.
func (c *JournalCommand) Name() string { return "journal" }

// Description returns a brief description of what the command does.
func (c *JournalCommand) Description() string {
	return "Journal operations (rebuild, replay, verify, show, tail, migrate, ingest)"
}

// Execute dispatches to a subcommand based on the first argument.
func (c *JournalCommand) Execute(ctx *Context) error {
	if len(ctx.Args) == 0 {
		fmt.Print(journalUsage())
		return nil
	}
	sub := ctx.Args[0]
	switch sub {
	case "ingest":
		return c.runIngest(ctx)
	case "rebuild":
		return notImplemented(sub, "C5 (capture) / X1 (full DAG)")
	case "replay":
		return notImplemented(sub, "X2")
	case "verify":
		return notImplemented(sub, "X3")
	case "show":
		return notImplemented(sub, "I1")
	case "tail":
		return notImplemented(sub, "I1")
	case "migrate":
		return notImplemented(sub, "C4")
	case "help", "-h", "--help":
		fmt.Print(journalUsage())
		return nil
	default:
		return fmt.Errorf("unknown journal subcommand: %s\n\n%s", sub, journalUsage())
	}
}

func notImplemented(sub, slice string) error {
	return fmt.Errorf("cortex journal %s: not yet implemented (lands in slice %s — see docs/journal-implementation-plan.md)", sub, slice)
}

// runIngest drains the project's capture journal once and exits. Lower
// level than `cortex ingest` — does not embed or analyze, just projects
// journal entries to SQLite.
func (c *JournalCommand) runIngest(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	if cfg == nil || store == nil {
		captureCfg, captureErr := loadCaptureConfig()
		if captureErr != nil {
			return fmt.Errorf("load config: %w", captureErr)
		}
		storageCfg, err := loadStorageConfig()
		if err != nil {
			return fmt.Errorf("load storage config: %w", err)
		}
		s, err := storage.New(storageCfg)
		if err != nil {
			return fmt.Errorf("open storage: %w", err)
		}
		defer s.Close()
		// ContextDir must be project-local so the journal/capture/ lookup
		// hits the project's journal, not the global ~/.cortex/.
		storageCfg.ContextDir = captureCfg.ContextDir
		cfg = storageCfg
		store = s
	}

	proc := processor.New(cfg, store)
	n, err := proc.RunBatch()
	if err != nil {
		return fmt.Errorf("drain journal: %w", err)
	}
	fmt.Printf("Projected %d journal entries\n", n)
	return nil
}

func journalUsage() string {
	return `Usage: cortex journal <subcommand>

Subcommands:
  ingest      Run indexer once; exits when caught up.
  rebuild     Truncate derived state, replay journal from 0.     (slice C5 / X1)
  replay      Re-run cognition with config overrides.            (slice X2)
  verify      Source-offset integrity, projection row counts.    (slice X3)
  show        Print a single entry by offset.                    (slice I1)
  tail        Stream entries as they're appended.                (slice I1)
  migrate     Pack .cortex/queue/processed/*.json into segments. (slice C4)

See docs/journal.md for the architecture and docs/journal-implementation-plan.md
for the full slice plan.
`
}
