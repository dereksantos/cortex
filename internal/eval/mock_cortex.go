package eval

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
	"gopkg.in/yaml.v3"
)

// CorpusItem represents a test result in the corpus file
type CorpusItem struct {
	ID        string   `yaml:"id"`
	Content   string   `yaml:"content"`
	Category  string   `yaml:"category"`
	Score     float64  `yaml:"score"`
	Tags      []string `yaml:"tags"`
	Timestamp string   `yaml:"timestamp,omitempty"`
}

// CorpusFile represents the structure of a corpus YAML file
type CorpusFile struct {
	Results []CorpusItem `yaml:"results"`
}

// LoadCorpusFile loads a corpus file from disk
func LoadCorpusFile(path string) (*CorpusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read corpus file: %w", err)
	}

	var corpus CorpusFile
	if err := yaml.Unmarshal(data, &corpus); err != nil {
		return nil, fmt.Errorf("failed to parse corpus YAML: %w", err)
	}

	return &corpus, nil
}

// MockCortex is a mock implementation of cognition.Cortex for testing
type MockCortex struct {
	// Configurable responses
	ReflexResults  []cognition.Result
	ReflectResults []cognition.Result
	ResolveResult  *cognition.ResolveResult

	// Corpus for realistic retrieval
	Corpus map[string]cognition.Result

	// Contradiction pairs for Reflect simulation
	ContradictionPairs [][]string

	// State tracking
	mu             sync.RWMutex
	sessionCtx     *cognition.SessionContext
	retrieveCount  int
	lastRetrieve   time.Time
	insightsChan   chan cognition.Result
	proactiveQueue []cognition.Result
}

// NewMockCortex creates a new mock Cortex
func NewMockCortex() *MockCortex {
	return &MockCortex{
		Corpus: make(map[string]cognition.Result),
		sessionCtx: &cognition.SessionContext{
			TopicWeights:           make(map[string]float64),
			RecentQueries:          make([]cognition.Query, 0),
			WarmCache:              make(map[string][]cognition.Result),
			CachedReflect:          make(map[string][]cognition.Result),
			ResolvedContradictions: make(map[string]string),
		},
		insightsChan: make(chan cognition.Result, 10),
	}
}

// WithCorpus loads test results from a YAML corpus file
func (m *MockCortex) WithCorpus(path string) (*MockCortex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("failed to read corpus file: %w", err)
	}

	var corpus CorpusFile
	if err := yaml.Unmarshal(data, &corpus); err != nil {
		return m, fmt.Errorf("failed to parse corpus YAML: %w", err)
	}

	for _, item := range corpus.Results {
		var ts time.Time
		if item.Timestamp != "" {
			ts, _ = time.Parse(time.RFC3339, item.Timestamp)
		} else {
			ts = time.Now()
		}

		m.Corpus[item.ID] = cognition.Result{
			ID:        item.ID,
			Content:   item.Content,
			Category:  item.Category,
			Score:     item.Score,
			Tags:      item.Tags,
			Timestamp: ts,
		}
	}

	return m, nil
}

// WithContradictions sets pairs of result IDs that should be detected as contradictions
func (m *MockCortex) WithContradictions(pairs [][]string) *MockCortex {
	m.ContradictionPairs = pairs
	return m
}

// WithReflexResults sets the results Reflex will return
func (m *MockCortex) WithReflexResults(results []cognition.Result) *MockCortex {
	m.ReflexResults = results
	return m
}

// WithResolveResult sets the result Resolve will return
func (m *MockCortex) WithResolveResult(result *cognition.ResolveResult) *MockCortex {
	m.ResolveResult = result
	return m
}

// Reflex implements cognition.Reflexer
func (m *MockCortex) Reflex(ctx context.Context, q cognition.Query) ([]cognition.Result, error) {
	// Simulate <10ms latency
	time.Sleep(5 * time.Millisecond)

	if m.ReflexResults != nil {
		return m.ReflexResults, nil
	}

	// Search corpus if available
	if len(m.Corpus) > 0 {
		return m.searchCorpus(q), nil
	}

	// Default: return empty results
	return []cognition.Result{}, nil
}

// searchCorpus performs simple text and tag matching against corpus
func (m *MockCortex) searchCorpus(q cognition.Query) []cognition.Result {
	var results []cognition.Result
	queryLower := strings.ToLower(q.Text)
	queryTerms := strings.Fields(queryLower)

	for _, result := range m.Corpus {
		score := 0.0
		contentLower := strings.ToLower(result.Content)

		// Text matching: count term hits
		for _, term := range queryTerms {
			if strings.Contains(contentLower, term) {
				score += 0.2
			}
		}

		// Tag matching
		if len(q.Tags) > 0 {
			tagSet := make(map[string]bool)
			for _, t := range result.Tags {
				tagSet[t] = true
			}
			for _, qt := range q.Tags {
				if tagSet[qt] {
					score += 0.3
				}
			}
		}

		// Category matching
		if q.Categories != nil {
			for _, c := range q.Categories {
				if result.Category == c {
					score += 0.2
					break
				}
			}
		}

		// Apply threshold
		if score >= q.Threshold && score > 0 {
			r := result
			r.Score = score
			results = append(results, r)
		}
	}

	// Sort by score (simple bubble sort for test code)
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Apply limit
	if q.Limit > 0 && len(results) > q.Limit {
		results = results[:q.Limit]
	}

	return results
}

// Reflect implements cognition.Reflector
func (m *MockCortex) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	// Simulate 200ms+ latency
	time.Sleep(50 * time.Millisecond)

	if m.ReflectResults != nil {
		return m.ReflectResults, nil
	}

	// Default: rerank candidates (decisions rank higher)
	for i := range candidates {
		baseScore := 0.5 + float64(len(candidates)-i)*0.1
		// Boost decisions over patterns/insights
		if candidates[i].Category == "decision" {
			baseScore += 0.2
		} else if candidates[i].Category == "constraint" {
			baseScore += 0.15
		}
		candidates[i].Score = baseScore
	}

	// Sort by updated scores
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].Score > candidates[i].Score {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	// Mark contradictions
	if len(m.ContradictionPairs) > 0 {
		idSet := make(map[string]int)
		for i, c := range candidates {
			idSet[c.ID] = i
		}

		for _, pair := range m.ContradictionPairs {
			if len(pair) >= 2 {
				if i1, ok1 := idSet[pair[0]]; ok1 {
					if i2, ok2 := idSet[pair[1]]; ok2 {
						// Mark both as contradicting
						if candidates[i1].Metadata == nil {
							candidates[i1].Metadata = make(map[string]any)
						}
						if candidates[i2].Metadata == nil {
							candidates[i2].Metadata = make(map[string]any)
						}
						candidates[i1].Metadata["contradicts"] = pair[1]
						candidates[i2].Metadata["contradicts"] = pair[0]
					}
				}
			}
		}
	}

	return candidates, nil
}

// Resolve implements cognition.Resolver
func (m *MockCortex) Resolve(ctx context.Context, q cognition.Query, results []cognition.Result) (*cognition.ResolveResult, error) {
	if m.ResolveResult != nil {
		return m.ResolveResult, nil
	}

	// Default behavior: inject if we have results
	decision := cognition.Discard
	confidence := 0.3

	if len(results) > 0 {
		decision = cognition.Inject
		confidence = 0.8
	}

	return &cognition.ResolveResult{
		Decision:   decision,
		Results:    results,
		Confidence: confidence,
		Reason:     "mock decision",
	}, nil
}

// MaybeThink implements cognition.Thinker
func (m *MockCortex) MaybeThink(ctx context.Context) (*cognition.ThinkResult, error) {
	// Simulate background processing
	time.Sleep(10 * time.Millisecond)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update session context to simulate learning
	m.sessionCtx.LastUpdated = time.Now()

	// Learn topic weights from term frequency in recent queries
	termCounts := make(map[string]int)
	for _, q := range m.sessionCtx.RecentQueries {
		terms := strings.Fields(strings.ToLower(q.Text))
		for _, term := range terms {
			// Skip common words
			if len(term) <= 2 || isStopWord(term) {
				continue
			}
			termCounts[term]++
		}
		// Also count tags as topics
		for _, tag := range q.Tags {
			termCounts[tag]++
		}
	}

	// Convert counts to weights (normalized)
	maxCount := 1
	for _, count := range termCounts {
		if count > maxCount {
			maxCount = count
		}
	}

	for term, count := range termCounts {
		// Weight = 0.3 base + 0.5 * (count/max) for repeated terms
		weight := 0.3 + 0.5*float64(count)/float64(maxCount)
		if existing, ok := m.sessionCtx.TopicWeights[term]; ok {
			// Blend with existing weight
			weight = (existing + weight) / 2
		}
		m.sessionCtx.TopicWeights[term] = weight
	}

	// Pre-cache Reflect results for recent queries
	for _, q := range m.sessionCtx.RecentQueries {
		if _, ok := m.sessionCtx.CachedReflect[q.Text]; !ok {
			// Simulate caching by storing placeholder
			if results := m.searchCorpus(q); len(results) > 0 {
				m.sessionCtx.CachedReflect[q.Text] = results
				m.sessionCtx.WarmCache[q.Text] = results
			}
		}
	}

	return &cognition.ThinkResult{
		Status:     cognition.ThinkRan,
		Operations: len(termCounts) + len(m.sessionCtx.RecentQueries),
		Duration:   10 * time.Millisecond,
	}, nil
}

// isStopWord returns true for common words that shouldn't become topics
func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"is": true, "are": true, "was": true, "were": true,
		"in": true, "on": true, "at": true, "to": true, "for": true,
		"of": true, "with": true, "by": true, "from": true,
		"how": true, "what": true, "where": true, "when": true, "why": true,
		"do": true, "does": true, "did": true, "will": true, "would": true,
		"should": true, "could": true, "can": true, "may": true, "might": true,
		"this": true, "that": true, "these": true, "those": true,
		"i": true, "we": true, "you": true, "he": true, "she": true, "it": true,
		"me": true, "us": true, "them": true, "my": true, "our": true, "your": true,
	}
	return stopWords[word]
}

// SessionContext implements cognition.Thinker
func (m *MockCortex) SessionContext() *cognition.SessionContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionCtx
}

// RegisterSource implements cognition.Dreamer
func (m *MockCortex) RegisterSource(source cognition.DreamSource) {
	// Mock: just track that it was registered
}

// MaybeDream implements cognition.Dreamer
func (m *MockCortex) MaybeDream(ctx context.Context) (*cognition.DreamResult, error) {
	// Simulate background exploration
	time.Sleep(100 * time.Millisecond)

	// Generate a mock insight
	insight := cognition.Result{
		ID:        "dream-insight-1",
		Content:   "Discovered connection between auth and session",
		Category:  "insight",
		Score:     0.9,
		Timestamp: time.Now(),
	}

	// Non-blocking send to insights channel
	select {
	case m.insightsChan <- insight:
	default:
	}

	return &cognition.DreamResult{
		Status:     cognition.DreamRan,
		Operations: 5,
		Duration:   100 * time.Millisecond,
		Insights:   1,
	}, nil
}

// Insights implements cognition.Dreamer
func (m *MockCortex) Insights() <-chan cognition.Result {
	return m.insightsChan
}

// ProactiveQueue implements cognition.Dreamer
func (m *MockCortex) ProactiveQueue() []cognition.Result {
	return m.proactiveQueue
}

// ForceIdle implements cognition.Dreamer (no-op for mock)
func (m *MockCortex) ForceIdle() {
	// No-op for mock - always considered idle
}

// ResetForTesting implements cognition.Dreamer (no-op for mock)
func (m *MockCortex) ResetForTesting() {
	// No-op for mock - no interval tracking
}

// Retrieve implements cognition.Cortex
func (m *MockCortex) Retrieve(ctx context.Context, q cognition.Query, mode cognition.RetrieveMode) (*cognition.ResolveResult, error) {
	m.mu.Lock()
	m.retrieveCount++
	m.lastRetrieve = time.Now()
	m.sessionCtx.RecentQueries = append(m.sessionCtx.RecentQueries, q)
	m.mu.Unlock()

	// Run Reflex
	candidates, err := m.Reflex(ctx, q)
	if err != nil {
		return nil, err
	}

	// In Full mode, also run Reflect
	if mode == cognition.Full {
		candidates, err = m.Reflect(ctx, q, candidates)
		if err != nil {
			return nil, err
		}
	}

	// Run Resolve
	result, err := m.Resolve(ctx, q, candidates)
	if err != nil {
		return nil, err
	}

	// Trigger background mode (simplified)
	go m.MaybeThink(ctx)

	return result, nil
}

// AddProactiveInsight adds an insight to the proactive queue
func (m *MockCortex) AddProactiveInsight(result cognition.Result) {
	m.proactiveQueue = append(m.proactiveQueue, result)
}

// AddEventToCorpus adds an event to the corpus for retrieval.
// This bridges the gap between event storage and the Retrieve pipeline.
func (m *MockCortex) AddEventToCorpus(id, content, category string, tags []string, importance int) {
	m.Corpus[id] = cognition.Result{
		ID:        id,
		Content:   content,
		Category:  category,
		Score:     float64(importance) / 10.0, // Normalize importance
		Tags:      tags,
		Timestamp: time.Now(),
		Metadata: map[string]any{
			"importance": importance,
		},
	}
}

// NotifyDreamCompleted implements cognition.Digester
func (m *MockCortex) NotifyDreamCompleted() {
	// No-op for mock
}

// MaybeDigest implements cognition.Digester
func (m *MockCortex) MaybeDigest(ctx context.Context) (*cognition.DigestResult, error) {
	return &cognition.DigestResult{
		Status:   cognition.DigestRan,
		Groups:   0,
		Merged:   0,
		Duration: 10 * time.Millisecond,
	}, nil
}

// DigestInsights implements cognition.Digester
func (m *MockCortex) DigestInsights(ctx context.Context, insights []cognition.Result) ([]cognition.DigestedInsight, error) {
	// Simple mock: return each insight as its own group
	var digested []cognition.DigestedInsight
	for _, ins := range insights {
		digested = append(digested, cognition.DigestedInsight{
			Representative: ins,
			Duplicates:     nil,
			Similarity:     0,
		})
	}
	return digested, nil
}

// GetDigestedInsights implements cognition.Digester
func (m *MockCortex) GetDigestedInsights(ctx context.Context, limit int) ([]cognition.DigestedInsight, error) {
	return nil, nil
}
