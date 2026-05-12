package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

// DreamAnalysisPrompt guides the LLM to extract insights from sampled content.
const DreamAnalysisPrompt = `Analyze this content for durable insights that would help future coding sessions.

Content:
%s

Source: %s
Path: %s
%s
Extract any:
- Decisions (choices made and why)
- Patterns (reusable approaches)
- Constraints (things to avoid)
- Corrections (mistakes to not repeat)

If nothing significant, respond with just: NO_INSIGHT

Otherwise respond in JSON:
{
  "content": "the insight (1-2 sentences)",
  "category": "decision|pattern|constraint|correction",
  "importance": 0.0-1.0,
  "tags": ["tag1", "tag2"]
}`

// QueuedTranscript represents a transcript queued for Dream analysis.
type QueuedTranscript struct {
	Path      string
	SessionID string
	QueuedAt  time.Time
}

// Dream implements cognition.Dreamer for idle-time exploration.
//
// Dream is fractal: it samples regions of files (not whole files), tracks
// which item-IDs and content hashes it has analyzed recently to skip
// duplicates, and queues neighbor regions of any region that yielded a
// high-signal insight so the next cycle zooms in. Sources still produce
// DreamItems, but DreamItems with region_offset/region_len metadata are
// treated as windowed reads.
type Dream struct {
	mu sync.Mutex

	// Components
	sources  []cognition.DreamSource
	storage  *storage.Storage
	llm      llm.Provider
	activity *ActivityTracker

	// Journal output (slice D1). When set, Dream emits dream.insight
	// entries to <journalDir>/dream/ on each insight discovery. Empty
	// disables journal emission (storage write-through is used instead;
	// the dual path goes away after slice D2 lands the projector).
	journalDir string

	// Fractal primitives
	novelty   *fractal.Novelty
	followUps *fractal.FollowUpQueue
	weights   *fractal.SourceWeights
	rng       *rand.Rand
	statePath string

	// Config
	config cognition.DreamConfig

	// State
	running         bool
	lastDream       time.Time
	insightsChan    chan cognition.Result
	proactiveQueue  []cognition.Result
	sessionInsights int

	queuedTranscripts []QueuedTranscript

	stateWriter *StateWriter
}

// SetJournalDir wires the project's <ContextDir>/journal/ root. When set,
// Dream emits dream.insight entries to <journalDir>/dream/ on each
// extracted insight. Pass empty to disable journal emission.
func (d *Dream) SetJournalDir(dir string) {
	d.journalDir = dir
}

// emitInsightToJournal best-effort writes a dream.insight entry. Errors
// are logged via log.Printf but never returned — journal emission must
// not block Dream's main loop.
func (d *Dream) emitInsightToJournal(insight cognition.Result, item cognition.DreamItem, sessionID string) {
	if d.journalDir == "" {
		return
	}
	payload := journal.DreamInsightPayload{
		InsightID:    insight.ID,
		Category:     insight.Category,
		Content:      insight.Content,
		Importance:   int(insight.Score * 10),
		Tags:         insight.Tags,
		SessionID:    sessionID,
		SourceItemID: item.ID,
		SourceName:   item.Source,
	}
	entry, err := journal.NewDreamInsightEntry(payload)
	if err != nil {
		log.Printf("dream: build journal entry: %v", err)
		return
	}
	classDir := filepath.Join(d.journalDir, "dream")
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		log.Printf("dream: open journal writer: %v", err)
		return
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		log.Printf("dream: append journal entry: %v", err)
	}
}

// NewDream creates a new Dream instance.
//
// statePath is where the novelty cache snapshots are persisted. An empty
// string disables persistence (cache lives only for daemon lifetime).
func NewDream(store *storage.Storage, provider llm.Provider, activity *ActivityTracker, statePath string) *Dream {
	d := &Dream{
		storage:      store,
		llm:          provider,
		activity:     activity,
		config:       cognition.DefaultDreamConfig(),
		insightsChan: make(chan cognition.Result, 100),
		novelty:      fractal.NewNovelty(0, 0),
		followUps:    fractal.NewFollowUpQueue(0, 0),
		weights:      fractal.NewSourceWeights(),
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
		statePath:    statePath,
	}
	if statePath != "" {
		_ = d.novelty.Load(statePath)
	}
	return d
}

// SetConfig updates the Dream configuration.
func (d *Dream) SetConfig(cfg cognition.DreamConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.config = cfg
}

// RegisterSource adds a source for Dream to explore.
func (d *Dream) RegisterSource(source cognition.DreamSource) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sources = append(d.sources, source)
}

// SetStateWriter sets the state writer for daemon status updates.
func (d *Dream) SetStateWriter(sw *StateWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stateWriter = sw
}

// MaybeDream attempts exploration if the system is idle.
func (d *Dream) MaybeDream(ctx context.Context) (*cognition.DreamResult, error) {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return &cognition.DreamResult{Status: cognition.DreamSkippedRunning}, nil
	}
	if !d.activity.IsIdle() {
		d.mu.Unlock()
		return &cognition.DreamResult{Status: cognition.DreamSkippedActive}, nil
	}
	if time.Since(d.lastDream) < d.config.MinInterval {
		d.mu.Unlock()
		return &cognition.DreamResult{Status: cognition.DreamSkippedRecent}, nil
	}
	if len(d.sources) == 0 {
		d.mu.Unlock()
		return &cognition.DreamResult{Status: cognition.DreamSkippedActive}, nil
	}

	d.running = true
	stateWriter := d.stateWriter
	sources := append([]cognition.DreamSource(nil), d.sources...)
	d.mu.Unlock()

	if stateWriter != nil {
		stateWriter.WriteMode("dream", "Dreaming about the codebase...")
	}

	start := time.Now()
	minDisplay := d.config.MinDisplayDuration

	defer func() {
		elapsed := time.Since(start)
		if elapsed < minDisplay {
			time.Sleep(minDisplay - elapsed)
		}
		d.mu.Lock()
		d.running = false
		d.lastDream = time.Now()
		d.mu.Unlock()
		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
	}()

	budget := d.activity.DreamBudget(d.config.MinBudget, d.config.MaxBudget, d.config.GrowthDuration)
	log.Printf("Dream: starting (budget: %d, sources: %d, novelty=%d, queue=%d)",
		budget, len(sources), d.novelty.Len(), d.followUps.Len())

	ops := 0
	insights := 0
	noveltySkips := 0
	sourcesCovered := make([]string, 0)
	seenSources := make(map[string]bool)
	perSource := make(map[string]fractal.CycleStats, len(sources))

	// 1) Reserve 25% of budget for follow-up drain.
	followBudget := budget / 4
	if followBudget < 1 && budget >= 1 {
		followBudget = 1
	}
	drained := d.followUps.Drain(followBudget)
	for _, fu := range drained {
		if ops >= budget {
			break
		}
		item, ok := buildFollowUpItem(fu)
		if !ok {
			continue
		}
		gotInsight, skipped := d.processItem(ctx, item, stateWriter, fu.Depth)
		if skipped {
			noveltySkips++
			continue
		}
		ops++
		stats := perSource[item.Source]
		stats.Items++
		if gotInsight {
			stats.Insights++
			insights++
		}
		perSource[item.Source] = stats
		if !seenSources[item.Source] {
			sourcesCovered = append(sourcesCovered, item.Source)
			seenSources[item.Source] = true
		}
	}

	// 2) Allocate the remaining budget across sources.
	remaining := budget - ops
	if remaining < 0 {
		remaining = 0
	}
	names := make([]string, 0, len(sources))
	byName := make(map[string]cognition.DreamSource, len(sources))
	for _, s := range sources {
		names = append(names, s.Name())
		byName[s.Name()] = s
	}
	alloc := d.weights.Allocate(remaining, names, d.rng)

	// Iterate in a shuffled order so no single source consistently
	// runs first.
	order := append([]string(nil), names...)
	d.rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	for _, name := range order {
		if ops >= budget {
			break
		}
		take := alloc[name]
		if take <= 0 {
			continue
		}
		source := byName[name]
		items, err := source.Sample(ctx, take)
		if err != nil {
			continue
		}
		if len(items) > 0 && !seenSources[name] {
			sourcesCovered = append(sourcesCovered, name)
			seenSources[name] = true
		}
		for _, item := range items {
			if ops >= budget {
				break
			}
			gotInsight, skipped := d.processItem(ctx, item, stateWriter, 0)
			if skipped {
				noveltySkips++
				continue
			}
			ops++
			stats := perSource[name]
			stats.Items++
			if gotInsight {
				stats.Insights++
				insights++
			}
			perSource[name] = stats
		}
	}

	d.weights.Update(perSource)
	if d.statePath != "" {
		_ = d.novelty.Snapshot(d.statePath)
	}

	log.Printf("Dream: completed (ops=%d, insights=%d, follow_ups=%d, novelty_skips=%d, %v)",
		ops, insights, len(drained), noveltySkips, time.Since(start))

	return &cognition.DreamResult{
		Status:         cognition.DreamRan,
		Operations:     ops,
		Duration:       time.Since(start),
		Insights:       insights,
		SourcesCovered: sourcesCovered,
	}, nil
}

// processItem runs one item through novelty → analyze → record → enqueue.
// Returns (gotInsight, skipped). skipped=true means the item was deduped.
func (d *Dream) processItem(ctx context.Context, item cognition.DreamItem, sw *StateWriter, parentDepth int) (bool, bool) {
	contentHash := d.novelty.HashContent(item.Content)
	if d.novelty.Seen(item.ID, contentHash) {
		return false, true
	}

	if sw != nil {
		if truncPath := TruncatePath(item.Path, 30); truncPath != "" {
			sw.WriteMode("dream", fmt.Sprintf("Exploring %s...", truncPath))
		}
	}

	insight, err := d.analyzeItem(ctx, item, sw)
	if err != nil || insight == nil {
		d.novelty.RecordSeen(item.ID, contentHash, false)
		return false, false
	}
	d.novelty.RecordSeen(item.ID, contentHash, true)

	var sessionID string
	if sid, ok := item.Metadata["session_id"].(string); ok {
		sessionID = sid
	}
	d.emitInsightToJournal(*insight, item, sessionID)
	// Storage write is the projector's job (slice D2). If no journalDir is
	// wired (some tests construct Dream without one), fall back to direct
	// storage write so the in-process tests still observe the insight.
	if d.journalDir == "" && d.storage != nil {
		d.storage.StoreInsightWithSession(
			item.ID,
			insight.Category,
			insight.Content,
			int(insight.Score*10),
			insight.Tags,
			"",
			sessionID,
			item.Source,
		)
	}

	select {
	case d.insightsChan <- *insight:
	default:
	}

	if sw != nil {
		sw.WriteMode("insight", fmt.Sprintf("Discovered %s", TruncateInsight(insight.Content, 35)))
		time.Sleep(2 * time.Second)
	}

	d.enqueueNeighbors(item, parentDepth)

	if insight.Score >= 0.8 {
		d.mu.Lock()
		d.proactiveQueue = append(d.proactiveQueue, *insight)
		d.mu.Unlock()
		d.extractAndStoreNuances(ctx, *insight, item, sw)
	}

	d.mu.Lock()
	d.sessionInsights++
	d.mu.Unlock()
	return true, false
}

// enqueueNeighbors looks at the item's region metadata; if present, it
// enqueues the four neighbor regions for analysis next cycle.
func (d *Dream) enqueueNeighbors(item cognition.DreamItem, parentDepth int) {
	off, hasOff := metaInt64(item.Metadata, "region_offset")
	length, hasLen := metaInt(item.Metadata, "region_len")
	if !hasOff || !hasLen {
		return
	}
	fullPath, _ := item.Metadata["full_path"].(string)
	if fullPath == "" {
		return
	}
	fileSize, _ := metaInt64(item.Metadata, "file_size")
	if fileSize <= 0 {
		fi, err := os.Stat(fullPath)
		if err != nil {
			return
		}
		fileSize = fi.Size()
	}
	parent := fractal.Region{Path: fullPath, Offset: off, Length: length}
	for _, n := range fractal.NeighborRegions(parent, fileSize) {
		d.followUps.Enqueue(fractal.FollowUp{
			Region:       n,
			ParentItemID: item.ID,
			Depth:        parentDepth + 1,
			Source:       item.Source,
			Meta: map[string]any{
				"rel_path":  item.Metadata["rel_path"],
				"full_path": fullPath,
				"file_size": fileSize,
				"ext":       item.Metadata["ext"],
			},
		})
	}
}

// buildFollowUpItem reads a region from disk and turns it into a
// DreamItem ready for analysis. Returns ok=false if the file is gone.
func buildFollowUpItem(fu fractal.FollowUp) (cognition.DreamItem, bool) {
	content, err := fractal.ReadRegion(fu.Region.Path, fu.Region.Offset, fu.Region.Length)
	if err != nil || content == "" {
		return cognition.DreamItem{}, false
	}
	relPath, _ := fu.Meta["rel_path"].(string)
	if relPath == "" {
		relPath = fu.Region.Path
	}
	source := fu.Source
	if source == "" {
		source = "fractal"
	}
	id := fmt.Sprintf("%s:%s#offset=%d", source, relPath, fu.Region.Offset)
	meta := map[string]any{
		"region_offset":  fu.Region.Offset,
		"region_len":     fu.Region.Length,
		"parent_item_id": fu.ParentItemID,
		"fractal_depth":  fu.Depth,
		"full_path":      fu.Region.Path,
		"rel_path":       relPath,
	}
	for k, v := range fu.Meta {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	return cognition.DreamItem{
		ID:       id,
		Source:   source,
		Content:  content,
		Path:     relPath,
		Metadata: meta,
	}, true
}

// extractAndStoreNuances handles the nuance-extraction side-effect that
// previously lived inline in MaybeDream. Behavior preserved.
func (d *Dream) extractAndStoreNuances(ctx context.Context, insight cognition.Result, item cognition.DreamItem, sw *StateWriter) {
	if sw != nil {
		sw.WriteMode("dream", "Extracting implementation nuances...")
	}
	nuances, err := ExtractNuances(ctx, d.llm, insight.Content)
	if err != nil || len(nuances) == 0 {
		return
	}
	if len(nuances) > 3 {
		nuances = nuances[:3]
	}
	var sessionID string
	if sid, ok := item.Metadata["session_id"].(string); ok {
		sessionID = sid
	}
	for _, nuance := range nuances {
		supplementalID := insight.ID + ":nuance"
		nuanceContent := fmt.Sprintf("NUANCE for '%s': %s (Why: %s)",
			TruncateInsight(insight.Content, 50), nuance.Detail, nuance.Why)
		nuanceTags := append(insight.Tags, "nuance", "implementation-detail")
		nuanceResultForJournal := cognition.Result{
			ID:       supplementalID,
			Category: "nuance",
			Content:  nuanceContent,
			Score:    insight.Score,
			Tags:     nuanceTags,
		}
		d.emitInsightToJournal(nuanceResultForJournal, item, sessionID)
		// Fallback to direct storage write when no journal is wired —
		// see equivalent guard in tryAnalyzeItem above.
		if d.journalDir == "" && d.storage != nil {
			d.storage.StoreInsightWithSession(
				supplementalID,
				"nuance",
				nuanceContent,
				int(insight.Score*10),
				nuanceTags,
				"",
				sessionID,
				item.Source,
			)
		}
		nuanceResult := cognition.Result{
			ID:        supplementalID,
			Content:   nuanceContent,
			Category:  "nuance",
			Score:     insight.Score,
			Timestamp: time.Now(),
			Tags:      append(insight.Tags, "nuance", "implementation-detail"),
			Metadata: map[string]any{
				"parent_insight": insight.ID,
				"nuance_detail":  nuance.Detail,
				"nuance_why":     nuance.Why,
			},
		}
		select {
		case d.insightsChan <- nuanceResult:
		default:
		}
	}
	log.Printf("Dream: extracted %d nuances for insight %s", len(nuances), insight.ID)
}

// ResetForTesting resets Dream's internal state for testing.
func (d *Dream) ResetForTesting() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastDream = time.Time{}
}

// analyzeItem uses LLM to extract insights from a sampled item.
func (d *Dream) analyzeItem(ctx context.Context, item cognition.DreamItem, stateWriter *StateWriter) (*cognition.Result, error) {
	if d.llm == nil || !d.llm.IsAvailable() {
		return nil, nil
	}

	if stateWriter != nil {
		if truncPath := TruncatePath(item.Path, 25); truncPath != "" {
			stateWriter.WriteMode("dream", fmt.Sprintf("Analyzing %s for patterns...", truncPath))
		}
	}

	content := item.Content
	if len(content) > 2000 {
		content = content[:2000] + "..."
	}

	regionNote := buildRegionNote(item)
	prompt := fmt.Sprintf(DreamAnalysisPrompt, content, item.Source, item.Path, regionNote)

	response, err := d.llm.GenerateWithSystem(ctx, prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return nil, err
	}
	if strings.Contains(strings.ToUpper(response), "NO_INSIGHT") {
		return nil, nil
	}
	return d.parseInsightResponse(response, item)
}

// buildRegionNote returns a hint line for the prompt when the item
// represents a windowed read; otherwise an empty string.
func buildRegionNote(item cognition.DreamItem) string {
	off, hasOff := metaInt64(item.Metadata, "region_offset")
	length, hasLen := metaInt(item.Metadata, "region_len")
	if !hasOff || !hasLen {
		return ""
	}
	size, _ := metaInt64(item.Metadata, "file_size")
	if size > 0 {
		return fmt.Sprintf("Region: window at offset %d, length %d of file size %d. Analyze only what is visible.\n", off, length, size)
	}
	return fmt.Sprintf("Region: window at offset %d, length %d. Analyze only what is visible.\n", off, length)
}

// insightResponse represents the LLM's insight output.
type insightResponse struct {
	Content    string   `json:"content"`
	Category   string   `json:"category"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags"`
}

// parseInsightResponse parses the LLM response into a Result.
func (d *Dream) parseInsightResponse(response string, item cognition.DreamItem) (*cognition.Result, error) {
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON found in response")
	}
	jsonStr := response[start : end+1]

	var ir insightResponse
	if err := json.Unmarshal([]byte(jsonStr), &ir); err != nil {
		return nil, err
	}
	if ir.Importance <= 0 || ir.Importance > 1.0 {
		ir.Importance = 0.5
	}

	meta := map[string]any{
		"source":      item.Source,
		"source_path": item.Path,
		"source_id":   item.ID,
	}
	// Preserve fractal metadata so downstream consumers can render it.
	for _, k := range []string{"region_offset", "region_len", "parent_item_id", "fractal_depth", "full_path", "rel_path"} {
		if v, ok := item.Metadata[k]; ok {
			meta[k] = v
		}
	}

	return &cognition.Result{
		ID:        "dream:" + item.ID,
		Content:   ir.Content,
		Category:  ir.Category,
		Score:     ir.Importance,
		Timestamp: time.Now(),
		Tags:      ir.Tags,
		Metadata:  meta,
	}, nil
}

// Insights returns a channel of discoveries from dreaming.
func (d *Dream) Insights() <-chan cognition.Result {
	return d.insightsChan
}

// ProactiveQueue returns items queued for proactive injection.
func (d *Dream) ProactiveQueue() []cognition.Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	queue := make([]cognition.Result, len(d.proactiveQueue))
	copy(queue, d.proactiveQueue)
	return queue
}

// ClearProactiveQueue clears the proactive queue after injection.
func (d *Dream) ClearProactiveQueue() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.proactiveQueue = nil
}

// QueueTranscript queues a transcript for Dream analysis during idle time.
func (d *Dream) QueueTranscript(path string, sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, qt := range d.queuedTranscripts {
		if qt.Path == path {
			return
		}
	}
	d.queuedTranscripts = append(d.queuedTranscripts, QueuedTranscript{
		Path:      path,
		SessionID: sessionID,
		QueuedAt:  time.Now(),
	})
	log.Printf("Dream: queued transcript %s for analysis", path)
}

// GetQueuedTranscripts returns a copy of queued transcripts.
func (d *Dream) GetQueuedTranscripts() []QueuedTranscript {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]QueuedTranscript, len(d.queuedTranscripts))
	copy(result, d.queuedTranscripts)
	return result
}

// ClearQueuedTranscripts clears transcripts after processing.
func (d *Dream) ClearQueuedTranscripts() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queuedTranscripts = nil
}

// SessionInsights returns the count of insights discovered this session.
func (d *Dream) SessionInsights() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessionInsights
}

// metaInt64 extracts an int64 value from a metadata map, accepting
// int / int64 / float64 (JSON round-trip).
func metaInt64(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func metaInt(m map[string]any, key string) (int, bool) {
	v, ok := metaInt64(m, key)
	return int(v), ok
}
