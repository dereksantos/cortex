package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

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
type Dream struct {
	mu sync.Mutex

	// Components
	sources  []cognition.DreamSource
	storage  *storage.Storage
	llm      llm.Provider
	activity *ActivityTracker

	// Config
	config cognition.DreamConfig

	// State
	running        bool
	lastDream      time.Time
	insightsChan   chan cognition.Result
	proactiveQueue []cognition.Result

	// Queued transcripts from Stop hooks
	queuedTranscripts []QueuedTranscript

	// State writer for daemon status updates
	stateWriter *StateWriter
}

// NewDream creates a new Dream instance.
func NewDream(store *storage.Storage, provider llm.Provider, activity *ActivityTracker) *Dream {
	return &Dream{
		storage:      store,
		llm:          provider,
		activity:     activity,
		config:       cognition.DefaultDreamConfig(),
		insightsChan: make(chan cognition.Result, 100),
	}
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
	// Check preconditions
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
	d.mu.Unlock()

	// Write state on start with natural language
	if stateWriter != nil {
		stateWriter.WriteMode("dream", "Dreaming about the codebase...")
	}

	start := time.Now()
	minDisplay := d.config.MinDisplayDuration

	defer func() {
		// Ensure minimum display duration for status visibility
		elapsed := time.Since(start)
		if elapsed < minDisplay {
			time.Sleep(minDisplay - elapsed)
		}

		d.mu.Lock()
		d.running = false
		d.lastDream = time.Now()
		d.mu.Unlock()

		// Write idle state on completion
		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
	}()
	budget := d.activity.DreamBudget(d.config.MinBudget, d.config.MaxBudget, d.config.GrowthDuration)
	log.Printf("Dream: starting (budget: %d, sources: %d)", budget, len(d.sources))
	ops := 0
	insights := 0

	// Sample from each source
	for _, source := range d.sources {
		if ops >= budget {
			break
		}

		items, err := source.Sample(ctx, budget-ops)
		if err != nil {
			continue
		}

		for _, item := range items {
			if ops >= budget {
				break
			}

			// Update status with current item being explored
			if stateWriter != nil {
				truncPath := TruncatePath(item.Path, 30)
				if truncPath != "" {
					stateWriter.WriteMode("dream", fmt.Sprintf("Exploring %s...", truncPath))
				}
			}

			// Analyze item for insights
			insight, err := d.analyzeItem(ctx, item, stateWriter)
			if err != nil || insight == nil {
				ops++
				continue
			}

			// Store insight
			if d.storage != nil {
				d.storage.StoreInsight(
					item.ID,
					insight.Category,
					insight.Content,
					int(insight.Score*10),
					insight.Tags,
					"",
				)
			}

			// Send to channel (non-blocking)
			select {
			case d.insightsChan <- *insight:
			default:
			}

			// Flash insight discovery to user
			if stateWriter != nil {
				truncInsight := TruncateInsight(insight.Content, 35)
				stateWriter.WriteMode("insight", fmt.Sprintf("Discovered %s", truncInsight))
				// Brief pause to let user see the insight
				time.Sleep(2 * time.Second)
			}

			// Queue high-value insights for proactive injection
			if insight.Score >= 0.8 {
				d.mu.Lock()
				d.proactiveQueue = append(d.proactiveQueue, *insight)
				d.mu.Unlock()
			}

			insights++
			ops++
		}
	}

	log.Printf("Dream: completed (%d ops, %d insights, %v)", ops, insights, time.Since(start))

	return &cognition.DreamResult{
		Status:     cognition.DreamRan,
		Operations: ops,
		Duration:   time.Since(start),
		Insights:   insights,
	}, nil
}

// analyzeItem uses LLM to extract insights from a sampled item.
func (d *Dream) analyzeItem(ctx context.Context, item cognition.DreamItem, stateWriter *StateWriter) (*cognition.Result, error) {
	if d.llm == nil || !d.llm.IsAvailable() {
		return nil, nil
	}

	// Update status to show we're analyzing
	if stateWriter != nil {
		truncPath := TruncatePath(item.Path, 25)
		if truncPath != "" {
			stateWriter.WriteMode("dream", fmt.Sprintf("Analyzing %s for patterns...", truncPath))
		}
	}

	// Truncate content if too long
	content := item.Content
	if len(content) > 2000 {
		content = content[:2000] + "..."
	}

	prompt := fmt.Sprintf(DreamAnalysisPrompt, content, item.Source, item.Path)

	response, err := d.llm.GenerateWithSystem(ctx, prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return nil, err
	}

	// Check for NO_INSIGHT response
	if strings.Contains(strings.ToUpper(response), "NO_INSIGHT") {
		return nil, nil
	}

	// Parse JSON response
	return d.parseInsightResponse(response, item)
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
	// Find JSON in response
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

	// Validate importance
	if ir.Importance <= 0 || ir.Importance > 1.0 {
		ir.Importance = 0.5
	}

	return &cognition.Result{
		ID:        "dream:" + item.ID,
		Content:   ir.Content,
		Category:  ir.Category,
		Score:     ir.Importance,
		Timestamp: time.Now(),
		Tags:      ir.Tags,
		Metadata: map[string]any{
			"source":      item.Source,
			"source_path": item.Path,
			"source_id":   item.ID,
		},
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

	// Return a copy
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
// Called by Router when a Stop event is received.
func (d *Dream) QueueTranscript(path string, sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check for duplicates
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
