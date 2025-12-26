package eval

import (
	"context"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// MockCortex is a mock implementation of cognition.Cortex for testing
type MockCortex struct {
	// Configurable responses
	ReflexResults  []cognition.Result
	ReflectResults []cognition.Result
	ResolveResult  *cognition.ResolveResult

	// State tracking
	sessionCtx     *cognition.SessionContext
	retrieveCount  int
	lastRetrieve   time.Time
	insightsChan   chan cognition.Result
	proactiveQueue []cognition.Result
}

// NewMockCortex creates a new mock Cortex
func NewMockCortex() *MockCortex {
	return &MockCortex{
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

	// Default: return empty results
	return []cognition.Result{}, nil
}

// Reflect implements cognition.Reflector
func (m *MockCortex) Reflect(ctx context.Context, q cognition.Query, candidates []cognition.Result) ([]cognition.Result, error) {
	// Simulate 200ms+ latency
	time.Sleep(50 * time.Millisecond)

	if m.ReflectResults != nil {
		return m.ReflectResults, nil
	}

	// Default: return candidates as-is with updated scores
	for i := range candidates {
		candidates[i].Score = 0.5 + float64(len(candidates)-i)*0.1
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

	// Update session context to simulate learning
	m.sessionCtx.LastUpdated = time.Now()

	// Simulate learning topic weights from recent queries
	for _, q := range m.sessionCtx.RecentQueries {
		// Simple: add weight for query text as topic
		if q.Text != "" {
			m.sessionCtx.TopicWeights[q.Text] = 0.5
		}
	}

	return &cognition.ThinkResult{
		Status:     cognition.ThinkRan,
		Operations: 3,
		Duration:   10 * time.Millisecond,
	}, nil
}

// SessionContext implements cognition.Thinker
func (m *MockCortex) SessionContext() *cognition.SessionContext {
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

// Retrieve implements cognition.Cortex
func (m *MockCortex) Retrieve(ctx context.Context, q cognition.Query, mode cognition.RetrieveMode) (*cognition.ResolveResult, error) {
	m.retrieveCount++
	m.lastRetrieve = time.Now()
	m.sessionCtx.RecentQueries = append(m.sessionCtx.RecentQueries, q)

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
