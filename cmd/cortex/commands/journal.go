package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
		return c.runRebuild(ctx)
	case "replay":
		return c.runReplay(ctx)
	case "verify":
		return c.runVerify(ctx)
	case "show":
		return c.runShow(ctx)
	case "tail":
		return c.runTail(ctx)
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

// runShow prints a single journal entry by offset. Defaults to the
// capture class; --class=NAME selects another writer-class.
//
//	cortex journal show 42
//	cortex journal show --class=dream 17
func (c *JournalCommand) runShow(ctx *Context) error {
	class := "capture"
	var targetOff journal.Offset
	for _, a := range ctx.Args[1:] {
		switch {
		case strings.HasPrefix(a, "--class="):
			class = strings.TrimPrefix(a, "--class=")
		default:
			var n int64
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				targetOff = journal.Offset(n)
			}
		}
	}
	if targetOff == 0 {
		return fmt.Errorf("usage: cortex journal show [--class=NAME] <offset>")
	}

	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}
	r, err := journal.NewReader(filepath.Join(contextDir, "journal", class))
	if err != nil {
		return fmt.Errorf("open reader for %s: %w", class, err)
	}
	defer r.Close()

	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if e.Offset == targetOff {
			data, err := json.MarshalIndent(e, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			fmt.Println(string(data))
			return nil
		}
	}
	return fmt.Errorf("offset %d not found in %s", targetOff, class)
}

// runTail streams new entries as they're appended to a writer-class.
// Polls every 500ms by default; Ctrl-C stops.
//
//	cortex journal tail                  # capture, follow forever
//	cortex journal tail --class=dream
//	cortex journal tail --from-offset=10 # start at offset 10
//	cortex journal tail --once           # print current tail and exit
func (c *JournalCommand) runTail(ctx *Context) error {
	class := "capture"
	var lastSeen journal.Offset
	once := false
	for _, a := range ctx.Args[1:] {
		switch {
		case strings.HasPrefix(a, "--class="):
			class = strings.TrimPrefix(a, "--class=")
		case strings.HasPrefix(a, "--from-offset="):
			lastSeen = parseOffsetFlag(a, "--from-offset=", 0) - 1
			if lastSeen < 0 {
				lastSeen = 0
			}
		case a == "--once":
			once = true
		}
	}

	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}
	classDir := filepath.Join(contextDir, "journal", class)

	pollOnce := func() error {
		r, err := journal.NewReader(classDir)
		if err != nil {
			return err
		}
		defer r.Close()
		for {
			e, err := r.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if e.Offset <= lastSeen {
				continue
			}
			line, _ := json.Marshal(e)
			fmt.Println(string(line))
			lastSeen = e.Offset
		}
	}

	if once {
		return pollOnce()
	}

	// Follow mode — poll every 500ms.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := pollOnce(); err != nil {
			return err
		}
		<-ticker.C
	}
}

// runVerify checks the journal's structural integrity:
//
//  1. Cursor consistency: each class's cursor offset is ≤ the highest
//     entry offset in that class. A cursor past the tail means the
//     index was advanced without a matching entry — corruption.
//  2. Source-offset references resolve: every entry with a non-empty
//     Sources list points at offsets that exist in their respective
//     writer-classes. Implemented as same-class lookups for now;
//     cross-class source references are recorded but not strictly
//     verified across class boundaries (entries within a class are
//     monotonic, so same-class is the common case).
//
// Returns nonzero exit if any check fails. Used post-migration and
// periodically as a health probe.
func (c *JournalCommand) runVerify(ctx *Context) error {
	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}

	classes := []string{"capture", "observation", "dream", "reflect", "resolve", "think", "feedback", "eval"}

	// Pass 1: collect highest offset per class.
	maxOffsets := make(map[string]journal.Offset)
	totals := make(map[string]int)
	for _, class := range classes {
		classDir := filepath.Join(contextDir, "journal", class)
		r, err := journal.NewReader(classDir)
		if err != nil {
			return fmt.Errorf("open reader for %s: %w", class, err)
		}
		for {
			e, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				r.Close()
				return fmt.Errorf("read %s: %w", class, err)
			}
			if e.Offset > maxOffsets[class] {
				maxOffsets[class] = e.Offset
			}
			totals[class]++
		}
		r.Close()
	}

	// Pass 2: cursor checks.
	issues := 0
	for _, class := range classes {
		classDir := filepath.Join(contextDir, "journal", class)
		cur, err := journal.OpenCursor(classDir).Get()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cursor read error: %v\n", class, err)
			issues++
			continue
		}
		if cur > maxOffsets[class] {
			fmt.Fprintf(os.Stderr, "%s: cursor=%d past max offset=%d\n",
				class, cur, maxOffsets[class])
			issues++
		}
	}

	// Pass 3: source-offset reference checks. Same-class lookup only.
	for _, class := range classes {
		classDir := filepath.Join(contextDir, "journal", class)
		r, err := journal.NewReader(classDir)
		if err != nil {
			continue
		}
		for {
			e, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			for _, src := range e.Sources {
				// Source offsets cite earlier entries. Within this class
				// the max-offset upper-bound is sufficient; cross-class
				// source references are recorded but not strictly
				// verified across boundaries here.
				if src <= 0 || src > maxOffsets[class] {
					// Cross-class source — accept if any class has it.
					found := false
					for _, c := range classes {
						if src <= maxOffsets[c] && src > 0 {
							found = true
							break
						}
					}
					if !found {
						fmt.Fprintf(os.Stderr,
							"%s offset=%d: source offset %d not resolvable in any class\n",
							class, e.Offset, src)
						issues++
					}
				}
			}
		}
		r.Close()
	}

	// Pretty-print summary.
	fmt.Println("Journal verify:")
	for _, class := range classes {
		fmt.Printf("  %s: %d entries (max offset %d)\n", class, totals[class], maxOffsets[class])
	}
	if issues > 0 {
		return fmt.Errorf("verify: %d issue(s) found", issues)
	}
	fmt.Println("OK")
	return nil
}

// runReplay re-walks a range of capture entries and reports what the
// cognition pipeline would project against the current configuration —
// the foundation of the counterfactual-eval primitive. Slice X2 lands
// the structural skeleton; future work threads --config-overrides
// (model, prompt-hash, budget) through cognition to compare derivations
// without overwriting the originals.
//
// Flags:
//
//	--class=capture           Writer-class to replay (default: capture).
//	--from-offset=N           First offset to replay (default: 1).
//	--to-offset=N             Last offset to replay (default: tail).
//	--config-overrides=...    Reserved for future use; parsed but not
//	                          yet threaded through cognition.
func (c *JournalCommand) runReplay(ctx *Context) error {
	class := "capture"
	var fromOff, toOff Offset = 1, 0
	configOverrides := ""

	for _, a := range ctx.Args[1:] {
		switch {
		case a == "--help" || a == "-h":
			fmt.Print(replayUsage())
			return nil
		case strings.HasPrefix(a, "--class="):
			class = strings.TrimPrefix(a, "--class=")
		case strings.HasPrefix(a, "--from-offset="):
			fromOff = parseOffsetFlag(a, "--from-offset=", fromOff)
		case strings.HasPrefix(a, "--to-offset="):
			toOff = parseOffsetFlag(a, "--to-offset=", toOff)
		case strings.HasPrefix(a, "--config-overrides="):
			configOverrides = strings.TrimPrefix(a, "--config-overrides=")
		}
	}

	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}
	classDir := filepath.Join(contextDir, "journal", class)

	r, err := journal.NewReader(classDir)
	if err != nil {
		return fmt.Errorf("open journal reader for %s: %w", class, err)
	}
	defer r.Close()

	scanned, replayed := 0, 0
	for {
		entry, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read entry: %w", err)
		}
		scanned++
		if entry.Offset < fromOff {
			continue
		}
		if toOff != 0 && entry.Offset > toOff {
			break
		}
		// Skeleton: print one summary line per replayed entry. Future
		// work feeds entry into cognition with config overrides and
		// emits comparison records to a side journal.
		fmt.Printf("offset=%d type=%s v=%d\n", entry.Offset, entry.Type, entry.V)
		replayed++
	}

	fmt.Printf("\nReplayed %d/%d entries from %s (range %d..%v)\n",
		replayed, scanned, class, fromOff, toOff)
	if configOverrides != "" {
		fmt.Printf("--config-overrides=%q parsed but not yet threaded through cognition.\n", configOverrides)
		fmt.Println("Counterfactual replay against overridden config lands in a follow-up slice.")
	}
	return nil
}

func parseOffsetFlag(arg, prefix string, fallback Offset) Offset {
	v := strings.TrimPrefix(arg, prefix)
	if v == "" {
		return fallback
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
		return Offset(n)
	}
	return fallback
}

// Offset is re-exported here so flag parsing stays in this file.
type Offset = journal.Offset

func replayUsage() string {
	return `Usage: cortex journal replay [flags]

Flags:
  --class=NAME             Writer-class to replay (default: capture)
  --from-offset=N          First offset (default: 1)
  --to-offset=N            Last offset (default: tail)
  --config-overrides=KV    Reserved for counterfactual eval; parsed but
                           not yet threaded through cognition.
`
}

// runRebuild walks the full writer-class DAG: truncates all derived state
// and replays every writer-class's journal from offset 0. Slice X1 — the
// full-DAG version of the capture-only rebuild from C5.
//
// Order is implicit from processor.New's indexer registration order:
// capture + observation are registered before derivation classes
// (dream, reflect, resolve, think) and feedback. Since RunBatch iterates
// indexers in registration order, derivations that reference earlier
// classes by source-offset see their dependencies materialized first
// within the same batch.
func (c *JournalCommand) runRebuild(ctx *Context) error {
	contextDir, err := journalContextDir(ctx)
	if err != nil {
		return err
	}

	cfg := ctx.Config
	store := ctx.Storage
	if cfg == nil || store == nil {
		storageCfg, sErr := loadStorageConfig()
		if sErr != nil {
			return fmt.Errorf("load storage config: %w", sErr)
		}
		storageCfg.ContextDir = contextDir
		s, oErr := storage.New(storageCfg)
		if oErr != nil {
			return fmt.Errorf("open storage: %w", oErr)
		}
		defer s.Close()
		cfg = storageCfg
		store = s
	}

	// 1. Truncate every derived JSONL + in-memory index reachable by
	//    a journal-side writer-class. Insights/entities/etc. (Dream's
	//    direct-storage products) are left intact for now.
	if err := store.TruncateAllDerivedState(); err != nil {
		return fmt.Errorf("truncate derived state: %w", err)
	}
	// 2. Reset cursors for every known writer-class so the indexer
	//    replays from offset 0.
	for _, class := range []string{"capture", "observation", "dream", "reflect", "resolve", "think", "feedback", "eval"} {
		classDir := filepath.Join(contextDir, "journal", class)
		if err := journal.OpenCursor(classDir).Set(0); err != nil {
			return fmt.Errorf("reset cursor for %s: %w", class, err)
		}
	}
	// 3. Run the indexer; processor.New registers projectors for every
	//    writer-class and adds each class dir to its indexer set.
	proc := processor.New(cfg, store)
	n, err := proc.RunBatch()
	if err != nil {
		return fmt.Errorf("replay journal: %w", err)
	}

	fmt.Printf("Rebuilt derived state from journal: replayed %d entries across writer-classes\n", n)
	return nil
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
  rebuild     Truncate the events log and replay the capture
              journal from offset 0. Use after corruption or to
              verify the journal is sufficient to regenerate
              derived state. Extended to walk derivations in X1.
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
