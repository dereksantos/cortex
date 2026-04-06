package cognition

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// Digest implements cognition.Digester for consolidating duplicate insights.
type Digest struct {
	mu sync.Mutex

	// Components
	storage *storage.Storage

	// Config
	config     cognition.DigestConfig
	contextDir string // Root .cortex/ directory for writing knowledge files

	// State
	running       bool
	lastDigest    time.Time
	lastDreamTime time.Time // Set by Dream when it completes

	// State writer for daemon status updates
	stateWriter *StateWriter
}

// NewDigest creates a new Digest instance.
func NewDigest(store *storage.Storage, contextDir string) *Digest {
	return &Digest{
		storage:    store,
		config:     cognition.DefaultDigestConfig(),
		contextDir: contextDir,
	}
}

// SetConfig updates the Digest configuration.
func (d *Digest) SetConfig(cfg cognition.DigestConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.config = cfg
}

// SetStateWriter sets the state writer for daemon status updates.
func (d *Digest) SetStateWriter(sw *StateWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stateWriter = sw
}

// NotifyDreamCompleted is called by Dream to signal digest should run.
func (d *Digest) NotifyDreamCompleted() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastDreamTime = time.Now()
}

// MaybeDigest attempts to consolidate insights after Dream.
func (d *Digest) MaybeDigest(ctx context.Context) (*cognition.DigestResult, error) {
	d.mu.Lock()

	// Check if running
	if d.running {
		d.mu.Unlock()
		return &cognition.DigestResult{Status: cognition.DigestSkippedRunning}, nil
	}

	// Check if Dream ran recently (within last 5 minutes)
	if time.Since(d.lastDreamTime) > 5*time.Minute {
		d.mu.Unlock()
		return &cognition.DigestResult{Status: cognition.DigestSkippedNoDream}, nil
	}

	d.running = true
	stateWriter := d.stateWriter
	d.mu.Unlock()

	// Write state on start
	if stateWriter != nil {
		stateWriter.WriteMode("digest", "Consolidating insights...")
	}

	start := time.Now()
	minDisplay := d.config.MinDisplayDuration

	defer func() {
		// Ensure minimum display duration
		elapsed := time.Since(start)
		if elapsed < minDisplay {
			time.Sleep(minDisplay - elapsed)
		}

		d.mu.Lock()
		d.running = false
		d.lastDigest = time.Now()
		d.mu.Unlock()

		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
	}()

	// Fetch recent insights
	insights, err := d.fetchInsightsAsResults(ctx, 100)
	if err != nil {
		log.Printf("Digest: failed to fetch insights: %v", err)
		return &cognition.DigestResult{Status: cognition.DigestSkippedNoInsights}, nil
	}

	if len(insights) == 0 {
		return &cognition.DigestResult{Status: cognition.DigestSkippedNoInsights}, nil
	}

	// Perform digest and compact storage
	digested, err := d.DigestInsights(ctx, insights)
	if err != nil {
		return nil, fmt.Errorf("digest failed: %w", err)
	}

	// Count groups with duplicates and actually merge them in storage
	groups := 0
	merged := 0
	for _, di := range digested {
		if len(di.Duplicates) > 0 {
			groups++

			// Extract storage IDs and compact
			keepID := parseInsightID(di.Representative.ID)
			if keepID > 0 {
				var deleteIDs []int64
				for _, dup := range di.Duplicates {
					if dupID := parseInsightID(dup.ID); dupID > 0 {
						deleteIDs = append(deleteIDs, dupID)
					}
				}

				if len(deleteIDs) > 0 && d.storage != nil {
					deleted, err := d.storage.MergeInsights(keepID, deleteIDs)
					if err != nil {
						log.Printf("Digest: merge failed for group %s: %v", di.Representative.ID, err)
					} else {
						merged += deleted
					}
				}
			}
		}
	}

	// Write high-quality insights to knowledge/ as committable files
	knowledgeWritten := d.writeKnowledgeFiles(digested)

	if stateWriter != nil && groups > 0 {
		stateWriter.WriteMode("digest", fmt.Sprintf("Compacted %d duplicates from %d groups", merged, groups))
	}

	log.Printf("Digest: completed (%d insights -> %d unique, %d groups, %d merged, %d knowledge files, %v)",
		len(insights), len(digested), groups, merged, knowledgeWritten, time.Since(start))

	return &cognition.DigestResult{
		Status:   cognition.DigestRan,
		Groups:   groups,
		Merged:   merged,
		Duration: time.Since(start),
	}, nil
}

// DigestInsights performs on-demand deduplication of given insights.
func (d *Digest) DigestInsights(ctx context.Context, insights []cognition.Result) ([]cognition.DigestedInsight, error) {
	if len(insights) == 0 {
		return nil, nil
	}

	// Group by category first
	byCategory := make(map[string][]cognition.Result)
	for _, ins := range insights {
		byCategory[ins.Category] = append(byCategory[ins.Category], ins)
	}

	var result []cognition.DigestedInsight

	// Process each category
	for _, categoryInsights := range byCategory {
		digested := d.digestCategory(categoryInsights)
		result = append(result, digested...)
	}

	return result, nil
}

// digestCategory deduplicates insights within a single category.
func (d *Digest) digestCategory(insights []cognition.Result) []cognition.DigestedInsight {
	if len(insights) == 0 {
		return nil
	}

	// Track which insights have been merged into another
	merged := make(map[int]bool)
	var result []cognition.DigestedInsight

	for i, ins := range insights {
		if merged[i] {
			continue
		}

		// Find duplicates for this insight
		var duplicates []cognition.Result
		for j := i + 1; j < len(insights); j++ {
			if merged[j] {
				continue
			}

			sim := textSimilarity(ins.Content, insights[j].Content)
			ngramSim := ngramSimilarity(ins.Content, insights[j].Content, 3)
			// Use max of Jaccard and n-gram similarity to catch paraphrases
			bestSim := sim
			if ngramSim > bestSim {
				bestSim = ngramSim
			}
			if bestSim >= d.config.SimilarityThreshold {
				duplicates = append(duplicates, insights[j])
				merged[j] = true
			}
		}

		// Choose representative based on recency or importance
		representative := ins
		if len(duplicates) > 0 && !d.config.RecencyBias {
			// Find highest importance
			for _, dup := range duplicates {
				if dup.Score > representative.Score {
					// Move current representative to duplicates
					duplicates = append(duplicates, representative)
					representative = dup
				}
			}
			// Remove representative from duplicates if it was added
			var filtered []cognition.Result
			for _, dup := range duplicates {
				if dup.ID != representative.ID {
					filtered = append(filtered, dup)
				}
			}
			duplicates = filtered
		}

		// Calculate average similarity for the group
		avgSim := 0.0
		if len(duplicates) > 0 {
			for _, dup := range duplicates {
				avgSim += textSimilarity(representative.Content, dup.Content)
			}
			avgSim /= float64(len(duplicates))
		}

		result = append(result, cognition.DigestedInsight{
			Representative: representative,
			Duplicates:     duplicates,
			Similarity:     avgSim,
		})
	}

	return result
}

// GetDigestedInsights returns all active insights in deduplicated form.
func (d *Digest) GetDigestedInsights(ctx context.Context, limit int) ([]cognition.DigestedInsight, error) {
	insights, err := d.fetchInsightsAsResults(ctx, limit)
	if err != nil {
		return nil, err
	}
	return d.DigestInsights(ctx, insights)
}

// fetchInsightsAsResults converts storage insights to cognition.Result.
func (d *Digest) fetchInsightsAsResults(_ context.Context, limit int) ([]cognition.Result, error) {
	if d.storage == nil {
		return nil, nil
	}

	storageInsights, err := d.storage.GetRecentInsights(limit)
	if err != nil {
		return nil, err
	}

	var results []cognition.Result
	for _, si := range storageInsights {
		results = append(results, cognition.Result{
			ID:        fmt.Sprintf("insight:%d", si.ID),
			Content:   si.Summary,
			Category:  si.Category,
			Score:     float64(si.Importance) / 10.0,
			Timestamp: si.CreatedAt,
			Tags:      si.Tags,
			Metadata: map[string]any{
				"event_id":  si.EventID,
				"reasoning": si.Reasoning,
			},
		})
	}

	return results, nil
}

// textSimilarity computes Jaccard similarity between two texts.
// Returns a value between 0 (no overlap) and 1 (identical).
func textSimilarity(a, b string) float64 {
	tokensA := tokenize(a)
	tokensB := tokenize(b)

	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0
	}

	// Build sets
	setA := make(map[string]bool)
	for _, t := range tokensA {
		setA[t] = true
	}

	setB := make(map[string]bool)
	for _, t := range tokensB {
		setB[t] = true
	}

	// Compute intersection
	intersection := 0
	for t := range setA {
		if setB[t] {
			intersection++
		}
	}

	// Compute union
	union := len(setA)
	for t := range setB {
		if !setA[t] {
			union++
		}
	}

	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// ngramSimilarity computes Jaccard similarity using character n-grams.
// This catches paraphrases that word-level Jaccard misses.
func ngramSimilarity(a, b string, n int) float64 {
	a = strings.ToLower(a)
	b = strings.ToLower(b)

	ngramsA := ngrams(a, n)
	ngramsB := ngrams(b, n)

	if len(ngramsA) == 0 || len(ngramsB) == 0 {
		return 0
	}

	intersection := 0
	for ng := range ngramsA {
		if ngramsB[ng] {
			intersection++
		}
	}

	union := len(ngramsA)
	for ng := range ngramsB {
		if !ngramsA[ng] {
			union++
		}
	}

	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// ngrams extracts character n-grams from text.
func ngrams(text string, n int) map[string]bool {
	result := make(map[string]bool)
	runes := []rune(text)
	for i := 0; i <= len(runes)-n; i++ {
		result[string(runes[i:i+n])] = true
	}
	return result
}

// tokenize splits text into lowercase word tokens.
func tokenize(text string) []string {
	// Lowercase and split on non-alphanumeric
	text = strings.ToLower(text)
	var tokens []string
	var current strings.Builder

	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			current.WriteRune(r)
		} else if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	// Filter stopwords for better similarity
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "can": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"and": true, "or": true, "but": true, "if": true, "then": true,
		"this": true, "that": true, "these": true, "those": true,
		"it": true, "its": true, "we": true, "you": true, "they": true,
	}

	var filtered []string
	for _, t := range tokens {
		if len(t) > 1 && !stopwords[t] {
			filtered = append(filtered, t)
		}
	}

	return filtered
}

// parseInsightID extracts the int64 storage ID from an insight ID string.
// Format: "insight:123" -> 123
func parseInsightID(id string) int64 {
	if !strings.HasPrefix(id, "insight:") {
		return 0
	}
	numStr := strings.TrimPrefix(id, "insight:")
	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0
	}
	return num
}

// writeKnowledgeFiles writes high-quality digested insights to .cortex/knowledge/ as markdown files.
func (d *Digest) writeKnowledgeFiles(digested []cognition.DigestedInsight) int {
	if d.contextDir == "" {
		return 0
	}

	written := 0
	for _, di := range digested {
		rep := di.Representative
		if rep.Score < 0.7 {
			continue
		}

		category := rep.Category
		if category == "" {
			category = "insights"
		}

		dir := filepath.Join(d.contextDir, "knowledge", category)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("Digest: failed to create knowledge dir %s: %v", dir, err)
			continue
		}

		slug := slugify(rep.Content)
		filePath := filepath.Join(dir, slug+".md")

		// Don't overwrite existing files
		if _, err := os.Stat(filePath); err == nil {
			continue
		}

		tagsStr := ""
		if len(rep.Tags) > 0 {
			tagsStr = fmt.Sprintf("tags: [%s]\n", strings.Join(rep.Tags, ", "))
		}

		content := fmt.Sprintf("---\nid: %s\ncategory: %s\nimportance: %.2f\n%screated: %s\n---\n\n%s\n",
			rep.ID,
			category,
			rep.Score,
			tagsStr,
			time.Now().Format(time.RFC3339),
			rep.Content,
		)

		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			log.Printf("Digest: failed to write knowledge file %s: %v", filePath, err)
			continue
		}
		written++
	}

	return written
}

// slugify converts text to a filesystem-safe slug.
func slugify(text string) string {
	s := strings.ToLower(text)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		// Don't end on a partial word
		if idx := strings.LastIndex(s, "-"); idx > 30 {
			s = s[:idx]
		}
	}
	if s == "" {
		s = "insight"
	}
	return s
}
