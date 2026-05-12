package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/events"
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
		return c.runMigrate(ctx)
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

// runMigrate packs the project's .cortex/queue/processed/*.json files into
// the journal/capture/ writer-class. Files are appended in lexicographic
// order, which corresponds to chronological order for the existing event
// ID format ("20060102-150405-xxxxxxxx"). Refuses to run if the target
// journal already has entries unless --force is passed; .cortex/queue/
// is not deleted here (slice C6 handles cleanup after verification).
func (c *JournalCommand) runMigrate(ctx *Context) error {
	force := false
	for _, a := range ctx.Args[1:] {
		if a == "--force" || a == "-f" {
			force = true
		}
	}

	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}
	queueDir := filepath.Join(contextDir, "queue", "processed")
	classDir := filepath.Join(contextDir, "journal", "capture")

	// Refuse to run on top of an existing populated journal unless --force.
	if !force {
		r, err := journal.NewReader(classDir)
		if err == nil {
			_, nextErr := r.Next()
			r.Close()
			if nextErr == nil {
				return fmt.Errorf("journal %s already has entries; pass --force to append", classDir)
			} else if nextErr != io.EOF {
				return fmt.Errorf("check existing journal: %w", nextErr)
			}
		}
	}

	if _, err := os.Stat(queueDir); os.IsNotExist(err) {
		fmt.Printf("No queue at %s — nothing to migrate.\n", queueDir)
		return nil
	}

	files, err := filepath.Glob(filepath.Join(queueDir, "*.json"))
	if err != nil {
		return fmt.Errorf("list %s: %w", queueDir, err)
	}
	if len(files) == 0 {
		fmt.Printf("No events in %s — nothing to migrate.\n", queueDir)
		return nil
	}
	sort.Strings(files)

	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return fmt.Errorf("open journal writer: %w", err)
	}
	defer w.Close()

	migrated, skipped := 0, 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", filepath.Base(path), err)
			skipped++
			continue
		}
		// Validate it parses as an Event before appending; malformed files
		// are skipped rather than wedging the journal.
		if _, err := events.FromJSON(data); err != nil {
			fmt.Fprintf(os.Stderr, "  skip %s: bad event JSON: %v\n",
				filepath.Base(path), err)
			skipped++
			continue
		}
		entry := &journal.Entry{
			Type:    "capture.event",
			V:       1,
			Payload: data,
		}
		if _, err := w.Append(entry); err != nil {
			return fmt.Errorf("append %s: %w", filepath.Base(path), err)
		}
		migrated++
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	fmt.Printf("Migrated %d events to %s", migrated, classDir)
	if skipped > 0 {
		fmt.Printf(" (%d skipped)", skipped)
	}
	fmt.Println()
	fmt.Println("Old queue files at " + queueDir + " left in place.")
	fmt.Println("Run `cortex journal rebuild` to verify, then slice C6 will remove them.")
	return nil
}

// journalContextDir returns the project-local .cortex/ path. Uses the
// Context's cfg if provided; otherwise walks up from cwd via the existing
// loadCaptureConfig helper.
func journalContextDir(ctx *Context) (string, error) {
	if ctx.Config != nil && ctx.Config.ContextDir != "" {
		return ctx.Config.ContextDir, nil
	}
	captureCfg, err := loadCaptureConfig()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	return captureCfg.ContextDir, nil
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
  migrate     Pack .cortex/queue/processed/*.json into segments.
              Pass --force to append when the journal already has entries.

See docs/journal.md for the architecture and docs/journal-implementation-plan.md
for the full slice plan.
`
}
