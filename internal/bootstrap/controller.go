package bootstrap

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/projectscan"
)

// ExtractedInsight is the controller-side view of one insight produced
// by either maintain.extract_insight or maintain.extract_overview.
// Callers (the cortex bootstrap subcommand) adapt their op handler
// outputs into this shape so the controller stays decoupled from the
// LLM/op package (and avoids the bootstrap ↔ ops import cycle).
type ExtractedInsight struct {
	Content    string
	Category   string
	Importance float64
	Tags       []string
	Reasoning  string
	// Provenance: which op produced this insight.
	OpName string // "maintain.extract_insight" | "maintain.extract_overview"
}

// ExtractFunc is the controller's interface to the LLM ops. content
// is the chunk body; source is provenance; langHint + fileRoleHint
// are optional metadata. Returns 0+ insights and a fallback flag (true
// when the mechanical fallback ran).
type ExtractFunc func(ctx context.Context, content, source, langHint, fileRoleHint string) (insights []ExtractedInsight, fallback bool, err error)

// BootstrapController owns the iterate-until-coverage loop. Construct
// via NewController; drive via Run.
type BootstrapController struct {
	cfg        Config
	state      *BootstrapState
	statePath  string
	ignore     *projectscan.IgnoreSet
	analyzer   BoundaryAnalyzer
	sampler    Sampler
	boundaries *BoundaryOutput

	extractInsight  ExtractFunc
	extractOverview ExtractFunc

	writer     *journal.Writer
	rng        *rand.Rand
	pid        *PIDLock
	mu         sync.Mutex
	lastBanner float64 // last coverage fraction at which a banner fired
}

// ControllerConfig is the construction payload — Config plus the
// extract functions (kept separate from Config so the controller can
// be unit-tested with mock extract functions without touching
// pkg/cognition/dag/ops).
type ControllerConfig struct {
	Config
	ExtractInsightFn  ExtractFunc
	ExtractOverviewFn ExtractFunc

	// Analyzer / Sampler may be overridden for tests. Nil means
	// "use UniversalAnalyzer / HierarchicalSampler with the knobs
	// from Config."
	Analyzer BoundaryAnalyzer
	Sampler  Sampler
}

// NewController validates the config, applies defaults, loads any
// prior state, and returns a controller ready for Run.
func NewController(cc ControllerConfig) (*BootstrapController, error) {
	if cc.ProjectRoot == "" {
		return nil, fmt.Errorf("controller: ProjectRoot is required")
	}
	if cc.ContextDir == "" {
		cc.ContextDir = filepath.Join(cc.ProjectRoot, ".cortex")
	}
	if cc.TargetCoverage <= 0 {
		cc.TargetCoverage = 0.80
	}
	if cc.BudgetMax <= 0 {
		cc.BudgetMax = 200
	}
	if cc.BatchSize <= 0 {
		cc.BatchSize = 4
	}
	if cc.WindowLines <= 0 {
		cc.WindowLines = DefaultWindowLines
	}
	if cc.WindowOverlap < 0 {
		cc.WindowOverlap = DefaultWindowOverlap
	}
	if !IsValidExtractOp(cc.ExtractOp) {
		return nil, fmt.Errorf("controller: invalid ExtractOp %q", cc.ExtractOp)
	}
	if cc.ExtractOp == "" {
		cc.ExtractOp = ExtractOpAuto
	}

	analyzer := cc.Analyzer
	if analyzer == nil {
		analyzer = UniversalAnalyzer{
			WindowLines:   cc.WindowLines,
			WindowOverlap: cc.WindowOverlap,
			Salt:          cc.Salt,
		}
	}
	sampler := cc.Sampler
	if sampler == nil {
		sampler = &HierarchicalSampler{}
	}

	statePath := StatePath(cc.ContextDir)
	state, err := LoadState(statePath)
	if err != nil {
		// Corrupt state: start fresh but log to banner.
		if cc.Banner != nil {
			cc.Banner(fmt.Sprintf("warning: existing state unreadable (%v); starting fresh", err))
		}
		state = nil
	}

	c := &BootstrapController{
		cfg:             cc.Config,
		state:           state,
		statePath:       statePath,
		ignore:          projectscan.LoadIgnoreSet(cc.ProjectRoot),
		analyzer:        analyzer,
		sampler:         sampler,
		extractInsight:  cc.ExtractInsightFn,
		extractOverview: cc.ExtractOverviewFn,
	}
	return c, nil
}

// Run executes the bootstrap loop. Returns nil when:
//   - the lock is held by another process (logs + skips);
//   - coverage hits target (state Halted="target");
//   - budget exhausts (state Halted="budget_loc" or "budget_files");
//   - ctx is canceled (state Halted="canceled").
//
// Returns a non-nil error only for setup-class failures (analyzer
// crash, journal writer init, etc.). Per-chunk LLM errors fall
// through to the mechanical fallback inside the ExtractFunc.
func (c *BootstrapController) Run(ctx context.Context) error {
	// Acquire the pid lock first. A second invocation sees the lock
	// and skips — not an error.
	pid, ok, err := AcquirePIDLock(c.cfg.ContextDir)
	if err != nil {
		return fmt.Errorf("pidlock: %w", err)
	}
	if !ok {
		holder := PIDLockHolderPID(c.cfg.ContextDir)
		c.banner(fmt.Sprintf("bootstrap already running (pid %d); skipping", holder))
		return nil
	}
	c.pid = pid
	defer c.finalize()

	// Boundary scan (cached internally to the analyzer's call). The
	// analyzer is fast: a Cortex-sized project takes well under a
	// second of walk + line indexing on an SSD.
	out, err := c.analyzer.Analyze(ctx, c.cfg.ProjectRoot, c.ignore)
	if err != nil {
		return fmt.Errorf("analyzer: %w", err)
	}
	c.boundaries = out

	// Open the dream journal writer. dream uses FsyncPerBatch — losses
	// are regeneratable from the journal replay.
	w, werr := journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(c.cfg.ContextDir, "journal", "dream"),
		Fsync:    journal.FsyncPerBatch,
	})
	if werr != nil {
		return fmt.Errorf("journal writer: %w", werr)
	}
	c.writer = w
	defer w.Close()

	// Initialize state, either from disk (resume) or fresh.
	if c.state == nil ||
		c.state.StateHash != out.StateHash ||
		c.state.ProjectRoot != c.cfg.ProjectRoot {
		// Fresh run: previous state belonged to a different snapshot
		// of the project (or there was no previous state).
		c.state = &BootstrapState{
			Version:        StateVersion,
			ProjectRoot:    c.cfg.ProjectRoot,
			StateHash:      out.StateHash,
			RNGSeed:        out.RNGSeed,
			TargetCoverage: c.cfg.TargetCoverage,
			BudgetMax:      c.cfg.BudgetMax,
			BatchSize:      c.cfg.BatchSize,
			StartedAt:      time.Now().UTC(),
			EffTotalLines:  out.EffTotalLines,
			TotalFiles:     out.TotalFiles,
			ExtractOpUsed:  map[string]int{},
		}
	} else {
		// Resume: keep covered set + counters. Refresh denominators
		// in case they drifted (shouldn't, since StateHash matches).
		c.state.EffTotalLines = out.EffTotalLines
		c.state.TotalFiles = out.TotalFiles
		if c.state.ExtractOpUsed == nil {
			c.state.ExtractOpUsed = map[string]int{}
		}
	}
	c.rng = rand.New(rand.NewSource(out.RNGSeed))

	// Pre-iteration meta insight: so the journal alone is replay-
	// sufficient (the resulting dream.insight describes the run).
	if c.state.Iteration == 0 {
		c.emitMetaInsight()
	}

	// Track which files have at least one emitted insight (secondary
	// coverage signal). Reconstruct from covered chunk IDs at start.
	coveredFiles := c.coveredFileSetFromState(out)

	// Main loop.
	for {
		if halt, reason := c.shouldHaltSecondary(coveredFiles); halt {
			c.state.Halted = reason
			break
		}
		if err := ctx.Err(); err != nil {
			c.state.Halted = "canceled"
			break
		}

		covered := c.coveredChunkSet()
		ids := c.sampler.Next(out, covered, c.cfg.BatchSize, c.rng)
		if len(ids) == 0 {
			// Exhausted available chunks before hitting target.
			c.state.Halted = "budget_files" // closest match: nothing left to draw
			if c.coveredFracEffLines() < c.cfg.TargetCoverage {
				c.state.Halted = "budget_loc"
			}
			break
		}

		c.processChunkBatch(ctx, out, ids, coveredFiles)
		c.state.Iteration++
		if err := SaveState(c.statePath, c.state); err != nil {
			// Persistence failure is non-fatal — journal is durable.
			c.banner(fmt.Sprintf("warning: state persist failed: %v", err))
		}
		c.maybeBanner()
	}

	now := time.Now().UTC()
	c.state.CompletedAt = &now
	c.state.CoveredFiles = len(coveredFiles)
	if err := SaveState(c.statePath, c.state); err != nil {
		c.banner(fmt.Sprintf("warning: final state persist failed: %v", err))
	}
	if err := c.writer.Flush(); err != nil {
		c.banner(fmt.Sprintf("warning: journal flush failed: %v", err))
	}
	c.banner(fmt.Sprintf("done: halted=%s eff_loc=%.0f%% files=%.0f%% insights=%d iters=%d",
		c.state.Halted,
		100*c.coveredFracEffLines(),
		100*c.coveredFracFiles(coveredFiles),
		c.state.InsightsEmitted,
		c.state.Iteration,
	))
	return nil
}

// processChunkBatch reads each chunk, calls the appropriate extract
// op, emits dream.insight entries, and updates coverage state.
func (c *BootstrapController) processChunkBatch(
	ctx context.Context,
	out *BoundaryOutput,
	ids []string,
	coveredFiles map[string]bool,
) {
	chunkByID := make(map[string]Chunk, len(out.Chunks))
	for _, ch := range out.Chunks {
		chunkByID[ch.ID] = ch
	}
	for _, id := range ids {
		ch, ok := chunkByID[id]
		if !ok {
			continue
		}
		body, err := fractal.ReadRegion(ch.Path, ch.ByteOffset, ch.ByteLength)
		if err != nil || strings.TrimSpace(body) == "" {
			c.markCovered(ch, coveredFiles, false)
			continue
		}

		insights := c.extractFor(ctx, ch, body)
		emitted := 0
		for _, ins := range insights {
			if c.cfg.DryRun {
				emitted++
				continue
			}
			if err := c.emitDreamInsight(ch, ins); err != nil {
				c.banner(fmt.Sprintf("warning: emit insight failed: %v", err))
				continue
			}
			emitted++
		}
		c.state.InsightsEmitted += emitted
		c.markCovered(ch, coveredFiles, emitted > 0)
	}
}

// extractFor dispatches a chunk to the configured extract op (auto-
// routed by lang/role when ExtractOp="auto"), records which op ran,
// and returns the produced insights. Returns an empty slice when both
// the LLM call and its mechanical fallback produce nothing.
func (c *BootstrapController) extractFor(ctx context.Context, ch Chunk, body string) []ExtractedInsight {
	opName := ChooseExtractOp(c.cfg.ExtractOp, ch.Lang)
	source := fmt.Sprintf("bootstrap:%s:%s", ch.RelPath, ch.ID)
	var (
		insights []ExtractedInsight
		err      error
	)
	switch opName {
	case "maintain.extract_overview":
		if c.extractOverview == nil {
			return nil
		}
		insights, _, err = c.extractOverview(ctx, body, source, ch.Lang, fileRoleFromLang(ch.Lang))
	case "maintain.extract_insight":
		if c.extractInsight == nil {
			return nil
		}
		insights, _, err = c.extractInsight(ctx, body, source, ch.Lang, fileRoleFromLang(ch.Lang))
	default:
		return nil
	}
	if err != nil {
		c.banner(fmt.Sprintf("warning: extract failed for %s: %v", ch.RelPath, err))
		return nil
	}
	for i := range insights {
		insights[i].OpName = opName
	}
	c.state.ExtractOpUsed[opName]++
	return insights
}

// fileRoleFromLang derives a coarse role hint for the extract_overview
// prompt. The mapping is intentionally simple — the prompt is
// permissive enough that wrong guesses degrade gracefully.
func fileRoleFromLang(lang string) string {
	switch lang {
	case "md", "txt", "rst":
		return "doc"
	case "toml", "yaml", "ini", "tf":
		return "config"
	}
	if strings.Contains(lang, "test") {
		return "test"
	}
	return "source"
}

// markCovered adds the chunk to the covered set and updates per-file
// coverage. emittedInsight=true means at least one insight was
// produced — this is what makes the file count toward the secondary
// coverage signal (so "skipped because empty body" doesn't inflate
// file coverage).
func (c *BootstrapController) markCovered(ch Chunk, coveredFiles map[string]bool, emittedInsight bool) {
	if !contains(c.state.CoveredChunkIDs, ch.ID) {
		c.state.CoveredChunkIDs = append(c.state.CoveredChunkIDs, ch.ID)
		c.state.CoveredEffLines += ch.EffLines
	}
	if emittedInsight {
		if _, ok := coveredFiles[ch.RelPath]; !ok {
			coveredFiles[ch.RelPath] = true
		}
	}
}

// emitDreamInsight builds a DreamInsightPayload from the extracted
// insight + chunk provenance and appends to the dream journal.
func (c *BootstrapController) emitDreamInsight(ch Chunk, ins ExtractedInsight) error {
	imp := int(ins.Importance * 10)
	if imp < 0 {
		imp = 0
	}
	if imp > 10 {
		imp = 10
	}
	cat := ins.Category
	if cat == "" {
		cat = "pattern"
	}
	tags := append([]string{"bootstrap"}, ins.Tags...)
	sort.Strings(tags)
	insightID := fmt.Sprintf("bootstrap:%s:%s", ch.RelPath, ch.ID)
	payload := journal.DreamInsightPayload{
		InsightID:    insightID,
		Category:     cat,
		Content:      ins.Content,
		Importance:   imp,
		Tags:         tags,
		Reasoning:    ins.Reasoning,
		SourceItemID: insightID,
		SourceName:   "bootstrap",
	}
	entry, err := journal.NewDreamInsightEntry(payload)
	if err != nil {
		return err
	}
	_, err = c.writer.Append(entry)
	return err
}

// emitMetaInsight writes a single dream.insight describing the
// bootstrap run as a whole, so the journal alone reconstructs the
// context (seed, sampler, window knobs, totals, op choice).
func (c *BootstrapController) emitMetaInsight() {
	if c.cfg.DryRun {
		return
	}
	content := fmt.Sprintf(
		"Bootstrap started: sampler=%s, window=%d/%d, chunks=%d, files=%d, eff_loc=%d, extract_op=%s, seed=%d",
		c.sampler.Name(),
		c.cfg.WindowLines, c.cfg.WindowOverlap,
		len(c.boundaries.Chunks),
		c.boundaries.TotalFiles,
		c.boundaries.EffTotalLines,
		c.cfg.ExtractOp,
		c.boundaries.RNGSeed,
	)
	payload := journal.DreamInsightPayload{
		InsightID:    fmt.Sprintf("bootstrap:meta:%s", c.boundaries.StateHash[:16]),
		Category:     "pattern",
		Content:      content,
		Importance:   3,
		Tags:         []string{"bootstrap", "meta"},
		SourceItemID: "bootstrap:meta",
		SourceName:   "bootstrap",
	}
	entry, err := journal.NewDreamInsightEntry(payload)
	if err != nil {
		return
	}
	_, _ = c.writer.Append(entry)
}

// shouldHaltSecondary returns (true, reason) when either coverage
// signal has met its target or budget has run out. Reason values:
//   - "target"        — both signals ≥ TargetCoverage
//   - "budget_loc"    — budget exhausted, eff-LOC further from target
//   - "budget_files"  — budget exhausted, file coverage further from target
func (c *BootstrapController) shouldHaltSecondary(coveredFiles map[string]bool) (bool, string) {
	if c.state.Iteration >= c.cfg.BudgetMax {
		locFrac := c.coveredFracEffLines()
		fileFrac := c.coveredFracFiles(coveredFiles)
		if locFrac < fileFrac {
			return true, "budget_loc"
		}
		return true, "budget_files"
	}
	locFrac := c.coveredFracEffLines()
	fileFrac := c.coveredFracFiles(coveredFiles)
	if locFrac >= c.cfg.TargetCoverage && fileFrac >= c.cfg.TargetCoverage {
		return true, "target"
	}
	return false, ""
}

// coveredChunkSet is a constant-time-lookup view over state.CoveredChunkIDs.
func (c *BootstrapController) coveredChunkSet() map[string]bool {
	m := make(map[string]bool, len(c.state.CoveredChunkIDs))
	for _, id := range c.state.CoveredChunkIDs {
		m[id] = true
	}
	return m
}

// coveredFileSetFromState reconstructs the per-file covered set from
// the persisted chunk IDs (file is "covered" if any of its chunks
// are covered). Used on resume to seed coveredFiles.
func (c *BootstrapController) coveredFileSetFromState(out *BoundaryOutput) map[string]bool {
	chunkByID := make(map[string]Chunk, len(out.Chunks))
	for _, ch := range out.Chunks {
		chunkByID[ch.ID] = ch
	}
	files := make(map[string]bool)
	for _, id := range c.state.CoveredChunkIDs {
		if ch, ok := chunkByID[id]; ok {
			files[ch.RelPath] = true
		}
	}
	return files
}

// coveredFracEffLines returns covered_eff_lines / eff_total_lines.
func (c *BootstrapController) coveredFracEffLines() float64 {
	if c.state.EffTotalLines <= 0 {
		return 1.0 // vacuous: empty project halts immediately
	}
	return float64(c.state.CoveredEffLines) / float64(c.state.EffTotalLines)
}

// coveredFracFiles returns covered_files / total_files.
func (c *BootstrapController) coveredFracFiles(coveredFiles map[string]bool) float64 {
	if c.state.TotalFiles <= 0 {
		return 1.0
	}
	return float64(len(coveredFiles)) / float64(c.state.TotalFiles)
}

// banner forwards a one-line status update to the configured banner
// callback. No-op when callback is nil.
func (c *BootstrapController) banner(line string) {
	if c.cfg.Banner == nil {
		return
	}
	c.cfg.Banner(line)
}

// maybeBanner emits a banner when coverage has crossed a 10% threshold
// since the last banner. Prevents flooding the REPL with per-chunk
// updates.
func (c *BootstrapController) maybeBanner() {
	frac := c.coveredFracEffLines()
	floor := math.Floor(frac*10) / 10
	if floor > c.lastBanner {
		c.lastBanner = floor
		c.banner(fmt.Sprintf("%d%% effective LOC covered (insights: %d, iter: %d)",
			int(floor*100), c.state.InsightsEmitted, c.state.Iteration))
	}
}

// finalize releases the pidlock and (best effort) removes the pid
// file. Called from defer in Run so the lock is released even on
// panic.
func (c *BootstrapController) finalize() {
	if c.pid != nil {
		c.pid.Release()
		c.pid = nil
	}
}

// RunInBackground is a convenience wrapper for callers (REPL, daemon)
// that want to spawn a controller in a goroutine without managing the
// NewController + Run dance themselves. Returns immediately with any
// construction error; logging the run's progress is the caller's
// responsibility via cc.Banner.
//
// Errors during Run are routed to cc.Banner as a warning line. The
// function does not return them — the bootstrap is best-effort
// background work and should not crash the caller.
func RunInBackground(ctx context.Context, cc ControllerConfig) {
	c, err := NewController(cc)
	if err != nil {
		if cc.Banner != nil {
			cc.Banner("bootstrap setup failed: " + err.Error())
		}
		return
	}
	if err := c.Run(ctx); err != nil {
		if cc.Banner != nil {
			cc.Banner("bootstrap run failed: " + err.Error())
		}
	}
}

// State returns a read-only view of the controller's current state.
// Tests + callers use this to inspect run results.
func (c *BootstrapController) State() *BootstrapState { return c.state }

// Boundaries returns the BoundaryOutput from the most-recent Analyze
// call. May be nil if Run has not been invoked or the analyzer
// errored.
func (c *BootstrapController) Boundaries() *BoundaryOutput { return c.boundaries }

// contains is a linear search over a small sorted slice — covered
// chunk IDs grow over a single run, so the slice stays bounded by
// the project's chunk count.
func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// fileExistsHelper is a thin os.Stat wrapper used in tests. Not
// exported — `os.Stat` from the standard library is the production
// entry.
func fileExistsHelper(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
