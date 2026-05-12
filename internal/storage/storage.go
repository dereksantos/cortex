// Package storage provides event sourcing storage with JSONL files and in-memory indexes
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/viterin/vek"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// Event is a simplified storage event (for processor use)
type Event struct {
	ID         string
	ToolName   string
	ToolResult string
}

// Storage handles event storage with JSONL files and in-memory indexes
type Storage struct {
	cfg       *config.Config
	dataDir   string
	projectID string // Tags all written records; empty string matches all on read
	mu        sync.RWMutex

	// Event indexes
	events        map[string]*events.Event // id -> Event
	eventsByTime  []*events.Event          // sorted by timestamp DESC
	sessionEvents map[string][]string      // session_id -> []event_id

	// Entity indexes
	entities     map[int64]*Entity // id -> Entity
	entityByKey  map[string]int64  // "type:name" -> id
	nextEntityID int64

	// Relationship indexes
	relationships map[int64]*Relationship // id -> Relationship
	relsByEntity  map[int64][]int64       // entity_id -> []relationship_id (from or to)
	nextRelID     int64

	// Insight indexes
	insights          map[int64]*Insight    // id -> Insight
	insightsByTime    []*Insight            // sorted by created_at DESC
	insightsByCat     map[string][]*Insight // category -> sorted by importance DESC, created_at DESC
	insightsBySession map[string][]*Insight // session_id -> insights
	insightsBySource  map[string][]*Insight // source_type -> insights
	nextInsightID     int64

	// Embedding data
	embeddings map[string]*embeddingEntry // "contentID:contentType" -> entry

	// Session indexes
	sessions       map[string]*SessionMetadata // session_id -> metadata
	sessionsByTime []*SessionMetadata          // sorted by started_at DESC
	nextSessionID  int64

	// Observation indexes (slice O3)
	observations map[string]map[string]*Observation // uri -> content_hash -> Observation

	// Contradiction indexes (slice R2)
	contradictions []*Contradiction

	// Retrieval indexes (slice Z2)
	retrievals []*Retrieval

	// SessionContext snapshots (slice T2)
	sessionContextSnapshots []*SessionContextSnapshot

	// Feedback indexes (slice B3)
	feedbackAll    []*Feedback
	feedbackByID   map[string][]*Feedback // graded_id -> feedbacks
	retractedIDs   map[string]bool        // graded_ids with a retraction

	// Open file handles for append
	eventFile         *os.File
	insightFile       *os.File
	entityFile        *os.File
	relFile           *os.File
	sessionFile       *os.File
	embFile           *os.File
	observationFile   *os.File
	contradictionFile *os.File
	retrievalFile     *os.File
	sessionCtxFile    *os.File
	feedbackFile      *os.File
}

type embeddingEntry struct {
	ProjectID   string    `json:"project_id,omitempty"`
	ContentID   string    `json:"content_id"`
	ContentType string    `json:"content_type"`
	Vector      []float32 `json:"vector"`
	ModelName   string    `json:"model_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// --- JSONL record types ---

type eventRecord struct {
	ProjectID      string                 `json:"project_id,omitempty"`
	ID             string                 `json:"id"`
	Source         events.Source          `json:"source"`
	EventType      events.EventType       `json:"event_type"`
	Timestamp      time.Time              `json:"timestamp"`
	ToolName       string                 `json:"tool_name,omitempty"`
	ToolInput      map[string]interface{} `json:"tool_input,omitempty"`
	ToolResult     string                 `json:"tool_result,omitempty"`
	Prompt         string                 `json:"prompt,omitempty"`
	TranscriptPath string                 `json:"transcript_path,omitempty"`
	Context        events.EventContext    `json:"context"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type entityRecord struct {
	ProjectID string    `json:"project_id,omitempty"`
	Op        string    `json:"op,omitempty"` // "" = insert/upsert, "delete" = soft delete
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

type relationshipRecord struct {
	ProjectID    string    `json:"project_id,omitempty"`
	Op           string    `json:"op,omitempty"`
	ID           int64     `json:"id"`
	FromEntityID int64     `json:"from_entity_id"`
	ToEntityID   int64     `json:"to_entity_id"`
	RelationType string    `json:"relation_type"`
	EventID      string    `json:"event_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type insightRecord struct {
	ProjectID    string    `json:"project_id,omitempty"`
	Op           string    `json:"op,omitempty"` // "" = insert, "delete" = soft delete, "update" = replace
	ID           int64     `json:"id"`
	EventID      string    `json:"event_id"`
	Category     string    `json:"category"`
	Summary      string    `json:"summary"`
	Importance   int       `json:"importance"`
	Tags         []string  `json:"tags"`
	Reasoning    string    `json:"reasoning,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	SourceType   string    `json:"source_type,omitempty"`
	WasRetrieved bool      `json:"was_retrieved,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// feedbackRecord projects a feedback.* journal entry. One row per
// grading event (correction/confirmation/retraction). Provides the input
// for supersession flags on derivation rows.
type feedbackRecord struct {
	ProjectID     string    `json:"project_id,omitempty"`
	Type          string    `json:"type"` // feedback.<kind>
	GradedID      string    `json:"graded_id,omitempty"`
	GradedOffset  int64     `json:"graded_offset,omitempty"`
	Note          string    `json:"note,omitempty"`
	Replacement   string    `json:"replacement,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	JournalOffset int64     `json:"journal_offset,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// Feedback is the derived view of a recorded feedback entry.
type Feedback struct {
	Type          string
	GradedID      string
	GradedOffset  int64
	Note          string
	Replacement   string
	Reason        string
	SessionID     string
	JournalOffset int64
	RecordedAt    time.Time
}

// sessionContextRecord projects a think.session_context snapshot. One
// row per Think cycle. Earlier snapshots are kept for replay / debugging.
type sessionContextRecord struct {
	ProjectID     string             `json:"project_id,omitempty"`
	TopicWeights  map[string]float64 `json:"topic_weights"`
	RecentQueries []string           `json:"recent_queries,omitempty"`
	CachedQueries []string           `json:"cached_queries,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
	JournalOffset int64              `json:"journal_offset,omitempty"`
	RecordedAt    time.Time          `json:"recorded_at"`
}

// SessionContextSnapshot is the derived view of one recorded
// think.session_context journal entry.
type SessionContextSnapshot struct {
	TopicWeights  map[string]float64
	RecentQueries []string
	CachedQueries []string
	SessionID     string
	JournalOffset int64
	RecordedAt    time.Time
}

// retrievalRecord projects a resolve.retrieval journal entry. Captures one
// retrieval decision; the aggregate stats (counts by decision) are
// re-derived in-memory at rebuild time.
type retrievalRecord struct {
	ProjectID     string    `json:"project_id,omitempty"`
	QueryText     string    `json:"query_text"`
	Decision      string    `json:"decision"`
	Confidence    float64   `json:"confidence"`
	ResultCount   int       `json:"result_count"`
	InjectedIDs   []string  `json:"injected_ids,omitempty"`
	AvgScore      float64   `json:"avg_score,omitempty"`
	MaxScore      float64   `json:"max_score,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	JournalOffset int64     `json:"journal_offset,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// Retrieval is the derived view of one recorded retrieval decision.
type Retrieval struct {
	QueryText     string
	Decision      string
	Confidence    float64
	ResultCount   int
	InjectedIDs   []string
	AvgScore      float64
	MaxScore      float64
	Reason        string
	SessionID     string
	JournalOffset int64
	RecordedAt    time.Time
}

// RetrievalStats aggregates the recorded retrievals: total count + count
// per decision.
type RetrievalStats struct {
	Total       int
	ByDecision  map[string]int
}

// contradictionRecord projects a contradiction detected during reflect.rerank.
// Each record links one pairwise (or n-way) conflict back to the journal
// offset of the rerank that detected it.
type contradictionRecord struct {
	ProjectID     string    `json:"project_id,omitempty"`
	IDs           []string  `json:"ids"`
	Reason        string    `json:"reason"`
	JournalOffset int64     `json:"journal_offset,omitempty"`
	QueryText     string    `json:"query_text,omitempty"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// Contradiction is the derived view of one recorded conflict between
// candidates during a Reflect rerank.
type Contradiction struct {
	IDs           []string
	Reason        string
	JournalOffset int64
	QueryText     string
	RecordedAt    time.Time
}

// observationRecord projects a journal observation entry into the storage
// log. Two observations with the same (URI, ContentHash) are deduplicated
// at record time — the substrate hasn't changed, no new evidence.
type observationRecord struct {
	ProjectID   string    `json:"project_id,omitempty"`
	Type        string    `json:"type"` // observation.<kind>
	SourceName  string    `json:"source_name"`
	URI         string    `json:"uri"`
	ContentHash string    `json:"content_hash"`
	Size        int64     `json:"size,omitempty"`
	Modified    time.Time `json:"modified,omitempty"`
	JournalOffset int64   `json:"journal_offset,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// Observation is the derived view of a recorded substrate sighting.
type Observation struct {
	Type          string
	SourceName    string
	URI           string
	ContentHash   string
	Size          int64
	Modified      time.Time
	JournalOffset int64
	RecordedAt    time.Time
}

type sessionRecord struct {
	ProjectID     string    `json:"project_id,omitempty"`
	Op            string    `json:"op,omitempty"` // "" = insert, "update" = replace
	ID            int64     `json:"id"`
	SessionID     string    `json:"session_id"`
	StartedAt     time.Time `json:"started_at"`
	InitialPrompt string    `json:"initial_prompt,omitempty"`
	EventCount    int       `json:"event_count"`
	LastAction    string    `json:"last_action,omitempty"`
	LastActionAt  time.Time `json:"last_action_at,omitempty"`
	ProjectPath   string    `json:"project_path,omitempty"`
}

// New creates a new Storage instance backed by JSONL files.
func New(cfg *config.Config) (*Storage, error) {
	dataDir := filepath.Join(cfg.ContextDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	s := &Storage{
		cfg:               cfg,
		dataDir:           dataDir,
		projectID:         cfg.ProjectID,
		events:            make(map[string]*events.Event),
		sessionEvents:     make(map[string][]string),
		entities:          make(map[int64]*Entity),
		entityByKey:       make(map[string]int64),
		nextEntityID:      1, // Start at 1 to match SQLite AUTOINCREMENT behavior
		relationships:     make(map[int64]*Relationship),
		relsByEntity:      make(map[int64][]int64),
		nextRelID:         1,
		insights:          make(map[int64]*Insight),
		insightsByCat:     make(map[string][]*Insight),
		insightsBySession: make(map[string][]*Insight),
		insightsBySource:  make(map[string][]*Insight),
		nextInsightID:     1,
		embeddings:        make(map[string]*embeddingEntry),
		sessions:          make(map[string]*SessionMetadata),
		nextSessionID:     1,
		observations:      make(map[string]map[string]*Observation),
		feedbackByID:      make(map[string][]*Feedback),
		retractedIDs:      make(map[string]bool),
	}

	// Rebuild in-memory indexes from JSONL files
	if err := s.rebuildIndexes(); err != nil {
		return nil, fmt.Errorf("failed to rebuild indexes: %w", err)
	}

	// Open files for append
	var err error
	s.eventFile, err = openAppend(filepath.Join(dataDir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("failed to open events file: %w", err)
	}
	s.insightFile, err = openAppend(filepath.Join(dataDir, "insights.jsonl"))
	if err != nil {
		s.eventFile.Close()
		return nil, fmt.Errorf("failed to open insights file: %w", err)
	}
	s.entityFile, err = openAppend(filepath.Join(dataDir, "entities.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		return nil, fmt.Errorf("failed to open entities file: %w", err)
	}
	s.relFile, err = openAppend(filepath.Join(dataDir, "relationships.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		return nil, fmt.Errorf("failed to open relationships file: %w", err)
	}
	s.sessionFile, err = openAppend(filepath.Join(dataDir, "sessions.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		return nil, fmt.Errorf("failed to open sessions file: %w", err)
	}
	s.embFile, err = openAppend(filepath.Join(dataDir, "embeddings.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		return nil, fmt.Errorf("failed to open embeddings file: %w", err)
	}
	s.observationFile, err = openAppend(filepath.Join(dataDir, "observations.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		s.embFile.Close()
		return nil, fmt.Errorf("failed to open observations file: %w", err)
	}
	s.contradictionFile, err = openAppend(filepath.Join(dataDir, "contradictions.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		s.embFile.Close()
		s.observationFile.Close()
		return nil, fmt.Errorf("failed to open contradictions file: %w", err)
	}
	s.retrievalFile, err = openAppend(filepath.Join(dataDir, "retrievals.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		s.embFile.Close()
		s.observationFile.Close()
		s.contradictionFile.Close()
		return nil, fmt.Errorf("failed to open retrievals file: %w", err)
	}
	s.sessionCtxFile, err = openAppend(filepath.Join(dataDir, "session_context.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		s.embFile.Close()
		s.observationFile.Close()
		s.contradictionFile.Close()
		s.retrievalFile.Close()
		return nil, fmt.Errorf("failed to open session_context file: %w", err)
	}
	s.feedbackFile, err = openAppend(filepath.Join(dataDir, "feedback.jsonl"))
	if err != nil {
		s.eventFile.Close()
		s.insightFile.Close()
		s.entityFile.Close()
		s.relFile.Close()
		s.sessionFile.Close()
		s.embFile.Close()
		s.observationFile.Close()
		s.contradictionFile.Close()
		s.retrievalFile.Close()
		s.sessionCtxFile.Close()
		return nil, fmt.Errorf("failed to open feedback file: %w", err)
	}

	return s, nil
}

// Close closes all open file handles
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for _, f := range []*os.File{s.eventFile, s.insightFile, s.entityFile, s.relFile, s.sessionFile, s.embFile, s.observationFile, s.contradictionFile, s.retrievalFile, s.sessionCtxFile, s.feedbackFile} {
		if f != nil {
			if err := f.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// TruncateEventLog removes events.jsonl and resets the in-memory event
// indexes. Used by `cortex journal rebuild` to clear the derived event
// state before replaying the journal — see docs/journal.md principle 1
// (CQRS): derived state must be regeneratable from the journal.
//
// Caller must ensure no concurrent reads are in flight; rebuild is
// expected to run with the daemon stopped.
//
// Insights, entities, relationships, sessions, and embeddings are NOT
// touched — those derive from other writer-classes (dream, reflect, etc.)
// and have their own rebuild paths (slices D2, R2, etc.).
func (s *Storage) TruncateEventLog() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.eventFile != nil {
		if err := s.eventFile.Close(); err != nil {
			return fmt.Errorf("close event file: %w", err)
		}
		s.eventFile = nil
	}
	eventPath := filepath.Join(s.dataDir, "events.jsonl")
	if err := os.Remove(eventPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", eventPath, err)
	}

	s.events = make(map[string]*events.Event)
	s.eventsByTime = nil
	s.sessionEvents = make(map[string][]string)

	f, err := openAppend(eventPath)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", eventPath, err)
	}
	s.eventFile = f
	return nil
}

// --- Index rebuild ---

func (s *Storage) rebuildIndexes() error {
	if err := s.rebuildEventIndexes(); err != nil {
		return err
	}
	if err := s.rebuildEntityIndexes(); err != nil {
		return err
	}
	if err := s.rebuildRelationshipIndexes(); err != nil {
		return err
	}
	if err := s.rebuildInsightIndexes(); err != nil {
		return err
	}
	if err := s.rebuildEmbeddingIndexes(); err != nil {
		return err
	}
	if err := s.rebuildSessionIndexes(); err != nil {
		return err
	}
	if err := s.rebuildObservationIndexes(); err != nil {
		return err
	}
	if err := s.rebuildContradictionIndexes(); err != nil {
		return err
	}
	if err := s.rebuildRetrievalIndexes(); err != nil {
		return err
	}
	if err := s.rebuildSessionContextIndexes(); err != nil {
		return err
	}
	if err := s.rebuildFeedbackIndexes(); err != nil {
		return err
	}
	return nil
}

// rebuildFeedbackIndexes replays feedback.jsonl into the feedback maps.
func (s *Storage) rebuildFeedbackIndexes() error {
	records, err := readLines[feedbackRecord](filepath.Join(s.dataDir, "feedback.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		fb := &Feedback{
			Type:          r.Type,
			GradedID:      r.GradedID,
			GradedOffset:  r.GradedOffset,
			Note:          r.Note,
			Replacement:   r.Replacement,
			Reason:        r.Reason,
			SessionID:     r.SessionID,
			JournalOffset: r.JournalOffset,
			RecordedAt:    r.RecordedAt,
		}
		s.feedbackAll = append(s.feedbackAll, fb)
		if r.GradedID != "" {
			s.feedbackByID[r.GradedID] = append(s.feedbackByID[r.GradedID], fb)
			if r.Type == "feedback.retraction" {
				s.retractedIDs[r.GradedID] = true
			}
		}
	}
	return nil
}

// RecordFeedback projects one feedback.* entry. Updates the by-id index
// and the retracted-ids set. Append-only.
func (s *Storage) RecordFeedback(f *Feedback) error {
	if f == nil {
		return fmt.Errorf("nil feedback")
	}
	if f.Type == "" {
		return fmt.Errorf("feedback requires Type")
	}
	if f.GradedID == "" && f.GradedOffset == 0 {
		return fmt.Errorf("feedback requires GradedID or GradedOffset")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if f.RecordedAt.IsZero() {
		f.RecordedAt = time.Now().UTC()
	}
	rec := feedbackRecord{
		ProjectID:     s.projectID,
		Type:          f.Type,
		GradedID:      f.GradedID,
		GradedOffset:  f.GradedOffset,
		Note:          f.Note,
		Replacement:   f.Replacement,
		Reason:        f.Reason,
		SessionID:     f.SessionID,
		JournalOffset: f.JournalOffset,
		RecordedAt:    f.RecordedAt,
	}
	if err := appendLine(s.feedbackFile, rec); err != nil {
		return fmt.Errorf("append feedback: %w", err)
	}
	s.feedbackAll = append(s.feedbackAll, f)
	if f.GradedID != "" {
		s.feedbackByID[f.GradedID] = append(s.feedbackByID[f.GradedID], f)
		if f.Type == "feedback.retraction" {
			s.retractedIDs[f.GradedID] = true
		}
	}
	return nil
}

// FeedbackFor returns the feedback entries that grade a given derivation
// (matched by GradedID). Most-recent-last.
func (s *Storage) FeedbackFor(gradedID string) []*Feedback {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Feedback, len(s.feedbackByID[gradedID]))
	copy(out, s.feedbackByID[gradedID])
	return out
}

// IsRetracted reports whether the given graded ID has at least one
// feedback.retraction entry recorded. Used by query paths to hide
// retracted derivations from results.
func (s *Storage) IsRetracted(gradedID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retractedIDs[gradedID]
}

// rebuildSessionContextIndexes replays session_context.jsonl into the
// snapshot list.
func (s *Storage) rebuildSessionContextIndexes() error {
	records, err := readLines[sessionContextRecord](filepath.Join(s.dataDir, "session_context.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		s.sessionContextSnapshots = append(s.sessionContextSnapshots, &SessionContextSnapshot{
			TopicWeights:  r.TopicWeights,
			RecentQueries: r.RecentQueries,
			CachedQueries: r.CachedQueries,
			SessionID:     r.SessionID,
			JournalOffset: r.JournalOffset,
			RecordedAt:    r.RecordedAt,
		})
	}
	return nil
}

// RecordSessionContextSnapshot projects one think.session_context entry.
// Append-only — every Think cycle adds a snapshot.
func (s *Storage) RecordSessionContextSnapshot(snap *SessionContextSnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil snapshot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap.RecordedAt.IsZero() {
		snap.RecordedAt = time.Now().UTC()
	}
	rec := sessionContextRecord{
		ProjectID:     s.projectID,
		TopicWeights:  snap.TopicWeights,
		RecentQueries: snap.RecentQueries,
		CachedQueries: snap.CachedQueries,
		SessionID:     snap.SessionID,
		JournalOffset: snap.JournalOffset,
		RecordedAt:    snap.RecordedAt,
	}
	if err := appendLine(s.sessionCtxFile, rec); err != nil {
		return fmt.Errorf("append session_context: %w", err)
	}
	s.sessionContextSnapshots = append(s.sessionContextSnapshots, snap)
	return nil
}

// LatestSessionContext returns the most recent recorded snapshot, or nil.
func (s *Storage) LatestSessionContext() *SessionContextSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sessionContextSnapshots) == 0 {
		return nil
	}
	return s.sessionContextSnapshots[len(s.sessionContextSnapshots)-1]
}

// SessionContextSnapshotCount returns the number of recorded snapshots.
func (s *Storage) SessionContextSnapshotCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessionContextSnapshots)
}

// rebuildRetrievalIndexes replays retrievals.jsonl into the in-memory list.
func (s *Storage) rebuildRetrievalIndexes() error {
	records, err := readLines[retrievalRecord](filepath.Join(s.dataDir, "retrievals.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		s.retrievals = append(s.retrievals, &Retrieval{
			QueryText:     r.QueryText,
			Decision:      r.Decision,
			Confidence:    r.Confidence,
			ResultCount:   r.ResultCount,
			InjectedIDs:   r.InjectedIDs,
			AvgScore:      r.AvgScore,
			MaxScore:      r.MaxScore,
			Reason:        r.Reason,
			SessionID:     r.SessionID,
			JournalOffset: r.JournalOffset,
			RecordedAt:    r.RecordedAt,
		})
	}
	return nil
}

// RecordRetrieval projects one resolve.retrieval entry. Append-only — no
// dedup. Rebuild safely re-projects everything.
func (s *Storage) RecordRetrieval(r *Retrieval) error {
	if r == nil {
		return fmt.Errorf("nil retrieval")
	}
	if r.Decision == "" {
		return fmt.Errorf("retrieval requires Decision")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	rec := retrievalRecord{
		ProjectID:     s.projectID,
		QueryText:     r.QueryText,
		Decision:      r.Decision,
		Confidence:    r.Confidence,
		ResultCount:   r.ResultCount,
		InjectedIDs:   r.InjectedIDs,
		AvgScore:      r.AvgScore,
		MaxScore:      r.MaxScore,
		Reason:        r.Reason,
		SessionID:     r.SessionID,
		JournalOffset: r.JournalOffset,
		RecordedAt:    r.RecordedAt,
	}
	if err := appendLine(s.retrievalFile, rec); err != nil {
		return fmt.Errorf("append retrieval: %w", err)
	}
	s.retrievals = append(s.retrievals, r)
	return nil
}

// GetRetrievals returns the most recent N retrievals.
func (s *Storage) GetRetrievals(limit int) []*Retrieval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.retrievals)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]*Retrieval, 0, n)
	for i := len(s.retrievals) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.retrievals[i])
	}
	return out
}

// GetRetrievalStats aggregates the recorded retrievals into total +
// per-decision counts. Computed at call time from the in-memory list.
func (s *Storage) GetRetrievalStats() RetrievalStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := RetrievalStats{
		ByDecision: make(map[string]int),
	}
	for _, r := range s.retrievals {
		stats.Total++
		stats.ByDecision[r.Decision]++
	}
	return stats
}

// rebuildContradictionIndexes replays contradictions.jsonl into the
// in-memory list. Order preserved.
func (s *Storage) rebuildContradictionIndexes() error {
	records, err := readLines[contradictionRecord](filepath.Join(s.dataDir, "contradictions.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		s.contradictions = append(s.contradictions, &Contradiction{
			IDs:           r.IDs,
			Reason:        r.Reason,
			JournalOffset: r.JournalOffset,
			QueryText:     r.QueryText,
			RecordedAt:    r.RecordedAt,
		})
	}
	return nil
}

// RecordContradiction projects one contradiction detected during reranking.
// Append-only — no dedup. Replay during rebuild may produce duplicate rows
// if the same rerank entry projects twice; callers can dedup at query
// time by (JournalOffset, IDs joined).
func (s *Storage) RecordContradiction(c *Contradiction) error {
	if c == nil {
		return fmt.Errorf("nil contradiction")
	}
	if c.Reason == "" || len(c.IDs) == 0 {
		return fmt.Errorf("contradiction requires Reason and at least one ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if c.RecordedAt.IsZero() {
		c.RecordedAt = time.Now().UTC()
	}
	rec := contradictionRecord{
		ProjectID:     s.projectID,
		IDs:           c.IDs,
		Reason:        c.Reason,
		JournalOffset: c.JournalOffset,
		QueryText:     c.QueryText,
		RecordedAt:    c.RecordedAt,
	}
	if err := appendLine(s.contradictionFile, rec); err != nil {
		return fmt.Errorf("append contradiction: %w", err)
	}
	s.contradictions = append(s.contradictions, c)
	return nil
}

// GetContradictions returns the most recent N contradictions.
func (s *Storage) GetContradictions(limit int) []*Contradiction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.contradictions)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]*Contradiction, 0, n)
	for i := len(s.contradictions) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.contradictions[i])
	}
	return out
}

// rebuildObservationIndexes replays observations.jsonl into the in-memory
// dedup map. Later observations of the same (URI, content_hash) overwrite
// earlier ones (preserving the most recent JournalOffset / RecordedAt
// metadata) but do not produce duplicate keys.
func (s *Storage) rebuildObservationIndexes() error {
	records, err := readLines[observationRecord](filepath.Join(s.dataDir, "observations.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.URI == "" || r.ContentHash == "" {
			continue
		}
		byHash, ok := s.observations[r.URI]
		if !ok {
			byHash = make(map[string]*Observation)
			s.observations[r.URI] = byHash
		}
		byHash[r.ContentHash] = &Observation{
			Type:          r.Type,
			SourceName:    r.SourceName,
			URI:           r.URI,
			ContentHash:   r.ContentHash,
			Size:          r.Size,
			Modified:      r.Modified,
			JournalOffset: r.JournalOffset,
			RecordedAt:    r.RecordedAt,
		}
	}
	return nil
}

// RecordObservation projects a journal observation entry. Idempotent on
// (URI, content_hash): if the substrate hasn't changed since the last
// observation, returns false and does not append a new record.
// Returns true when a new (URI, content_hash) is recorded.
func (s *Storage) RecordObservation(o *Observation) (bool, error) {
	if o == nil {
		return false, fmt.Errorf("nil observation")
	}
	if o.URI == "" || o.ContentHash == "" {
		return false, fmt.Errorf("observation requires URI and ContentHash")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	byHash, ok := s.observations[o.URI]
	if !ok {
		byHash = make(map[string]*Observation)
		s.observations[o.URI] = byHash
	}
	if _, exists := byHash[o.ContentHash]; exists {
		return false, nil
	}

	if o.RecordedAt.IsZero() {
		o.RecordedAt = time.Now().UTC()
	}
	rec := observationRecord{
		ProjectID:     s.projectID,
		Type:          o.Type,
		SourceName:    o.SourceName,
		URI:           o.URI,
		ContentHash:   o.ContentHash,
		Size:          o.Size,
		Modified:      o.Modified,
		JournalOffset: o.JournalOffset,
		RecordedAt:    o.RecordedAt,
	}
	if err := appendLine(s.observationFile, rec); err != nil {
		return false, fmt.Errorf("append observation: %w", err)
	}
	byHash[o.ContentHash] = o
	return true, nil
}

// HasObservation reports whether (uri, contentHash) is already recorded.
// Used by `cortex journal verify` and by callers that want to short-circuit
// substrate parsing when content hasn't changed.
func (s *Storage) HasObservation(uri, contentHash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byHash, ok := s.observations[uri]
	if !ok {
		return false
	}
	_, exists := byHash[contentHash]
	return exists
}

func (s *Storage) rebuildEventIndexes() error {
	records, err := readLines[eventRecord](filepath.Join(s.dataDir, "events.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		ev := &events.Event{
			ID:             r.ID,
			Source:         r.Source,
			EventType:      r.EventType,
			Timestamp:      r.Timestamp,
			ToolName:       r.ToolName,
			ToolInput:      r.ToolInput,
			ToolResult:     r.ToolResult,
			Prompt:         r.Prompt,
			TranscriptPath: r.TranscriptPath,
			Context:        r.Context,
			Metadata:       r.Metadata,
		}
		s.events[ev.ID] = ev
		s.eventsByTime = append(s.eventsByTime, ev)
		if ev.Context.SessionID != "" {
			s.sessionEvents[ev.Context.SessionID] = append(s.sessionEvents[ev.Context.SessionID], ev.ID)
		}
	}
	sort.Slice(s.eventsByTime, func(i, j int) bool {
		return s.eventsByTime[i].Timestamp.After(s.eventsByTime[j].Timestamp)
	})
	return nil
}

func (s *Storage) rebuildEntityIndexes() error {
	records, err := readLines[entityRecord](filepath.Join(s.dataDir, "entities.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Op == "delete" {
			delete(s.entities, r.ID)
			// find and remove from entityByKey
			for k, v := range s.entityByKey {
				if v == r.ID {
					delete(s.entityByKey, k)
					break
				}
			}
			continue
		}
		e := &Entity{
			ID:        r.ID,
			Type:      r.Type,
			Name:      r.Name,
			FirstSeen: r.FirstSeen,
			LastSeen:  r.LastSeen,
		}
		// If entity already exists (upsert), update it
		key := r.Type + ":" + r.Name
		if existingID, ok := s.entityByKey[key]; ok {
			existing := s.entities[existingID]
			if existing != nil {
				existing.LastSeen = r.LastSeen
				continue
			}
		}
		s.entities[r.ID] = e
		s.entityByKey[key] = r.ID
		if r.ID >= s.nextEntityID {
			s.nextEntityID = r.ID + 1
		}
	}
	return nil
}

func (s *Storage) rebuildRelationshipIndexes() error {
	records, err := readLines[relationshipRecord](filepath.Join(s.dataDir, "relationships.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Op == "delete" {
			delete(s.relationships, r.ID)
			continue
		}
		rel := &Relationship{
			ID:           r.ID,
			FromEntity:   s.entities[r.FromEntityID],
			ToEntity:     s.entities[r.ToEntityID],
			RelationType: r.RelationType,
			EventID:      r.EventID,
			CreatedAt:    r.CreatedAt,
		}
		s.relationships[r.ID] = rel
		s.relsByEntity[r.FromEntityID] = append(s.relsByEntity[r.FromEntityID], r.ID)
		s.relsByEntity[r.ToEntityID] = append(s.relsByEntity[r.ToEntityID], r.ID)
		if r.ID >= s.nextRelID {
			s.nextRelID = r.ID + 1
		}
	}
	return nil
}

func (s *Storage) rebuildInsightIndexes() error {
	records, err := readLines[insightRecord](filepath.Join(s.dataDir, "insights.jsonl"))
	if err != nil {
		return err
	}
	// Process in order — last write wins for updates, deletes remove
	for _, r := range records {
		switch r.Op {
		case "delete":
			s.removeInsightFromIndexes(r.ID)
			delete(s.insights, r.ID)
		case "update":
			// Remove old version from secondary indexes, then re-add
			s.removeInsightFromIndexes(r.ID)
			insight := recordToInsight(r)
			s.insights[r.ID] = insight
			s.addInsightToSecondaryIndexes(insight)
		default:
			// Insert
			insight := recordToInsight(r)
			s.insights[r.ID] = insight
			s.addInsightToSecondaryIndexes(insight)
			if r.ID >= s.nextInsightID {
				s.nextInsightID = r.ID + 1
			}
		}
	}
	// Sort secondary indexes
	sort.Slice(s.insightsByTime, func(i, j int) bool {
		return s.insightsByTime[i].CreatedAt.After(s.insightsByTime[j].CreatedAt)
	})
	for cat := range s.insightsByCat {
		sortInsightsByImportance(s.insightsByCat[cat])
	}
	for src := range s.insightsBySource {
		sortInsightsByImportance(s.insightsBySource[src])
	}
	return nil
}

func (s *Storage) rebuildEmbeddingIndexes() error {
	records, err := readLines[embeddingEntry](filepath.Join(s.dataDir, "embeddings.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		key := r.ContentID + ":" + r.ContentType
		entry := r // copy
		s.embeddings[key] = &entry
	}
	return nil
}

func (s *Storage) rebuildSessionIndexes() error {
	records, err := readLines[sessionRecord](filepath.Join(s.dataDir, "sessions.jsonl"))
	if err != nil {
		return err
	}
	for _, r := range records {
		sess := &SessionMetadata{
			ID:            r.ID,
			SessionID:     r.SessionID,
			StartedAt:     r.StartedAt,
			InitialPrompt: r.InitialPrompt,
			EventCount:    r.EventCount,
			LastAction:    r.LastAction,
			LastActionAt:  r.LastActionAt,
			ProjectPath:   r.ProjectPath,
		}
		s.sessions[r.SessionID] = sess
		if r.ID >= s.nextSessionID {
			s.nextSessionID = r.ID + 1
		}
	}
	// Build sessionsByTime from map
	s.sessionsByTime = nil
	for _, sess := range s.sessions {
		s.sessionsByTime = append(s.sessionsByTime, sess)
	}
	sort.Slice(s.sessionsByTime, func(i, j int) bool {
		return s.sessionsByTime[i].StartedAt.After(s.sessionsByTime[j].StartedAt)
	})
	return nil
}

// --- Index helpers ---

func recordToInsight(r insightRecord) *Insight {
	return &Insight{
		ID:           r.ID,
		EventID:      r.EventID,
		Category:     r.Category,
		Summary:      r.Summary,
		Importance:   r.Importance,
		Tags:         r.Tags,
		Reasoning:    r.Reasoning,
		SessionID:    r.SessionID,
		SourceType:   r.SourceType,
		WasRetrieved: r.WasRetrieved,
		CreatedAt:    r.CreatedAt,
	}
}

func insightToRecord(ins *Insight, op, projectID string) insightRecord {
	return insightRecord{
		ProjectID:    projectID,
		Op:           op,
		ID:           ins.ID,
		EventID:      ins.EventID,
		Category:     ins.Category,
		Summary:      ins.Summary,
		Importance:   ins.Importance,
		Tags:         ins.Tags,
		Reasoning:    ins.Reasoning,
		SessionID:    ins.SessionID,
		SourceType:   ins.SourceType,
		WasRetrieved: ins.WasRetrieved,
		CreatedAt:    ins.CreatedAt,
	}
}

func (s *Storage) addInsightToSecondaryIndexes(insight *Insight) {
	s.insightsByTime = append(s.insightsByTime, insight)
	if insight.Category != "" {
		s.insightsByCat[insight.Category] = append(s.insightsByCat[insight.Category], insight)
	}
	if insight.SessionID != "" {
		s.insightsBySession[insight.SessionID] = append(s.insightsBySession[insight.SessionID], insight)
	}
	if insight.SourceType != "" {
		s.insightsBySource[insight.SourceType] = append(s.insightsBySource[insight.SourceType], insight)
	}
}

func (s *Storage) removeInsightFromIndexes(id int64) {
	insight := s.insights[id]
	if insight == nil {
		return
	}
	s.insightsByTime = removeInsightFromSlice(s.insightsByTime, id)
	if insight.Category != "" {
		s.insightsByCat[insight.Category] = removeInsightFromSlice(s.insightsByCat[insight.Category], id)
	}
	if insight.SessionID != "" {
		s.insightsBySession[insight.SessionID] = removeInsightFromSlice(s.insightsBySession[insight.SessionID], id)
	}
	if insight.SourceType != "" {
		s.insightsBySource[insight.SourceType] = removeInsightFromSlice(s.insightsBySource[insight.SourceType], id)
	}
}

func removeInsightFromSlice(slice []*Insight, id int64) []*Insight {
	for i, ins := range slice {
		if ins.ID == id {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func sortInsightsByImportance(slice []*Insight) {
	sort.Slice(slice, func(i, j int) bool {
		if slice[i].Importance != slice[j].Importance {
			return slice[i].Importance > slice[j].Importance
		}
		return slice[i].CreatedAt.After(slice[j].CreatedAt)
	})
}

// --- Event methods ---

// StoreEvent stores an event (append-only)
func (s *Storage) StoreEvent(event *events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicate
	if _, exists := s.events[event.ID]; exists {
		return fmt.Errorf("failed to store event: UNIQUE constraint failed: events.id")
	}

	rec := eventRecord{
		ProjectID:      s.projectID,
		ID:             event.ID,
		Source:         event.Source,
		EventType:      event.EventType,
		Timestamp:      event.Timestamp,
		ToolName:       event.ToolName,
		ToolInput:      event.ToolInput,
		ToolResult:     event.ToolResult,
		Prompt:         event.Prompt,
		TranscriptPath: event.TranscriptPath,
		Context:        event.Context,
		Metadata:       event.Metadata,
	}

	if err := appendLine(s.eventFile, rec); err != nil {
		return fmt.Errorf("failed to store event: %w", err)
	}

	// Update in-memory indexes
	s.events[event.ID] = event
	// Insert into sorted slice maintaining DESC order
	idx := sort.Search(len(s.eventsByTime), func(i int) bool {
		return s.eventsByTime[i].Timestamp.Before(event.Timestamp)
	})
	s.eventsByTime = append(s.eventsByTime, nil)
	copy(s.eventsByTime[idx+1:], s.eventsByTime[idx:])
	s.eventsByTime[idx] = event

	if event.Context.SessionID != "" {
		s.sessionEvents[event.Context.SessionID] = append(s.sessionEvents[event.Context.SessionID], event.ID)
	}

	return nil
}

// GetEvent retrieves an event by ID
func (s *Storage) GetEvent(id string) (*events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ev, ok := s.events[id]
	if !ok {
		return nil, fmt.Errorf("event not found: %s", id)
	}
	return ev, nil
}

// GetRecentEvents retrieves recent events
func (s *Storage) GetRecentEvents(limit int) ([]*events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit > len(s.eventsByTime) {
		limit = len(s.eventsByTime)
	}
	result := make([]*events.Event, limit)
	copy(result, s.eventsByTime[:limit])
	return result, nil
}

// SearchEvents performs text search on events (fallback when vectors unavailable).
func (s *Storage) SearchEvents(query string, limit int) ([]*events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	queryLower := strings.ToLower(query)
	var results []*events.Event

	for _, ev := range s.eventsByTime {
		if len(results) >= limit {
			break
		}
		if matchesEvent(ev, queryLower) {
			results = append(results, ev)
		}
	}

	return results, nil
}

// SearchEventsMultiTerm searches events matching ANY of the provided terms (OR logic).
func (s *Storage) SearchEventsMultiTerm(terms []string, limit int) ([]*events.Event, error) {
	if len(terms) == 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	lowerTerms := make([]string, len(terms))
	for i, t := range terms {
		lowerTerms[i] = strings.ToLower(t)
	}

	var results []*events.Event
	for _, ev := range s.eventsByTime {
		if len(results) >= limit {
			break
		}
		for _, term := range lowerTerms {
			if matchesEvent(ev, term) {
				results = append(results, ev)
				break
			}
		}
	}

	return results, nil
}

func matchesEvent(ev *events.Event, queryLower string) bool {
	if strings.Contains(strings.ToLower(ev.ToolResult), queryLower) {
		return true
	}
	if strings.Contains(strings.ToLower(ev.ToolName), queryLower) {
		return true
	}
	// Check tool_input as JSON string (matches SQLite LIKE on the JSON text)
	if ev.ToolInput != nil {
		inputJSON, _ := json.Marshal(ev.ToolInput)
		if strings.Contains(strings.ToLower(string(inputJSON)), queryLower) {
			return true
		}
	}
	return false
}

// CountEventsBySession counts events for a specific session
func (s *Storage) CountEventsBySession(sessionID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.sessionEvents[sessionID]), nil
}

// --- Entity types and methods ---

// Entity represents a knowledge graph entity
type Entity struct {
	ID        int64
	Type      string
	Name      string
	FirstSeen time.Time
	LastSeen  time.Time
}

// Relationship represents a connection between entities
type Relationship struct {
	ID           int64
	FromEntity   *Entity
	ToEntity     *Entity
	RelationType string
	EventID      string
	CreatedAt    time.Time
}

// StoreEntity stores or updates an entity
func (s *Storage) StoreEntity(entityType, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	key := entityType + ":" + name

	if existingID, ok := s.entityByKey[key]; ok {
		// Upsert: update last_seen
		existing := s.entities[existingID]
		existing.LastSeen = now

		rec := entityRecord{
			ProjectID: s.projectID,
			ID:        existingID,
			Type:      entityType,
			Name:      name,
			FirstSeen: existing.FirstSeen,
			LastSeen:  now,
		}
		if err := appendLine(s.entityFile, rec); err != nil {
			return 0, fmt.Errorf("failed to store entity: %w", err)
		}
		return existingID, nil
	}

	// New entity
	id := s.nextEntityID
	s.nextEntityID++

	entity := &Entity{
		ID:        id,
		Type:      entityType,
		Name:      name,
		FirstSeen: now,
		LastSeen:  now,
	}

	rec := entityRecord{
		ProjectID: s.projectID,
		ID:        id,
		Type:      entityType,
		Name:      name,
		FirstSeen: now,
		LastSeen:  now,
	}
	if err := appendLine(s.entityFile, rec); err != nil {
		return 0, fmt.Errorf("failed to store entity: %w", err)
	}

	s.entities[id] = entity
	s.entityByKey[key] = id
	return id, nil
}

// StoreRelationship stores a relationship between entities
func (s *Storage) StoreRelationship(fromID, toID int64, relationType, eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextRelID
	s.nextRelID++
	now := time.Now()

	rec := relationshipRecord{
		ProjectID:    s.projectID,
		ID:           id,
		FromEntityID: fromID,
		ToEntityID:   toID,
		RelationType: relationType,
		EventID:      eventID,
		CreatedAt:    now,
	}
	if err := appendLine(s.relFile, rec); err != nil {
		return fmt.Errorf("failed to store relationship: %w", err)
	}

	rel := &Relationship{
		ID:           id,
		FromEntity:   s.entities[fromID],
		ToEntity:     s.entities[toID],
		RelationType: relationType,
		EventID:      eventID,
		CreatedAt:    now,
	}
	s.relationships[id] = rel
	s.relsByEntity[fromID] = append(s.relsByEntity[fromID], id)
	s.relsByEntity[toID] = append(s.relsByEntity[toID], id)

	return nil
}

// GetEntity retrieves an entity by type and name
func (s *Storage) GetEntity(entityType, name string) (*Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := entityType + ":" + name
	id, ok := s.entityByKey[key]
	if !ok {
		return nil, fmt.Errorf("entity not found: %s/%s", entityType, name)
	}
	return s.entities[id], nil
}

// GetEntityByID retrieves an entity by ID
func (s *Storage) GetEntityByID(id int64) (*Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.entities[id]
	if !ok {
		return nil, fmt.Errorf("entity not found: %d", id)
	}
	return e, nil
}

// GetEntitiesByType retrieves all entities of a specific type
func (s *Storage) GetEntitiesByType(entityType string) ([]*Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Entity
	for _, e := range s.entities {
		if e.Type == entityType {
			result = append(result, e)
		}
	}
	// Sort by last_seen DESC
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastSeen.After(result[j].LastSeen)
	})
	return result, nil
}

// GetRelatedEntities finds entities related to the given entity
func (s *Storage) GetRelatedEntities(entityID int64, relationType string) ([]*Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[int64]bool)
	var result []*Entity

	for _, relID := range s.relsByEntity[entityID] {
		rel := s.relationships[relID]
		if rel == nil {
			continue
		}
		if relationType != "" && rel.RelationType != relationType {
			continue
		}

		// Find the other entity
		var otherID int64
		if rel.FromEntity != nil && rel.FromEntity.ID == entityID {
			if rel.ToEntity != nil {
				otherID = rel.ToEntity.ID
			}
		} else if rel.ToEntity != nil && rel.ToEntity.ID == entityID {
			if rel.FromEntity != nil {
				otherID = rel.FromEntity.ID
			}
		}

		if otherID != 0 && !seen[otherID] {
			if e := s.entities[otherID]; e != nil {
				result = append(result, e)
				seen[otherID] = true
			}
		}
	}

	return result, nil
}

// GetRelationships retrieves relationships for an entity
func (s *Storage) GetRelationships(entityID int64) ([]*Relationship, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Relationship
	for _, relID := range s.relsByEntity[entityID] {
		rel := s.relationships[relID]
		if rel != nil {
			result = append(result, rel)
		}
	}
	// Sort by created_at DESC
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

// --- Insight types and methods ---

// Insight represents an LLM-extracted insight
type Insight struct {
	ID           int64
	EventID      string
	Category     string
	Summary      string
	Importance   int
	Tags         []string
	Reasoning    string
	SessionID    string // Links to session for session-over-session analysis
	SourceType   string // project, cortex, claude_history, git
	WasRetrieved bool   // True if ever returned in a search
	CreatedAt    time.Time
}

// StoreInsight stores an insight from LLM analysis
func (s *Storage) StoreInsight(eventID, category, summary string, importance int, tags []string, reasoning string) error {
	return s.StoreInsightFull(eventID, category, summary, importance, tags, reasoning, "", "", time.Time{})
}

// StoreInsightWithTimestamp stores an insight with a specific timestamp
func (s *Storage) StoreInsightWithTimestamp(eventID, category, summary string, importance int, tags []string, reasoning string, timestamp time.Time) error {
	return s.StoreInsightFull(eventID, category, summary, importance, tags, reasoning, "", "", timestamp)
}

// StoreInsightWithSession stores an insight with session and source tracking.
func (s *Storage) StoreInsightWithSession(eventID, category, summary string, importance int, tags []string, reasoning, sessionID, sourceType string) error {
	return s.StoreInsightFull(eventID, category, summary, importance, tags, reasoning, sessionID, sourceType, time.Time{})
}

// StoreInsightFull stores an insight with all optional fields.
func (s *Storage) StoreInsightFull(eventID, category, summary string, importance int, tags []string, reasoning, sessionID, sourceType string, timestamp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	id := s.nextInsightID
	s.nextInsightID++

	insight := &Insight{
		ID:         id,
		EventID:    eventID,
		Category:   category,
		Summary:    summary,
		Importance: importance,
		Tags:       tags,
		Reasoning:  reasoning,
		SessionID:  sessionID,
		SourceType: sourceType,
		CreatedAt:  timestamp,
	}

	rec := insightToRecord(insight, "", s.projectID)
	if err := appendLine(s.insightFile, rec); err != nil {
		return fmt.Errorf("failed to store insight: %w", err)
	}

	s.insights[id] = insight

	// Insert into sorted slices maintaining order
	s.insightsByTime = insertInsightSorted(s.insightsByTime, insight, func(a, b *Insight) bool {
		return a.CreatedAt.After(b.CreatedAt)
	})
	if category != "" {
		s.insightsByCat[category] = insertInsightSorted(s.insightsByCat[category], insight, func(a, b *Insight) bool {
			if a.Importance != b.Importance {
				return a.Importance > b.Importance
			}
			return a.CreatedAt.After(b.CreatedAt)
		})
	}
	if sessionID != "" {
		s.insightsBySession[sessionID] = append(s.insightsBySession[sessionID], insight)
	}
	if sourceType != "" {
		s.insightsBySource[sourceType] = insertInsightSorted(s.insightsBySource[sourceType], insight, func(a, b *Insight) bool {
			if a.Importance != b.Importance {
				return a.Importance > b.Importance
			}
			return a.CreatedAt.After(b.CreatedAt)
		})
	}

	return nil
}

func insertInsightSorted(slice []*Insight, ins *Insight, less func(a, b *Insight) bool) []*Insight {
	idx := sort.Search(len(slice), func(i int) bool {
		return !less(slice[i], ins)
	})
	slice = append(slice, nil)
	copy(slice[idx+1:], slice[idx:])
	slice[idx] = ins
	return slice
}

// GetInsightsByCategory retrieves insights by category
func (s *Storage) GetInsightsByCategory(category string, limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	catInsights := s.insightsByCat[category]
	if limit > len(catInsights) {
		limit = len(catInsights)
	}
	result := make([]*Insight, limit)
	copy(result, catInsights[:limit])
	return result, nil
}

// GetRecentInsights retrieves recent insights
func (s *Storage) GetRecentInsights(limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit > len(s.insightsByTime) {
		limit = len(s.insightsByTime)
	}
	result := make([]*Insight, limit)
	copy(result, s.insightsByTime[:limit])
	return result, nil
}

// GetImportantInsights retrieves high-importance insights
func (s *Storage) GetImportantInsights(minImportance, limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Insight
	// Walk insightsByTime but we need importance-sorted output.
	// Collect all matching, then sort.
	for _, ins := range s.insights {
		if ins.Importance >= minImportance {
			result = append(result, ins)
		}
	}
	sortInsightsByImportance(result)
	if limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

// GetInsightByID retrieves a single insight by ID
func (s *Storage) GetInsightByID(id int64) (*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ins, ok := s.insights[id]
	if !ok {
		return nil, fmt.Errorf("insight not found: %d", id)
	}
	return ins, nil
}

// GetInsightsBySession retrieves insights linked to a specific session.
func (s *Storage) GetInsightsBySession(sessionID string, limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessInsights := s.insightsBySession[sessionID]
	if limit > len(sessInsights) {
		limit = len(sessInsights)
	}
	if limit == 0 {
		return nil, nil
	}
	result := make([]*Insight, limit)
	copy(result, sessInsights[:limit])
	return result, nil
}

// GetInsightsBySourceType retrieves insights from a specific source type.
func (s *Storage) GetInsightsBySourceType(sourceType string, limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	srcInsights := s.insightsBySource[sourceType]
	if limit > len(srcInsights) {
		limit = len(srcInsights)
	}
	if limit == 0 {
		return nil, nil
	}
	result := make([]*Insight, limit)
	copy(result, srcInsights[:limit])
	return result, nil
}

// GetUnretrievedInsights returns insights that have never been returned in a search.
func (s *Storage) GetUnretrievedInsights(limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Insight
	for _, ins := range s.insights {
		if !ins.WasRetrieved {
			result = append(result, ins)
		}
	}
	sortInsightsByImportance(result)
	if limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

// SearchInsights searches insights by keyword
func (s *Storage) SearchInsights(keyword string, limit int) ([]*Insight, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keywordLower := strings.ToLower(keyword)
	var result []*Insight

	for _, ins := range s.insights {
		if strings.Contains(strings.ToLower(ins.Summary), keywordLower) ||
			strings.Contains(strings.ToLower(ins.Category), keywordLower) ||
			tagsContain(ins.Tags, keywordLower) {
			result = append(result, ins)
		}
	}

	sortInsightsByImportance(result)
	if limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

func tagsContain(tags []string, keyword string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), keyword) {
			return true
		}
	}
	return false
}

// SearchKnowledgeFiles searches .cortex/knowledge/ markdown files by keyword.
func (s *Storage) SearchKnowledgeFiles(keyword string, limit int) ([]*Insight, error) {
	if s.cfg == nil || s.cfg.ContextDir == "" {
		return nil, nil
	}

	knowledgeDir := filepath.Join(s.cfg.ContextDir, "knowledge")
	if _, err := os.Stat(knowledgeDir); err != nil {
		return nil, nil
	}

	keyword = strings.ToLower(keyword)
	var results []*Insight

	err := filepath.Walk(knowledgeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if len(results) >= limit {
			return filepath.SkipAll
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if !strings.Contains(strings.ToLower(string(content)), keyword) {
			return nil
		}

		category := filepath.Base(filepath.Dir(path))
		importance := 5
		summary := string(content)

		if parts := strings.SplitN(summary, "---", 3); len(parts) == 3 {
			summary = strings.TrimSpace(parts[2])
		}

		results = append(results, &Insight{
			Category:   category,
			Summary:    summary,
			Importance: importance,
			SourceType: "knowledge_file",
			CreatedAt:  info.ModTime(),
		})
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to search knowledge files: %w", err)
	}

	return results, nil
}

// ForgetInsight deletes an insight by ID
func (s *Storage) ForgetInsight(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.insights[id]; !ok {
		return fmt.Errorf("insight not found")
	}

	rec := insightRecord{ProjectID: s.projectID, Op: "delete", ID: id}
	if err := appendLine(s.insightFile, rec); err != nil {
		return fmt.Errorf("failed to delete insight: %w", err)
	}

	s.removeInsightFromIndexes(id)
	delete(s.insights, id)
	return nil
}

// ForgetInsightsByKeyword deletes insights matching a keyword in summary
func (s *Storage) ForgetInsightsByKeyword(keyword string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keywordLower := strings.ToLower(keyword)
	var toDelete []int64

	for _, ins := range s.insights {
		if strings.Contains(strings.ToLower(ins.Summary), keywordLower) {
			toDelete = append(toDelete, ins.ID)
		}
	}

	for _, id := range toDelete {
		rec := insightRecord{ProjectID: s.projectID, Op: "delete", ID: id}
		if err := appendLine(s.insightFile, rec); err != nil {
			return 0, fmt.Errorf("failed to delete insights: %w", err)
		}
		s.removeInsightFromIndexes(id)
		delete(s.insights, id)
	}

	return len(toDelete), nil
}

// MergeInsights keeps one insight and deletes duplicates, merging their tags.
func (s *Storage) MergeInsights(keepID int64, deleteIDs []int64) (int, error) {
	if len(deleteIDs) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	keeper := s.insights[keepID]
	if keeper == nil {
		return 0, fmt.Errorf("failed to get representative tags: insight not found")
	}

	// Collect all tags
	tagSet := make(map[string]bool)
	for _, t := range keeper.Tags {
		tagSet[t] = true
	}

	deleted := 0
	for _, dupID := range deleteIDs {
		dup := s.insights[dupID]
		if dup == nil {
			continue
		}
		for _, t := range dup.Tags {
			tagSet[t] = true
		}
		// Append delete op
		rec := insightRecord{ProjectID: s.projectID, Op: "delete", ID: dupID}
		if err := appendLine(s.insightFile, rec); err != nil {
			return deleted, fmt.Errorf("failed to delete duplicate: %w", err)
		}
		s.removeInsightFromIndexes(dupID)
		delete(s.insights, dupID)
		deleted++
	}

	// Update keeper tags
	var uniqueTags []string
	for t := range tagSet {
		uniqueTags = append(uniqueTags, t)
	}
	keeper.Tags = uniqueTags

	// Append update op
	rec := insightToRecord(keeper, "update", s.projectID)
	if err := appendLine(s.insightFile, rec); err != nil {
		return deleted, fmt.Errorf("failed to update representative tags: %w", err)
	}

	return deleted, nil
}

// MarkInsightRetrieved marks an insight as having been returned in a search.
func (s *Storage) MarkInsightRetrieved(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ins, ok := s.insights[id]
	if !ok {
		return fmt.Errorf("failed to mark insight as retrieved: not found")
	}

	ins.WasRetrieved = true

	rec := insightToRecord(ins, "update", s.projectID)
	if err := appendLine(s.insightFile, rec); err != nil {
		return fmt.Errorf("failed to mark insight as retrieved: %w", err)
	}

	return nil
}

// MarkInsightsRetrieved marks multiple insights as retrieved in a single operation.
func (s *Storage) MarkInsightsRetrieved(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		ins, ok := s.insights[id]
		if !ok {
			return fmt.Errorf("failed to mark insight %d as retrieved: not found", id)
		}
		ins.WasRetrieved = true

		rec := insightToRecord(ins, "update", s.projectID)
		if err := appendLine(s.insightFile, rec); err != nil {
			return fmt.Errorf("failed to mark insight %d as retrieved: %w", id, err)
		}
	}

	return nil
}

// --- Embedding types and methods ---

// VectorSearchResult represents a result from vector similarity search
type VectorSearchResult struct {
	ContentID   string
	ContentType string
	Content     string
	Similarity  float64
}

// EmbeddingContent represents a content ID and type from the embeddings table.
type EmbeddingContent struct {
	ContentID   string
	ContentType string
}

// StoreEmbedding stores a vector embedding for content
func (s *Storage) StoreEmbedding(contentID, contentType string, vector []float32) error {
	return s.StoreEmbeddingWithModel(contentID, contentType, vector, "")
}

// StoreEmbeddingWithModel stores a vector embedding with model name tracking.
func (s *Storage) StoreEmbeddingWithModel(contentID, contentType string, vector []float32, modelName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := &embeddingEntry{
		ProjectID:   s.projectID,
		ContentID:   contentID,
		ContentType: contentType,
		Vector:      vector,
		ModelName:   modelName,
		CreatedAt:   time.Now(),
	}

	if err := appendLine(s.embFile, entry); err != nil {
		return fmt.Errorf("failed to store embedding: %w", err)
	}

	key := contentID + ":" + contentType
	s.embeddings[key] = entry
	return nil
}

// GetAllEmbeddingContentIDs returns all content IDs and types from the embeddings.
func (s *Storage) GetAllEmbeddingContentIDs() ([]EmbeddingContent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []EmbeddingContent
	for _, entry := range s.embeddings {
		results = append(results, EmbeddingContent{
			ContentID:   entry.ContentID,
			ContentType: entry.ContentType,
		})
	}
	return results, nil
}

// GetEmbeddingCount returns the total number of embeddings.
func (s *Storage) GetEmbeddingCount() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.embeddings), nil
}

// SearchByVector finds content similar to the query vector
func (s *Storage) SearchByVector(queryVector []float32, limit int, threshold float64) ([]VectorSearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []VectorSearchResult
	for _, entry := range s.embeddings {
		similarity := cosineSimilarity(queryVector, entry.Vector)
		if similarity >= threshold {
			// Try to get content from events
			var content string
			if entry.ContentType == "event" {
				if ev, ok := s.events[entry.ContentID]; ok {
					content = ev.ToolResult
				}
			}
			results = append(results, VectorSearchResult{
				ContentID:   entry.ContentID,
				ContentType: entry.ContentType,
				Content:     content,
				Similarity:  similarity,
			})
		}
	}

	sortBySimilarity(results)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// cosineSimilarity computes cosine similarity between two vectors using SIMD
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	a64 := make([]float64, len(a))
	b64 := make([]float64, len(b))
	for i := range a {
		a64[i] = float64(a[i])
		b64[i] = float64(b[i])
	}
	return vek.CosineSimilarity(a64, b64)
}

// sortBySimilarity sorts results by similarity descending
func sortBySimilarity(results []VectorSearchResult) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})
}

// --- Session types and methods ---

// SessionMetadata represents session tracking info for the watch view
type SessionMetadata struct {
	ID            int64
	SessionID     string
	StartedAt     time.Time
	InitialPrompt string
	EventCount    int
	LastAction    string
	LastActionAt  time.Time
	ProjectPath   string
}

// CreateOrUpdateSession creates a new session or updates an existing one.
func (s *Storage) CreateOrUpdateSession(sessionID, initialPrompt, lastAction, projectPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if existing, ok := s.sessions[sessionID]; ok {
		// Update existing
		existing.EventCount++
		existing.LastAction = lastAction
		existing.LastActionAt = now

		rec := sessionRecord{
			ProjectID:     s.projectID,
			Op:            "update",
			ID:            existing.ID,
			SessionID:     sessionID,
			StartedAt:     existing.StartedAt,
			InitialPrompt: existing.InitialPrompt,
			EventCount:    existing.EventCount,
			LastAction:    lastAction,
			LastActionAt:  now,
			ProjectPath:   existing.ProjectPath,
		}
		return appendLine(s.sessionFile, rec)
	}

	// New session
	id := s.nextSessionID
	s.nextSessionID++

	sess := &SessionMetadata{
		ID:            id,
		SessionID:     sessionID,
		StartedAt:     now,
		InitialPrompt: initialPrompt,
		EventCount:    1,
		LastAction:    lastAction,
		LastActionAt:  now,
		ProjectPath:   projectPath,
	}

	rec := sessionRecord{
		ProjectID:     s.projectID,
		ID:            id,
		SessionID:     sessionID,
		StartedAt:     now,
		InitialPrompt: initialPrompt,
		EventCount:    1,
		LastAction:    lastAction,
		LastActionAt:  now,
		ProjectPath:   projectPath,
	}
	if err := appendLine(s.sessionFile, rec); err != nil {
		return fmt.Errorf("failed to create/update session: %w", err)
	}

	s.sessions[sessionID] = sess
	// Insert into sorted slice
	idx := sort.Search(len(s.sessionsByTime), func(i int) bool {
		return s.sessionsByTime[i].StartedAt.Before(now)
	})
	s.sessionsByTime = append(s.sessionsByTime, nil)
	copy(s.sessionsByTime[idx+1:], s.sessionsByTime[idx:])
	s.sessionsByTime[idx] = sess

	return nil
}

// UpdateSessionLastAction updates only the last action for a session
func (s *Storage) UpdateSessionLastAction(sessionID, lastAction string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("failed to update session last action: session not found")
	}

	existing.EventCount++
	existing.LastAction = lastAction
	existing.LastActionAt = time.Now()

	rec := sessionRecord{
		ProjectID:     s.projectID,
		Op:            "update",
		ID:            existing.ID,
		SessionID:     sessionID,
		StartedAt:     existing.StartedAt,
		InitialPrompt: existing.InitialPrompt,
		EventCount:    existing.EventCount,
		LastAction:    lastAction,
		LastActionAt:  existing.LastActionAt,
		ProjectPath:   existing.ProjectPath,
	}
	return appendLine(s.sessionFile, rec)
}

// GetRecentSessions retrieves the most recent sessions
func (s *Storage) GetRecentSessions(limit int) ([]*SessionMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit > len(s.sessionsByTime) {
		limit = len(s.sessionsByTime)
	}
	result := make([]*SessionMetadata, limit)
	copy(result, s.sessionsByTime[:limit])
	return result, nil
}

// GetSessionByID retrieves a specific session by session_id
func (s *Storage) GetSessionByID(sessionID string) (*SessionMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return sess, nil
}

// GetSessionEvents retrieves events for a specific session (for expanded view)
func (s *Storage) GetSessionEvents(sessionID string, limit int) ([]*events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	eventIDs := s.sessionEvents[sessionID]
	var result []*events.Event

	// Walk in reverse to get most recent first
	for i := len(eventIDs) - 1; i >= 0 && len(result) < limit; i-- {
		if ev, ok := s.events[eventIDs[i]]; ok {
			result = append(result, ev)
		}
	}

	return result, nil
}

// --- Utility methods ---

// GetStats returns storage statistics
func (s *Storage) GetStats() (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[string]interface{})

	stats["total_events"] = len(s.events)

	// Events by source
	bySource := make(map[string]int)
	for _, ev := range s.events {
		bySource[string(ev.Source)]++
	}
	stats["by_source"] = bySource

	stats["total_entities"] = len(s.entities)
	stats["total_insights"] = len(s.insights)
	stats["total_embeddings"] = len(s.embeddings)

	// Date range
	var oldest, newest time.Time
	for _, ev := range s.eventsByTime {
		if oldest.IsZero() || ev.Timestamp.Before(oldest) {
			oldest = ev.Timestamp
		}
		if newest.IsZero() || ev.Timestamp.After(newest) {
			newest = ev.Timestamp
		}
	}
	stats["oldest_event"] = oldest
	stats["newest_event"] = newest

	// Data size (sum of JSONL file sizes)
	var totalSize int64
	files := []string{"events.jsonl", "insights.jsonl", "entities.jsonl", "relationships.jsonl", "sessions.jsonl", "embeddings.jsonl"}
	for _, name := range files {
		if info, err := os.Stat(filepath.Join(s.dataDir, name)); err == nil {
			totalSize += info.Size()
		}
	}
	stats["db_size_bytes"] = totalSize

	return stats, nil
}

// Compact rewrites JSONL files removing deleted/superseded records.
func (s *Storage) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Compact insights (most likely to have deletes/updates)
	var liveInsights []insightRecord
	for _, ins := range s.insights {
		liveInsights = append(liveInsights, insightToRecord(ins, "", s.projectID))
	}
	insightsPath := filepath.Join(s.dataDir, "insights.jsonl")
	if err := atomicRewrite(insightsPath, liveInsights); err != nil {
		return fmt.Errorf("failed to compact insights: %w", err)
	}
	// Reopen file handle
	s.insightFile.Close()
	var err error
	s.insightFile, err = openAppend(insightsPath)
	if err != nil {
		return fmt.Errorf("failed to reopen insights file: %w", err)
	}

	// Compact entities
	var liveEntities []entityRecord
	for _, e := range s.entities {
		liveEntities = append(liveEntities, entityRecord{
			ProjectID: s.projectID,
			ID:        e.ID, Type: e.Type, Name: e.Name,
			FirstSeen: e.FirstSeen, LastSeen: e.LastSeen,
		})
	}
	entitiesPath := filepath.Join(s.dataDir, "entities.jsonl")
	if err := atomicRewrite(entitiesPath, liveEntities); err != nil {
		return fmt.Errorf("failed to compact entities: %w", err)
	}
	s.entityFile.Close()
	s.entityFile, err = openAppend(entitiesPath)
	if err != nil {
		return fmt.Errorf("failed to reopen entities file: %w", err)
	}

	// Compact sessions
	var liveSessions []sessionRecord
	for _, sess := range s.sessions {
		liveSessions = append(liveSessions, sessionRecord{
			ProjectID: s.projectID,
			ID:        sess.ID, SessionID: sess.SessionID, StartedAt: sess.StartedAt,
			InitialPrompt: sess.InitialPrompt, EventCount: sess.EventCount,
			LastAction: sess.LastAction, LastActionAt: sess.LastActionAt,
			ProjectPath: sess.ProjectPath,
		})
	}
	sessionsPath := filepath.Join(s.dataDir, "sessions.jsonl")
	if err := atomicRewrite(sessionsPath, liveSessions); err != nil {
		return fmt.Errorf("failed to compact sessions: %w", err)
	}
	s.sessionFile.Close()
	s.sessionFile, err = openAppend(sessionsPath)
	if err != nil {
		return fmt.Errorf("failed to reopen sessions file: %w", err)
	}

	// Compact embeddings (deduped by key)
	var liveEmbeddings []embeddingEntry
	for _, entry := range s.embeddings {
		liveEmbeddings = append(liveEmbeddings, *entry)
	}
	embPath := filepath.Join(s.dataDir, "embeddings.jsonl")
	if err := atomicRewrite(embPath, liveEmbeddings); err != nil {
		return fmt.Errorf("failed to compact embeddings: %w", err)
	}
	s.embFile.Close()
	s.embFile, err = openAppend(embPath)
	if err != nil {
		return fmt.Errorf("failed to reopen embeddings file: %w", err)
	}

	return nil
}
