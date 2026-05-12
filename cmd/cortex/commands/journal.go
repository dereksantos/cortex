package commands

import (
	"fmt"
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
		return notImplemented(sub, "C3")
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

func journalUsage() string {
	return `Usage: cortex journal <subcommand>

Subcommands:
  ingest      Run indexer once; exits when caught up.            (slice C3)
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
