// Package storage provides event sourcing storage with SQLite
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"math"
	"time"

	"github.com/viterin/vek"
	_ "modernc.org/sqlite"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

// Event is a simplified storage event (for processor use)
type Event struct {
	ID         string
	ToolName   string
	ToolResult string
}

// Storage handles event storage with event sourcing pattern
type Storage struct {
	db  *sql.DB
	cfg *config.Config
}

// New creates a new Storage instance
func New(cfg *config.Config) (*Storage, error) {
	dbPath := filepath.Join(cfg.ContextDir, "db", "events.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	storage := &Storage{
		db:  db,
		cfg: cfg,
	}

	// Initialize schema
	if err := storage.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return storage, nil
}

// Close closes the database connection
func (s *Storage) Close() error {
	return s.db.Close()
}

// initSchema creates the database schema
func (s *Storage) initSchema() error {
	schema := `
	-- Event store (immutable, append-only)
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		event_type TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		tool_name TEXT,
		tool_input TEXT,
		tool_result TEXT,
		context TEXT,
		metadata TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	-- Entities extracted from events
	CREATE TABLE IF NOT EXISTS entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL, -- file, concept, decision, pattern
		name TEXT NOT NULL,
		first_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(type, name)
	);

	-- Relationships between entities
	CREATE TABLE IF NOT EXISTS relationships (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_entity_id INTEGER NOT NULL,
		to_entity_id INTEGER NOT NULL,
		relation_type TEXT NOT NULL, -- affects, implements, relates_to, etc.
		event_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(from_entity_id) REFERENCES entities(id),
		FOREIGN KEY(to_entity_id) REFERENCES entities(id),
		FOREIGN KEY(event_id) REFERENCES events(id)
	);

	-- Insights derived from events
	CREATE TABLE IF NOT EXISTS insights (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL,
		category TEXT NOT NULL, -- decision, pattern, insight, strategy
		summary TEXT NOT NULL,
		importance INTEGER DEFAULT 0,
		tags TEXT,
		reasoning TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(event_id) REFERENCES events(id)
	);

	-- Vector embeddings for semantic search
	CREATE TABLE IF NOT EXISTS embeddings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		content_id TEXT NOT NULL,
		content_type TEXT NOT NULL, -- event, insight
		vector BLOB NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(content_id, content_type)
	);

	-- Full-text search index
	CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
		event_id,
		tool_name,
		tool_result,
		content='events',
		content_rowid='rowid'
	);

	-- Session metadata for watch tracking
	CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT UNIQUE NOT NULL,
		started_at DATETIME NOT NULL,
		initial_prompt TEXT,
		event_count INTEGER DEFAULT 0,
		last_action TEXT,
		last_action_at DATETIME,
		project_path TEXT
	);

	-- Indexes for performance
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
	CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);
	CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type);
	CREATE INDEX IF NOT EXISTS idx_relationships_from ON relationships(from_entity_id);
	CREATE INDEX IF NOT EXISTS idx_relationships_to ON relationships(to_entity_id);
	CREATE INDEX IF NOT EXISTS idx_insights_category ON insights(category);
	CREATE INDEX IF NOT EXISTS idx_insights_event ON insights(event_id);
	CREATE INDEX IF NOT EXISTS idx_embeddings_content ON embeddings(content_id, content_type);
	CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC);
	`

	_, err := s.db.Exec(schema)
	return err
}

// StoreEvent stores an event (append-only)
func (s *Storage) StoreEvent(event *events.Event) error {
	toolInputJSON, _ := json.Marshal(event.ToolInput)
	contextJSON, _ := json.Marshal(event.Context)
	metadataJSON, _ := json.Marshal(event.Metadata)

	_, err := s.db.Exec(`
		INSERT INTO events (id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		event.Source,
		event.EventType,
		event.Timestamp,
		event.ToolName,
		string(toolInputJSON),
		event.ToolResult,
		string(contextJSON),
		string(metadataJSON),
	)

	if err != nil {
		return fmt.Errorf("failed to store event: %w", err)
	}

	// Update FTS index
	_, _ = s.db.Exec(`
		INSERT INTO events_fts (event_id, tool_name, tool_result)
		VALUES (?, ?, ?)
	`, event.ID, event.ToolName, event.ToolResult)

	return nil
}

// GetEvent retrieves an event by ID
func (s *Storage) GetEvent(id string) (*events.Event, error) {
	var event events.Event
	var toolInputJSON, contextJSON, metadataJSON string

	err := s.db.QueryRow(`
		SELECT id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata
		FROM events WHERE id = ?
	`, id).Scan(
		&event.ID,
		&event.Source,
		&event.EventType,
		&event.Timestamp,
		&event.ToolName,
		&toolInputJSON,
		&event.ToolResult,
		&contextJSON,
		&metadataJSON,
	)

	if err != nil {
		return nil, err
	}

	json.Unmarshal([]byte(toolInputJSON), &event.ToolInput)
	json.Unmarshal([]byte(contextJSON), &event.Context)
	json.Unmarshal([]byte(metadataJSON), &event.Metadata)

	return &event, nil
}

// GetRecentEvents retrieves recent events
func (s *Storage) GetRecentEvents(limit int) ([]*events.Event, error) {
	rows, err := s.db.Query(`
		SELECT id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata
		FROM events
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var eventList []*events.Event
	for rows.Next() {
		var event events.Event
		var toolInputJSON, contextJSON, metadataJSON string

		err := rows.Scan(
			&event.ID,
			&event.Source,
			&event.EventType,
			&event.Timestamp,
			&event.ToolName,
			&toolInputJSON,
			&event.ToolResult,
			&contextJSON,
			&metadataJSON,
		)

		if err != nil {
			continue
		}

		json.Unmarshal([]byte(toolInputJSON), &event.ToolInput)
		json.Unmarshal([]byte(contextJSON), &event.Context)
		json.Unmarshal([]byte(metadataJSON), &event.Metadata)

		eventList = append(eventList, &event)
	}

	return eventList, nil
}

// SearchEvents performs text search on events (fallback when vectors unavailable).
func (s *Storage) SearchEvents(query string, limit int) ([]*events.Event, error) {
	searchPattern := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata
		FROM events
		WHERE tool_result LIKE ? OR tool_name LIKE ? OR tool_input LIKE ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, searchPattern, searchPattern, searchPattern, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var eventList []*events.Event
	for rows.Next() {
		var event events.Event
		var toolInputJSON, contextJSON, metadataJSON string

		err := rows.Scan(
			&event.ID,
			&event.Source,
			&event.EventType,
			&event.Timestamp,
			&event.ToolName,
			&toolInputJSON,
			&event.ToolResult,
			&contextJSON,
			&metadataJSON,
		)

		if err != nil {
			continue
		}

		json.Unmarshal([]byte(toolInputJSON), &event.ToolInput)
		json.Unmarshal([]byte(contextJSON), &event.Context)
		json.Unmarshal([]byte(metadataJSON), &event.Metadata)

		eventList = append(eventList, &event)
	}

	return eventList, nil
}

// SearchEventsMultiTerm searches events matching ANY of the provided terms (OR logic).
// This enables natural language queries like "how to implement caching" to match
// content containing any of the extracted keywords.
func (s *Storage) SearchEventsMultiTerm(terms []string, limit int) ([]*events.Event, error) {
	if len(terms) == 0 {
		return nil, nil
	}

	// Build dynamic WHERE clause with OR for each term
	var conditions []string
	var args []any
	for _, term := range terms {
		pattern := "%" + term + "%"
		conditions = append(conditions, "(tool_result LIKE ? OR tool_name LIKE ? OR tool_input LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, limit)

	query := `
		SELECT id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata
		FROM events
		WHERE ` + joinConditions(conditions, " OR ") + `
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var eventList []*events.Event
	for rows.Next() {
		var event events.Event
		var toolInputJSON, contextJSON, metadataJSON string

		err := rows.Scan(
			&event.ID,
			&event.Source,
			&event.EventType,
			&event.Timestamp,
			&event.ToolName,
			&toolInputJSON,
			&event.ToolResult,
			&contextJSON,
			&metadataJSON,
		)

		if err != nil {
			continue
		}

		json.Unmarshal([]byte(toolInputJSON), &event.ToolInput)
		json.Unmarshal([]byte(contextJSON), &event.Context)
		json.Unmarshal([]byte(metadataJSON), &event.Metadata)

		eventList = append(eventList, &event)
	}

	return eventList, nil
}

// joinConditions joins SQL conditions with a separator.
func joinConditions(conditions []string, sep string) string {
	if len(conditions) == 0 {
		return ""
	}
	result := conditions[0]
	for i := 1; i < len(conditions); i++ {
		result += sep + conditions[i]
	}
	return result
}

// GetStats returns storage statistics
func (s *Storage) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Total events
	var totalEvents int
	s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&totalEvents)
	stats["total_events"] = totalEvents

	// Events by source
	rows, err := s.db.Query("SELECT source, COUNT(*) FROM events GROUP BY source")
	if err == nil {
		defer rows.Close()
		bySource := make(map[string]int)
		for rows.Next() {
			var source string
			var count int
			rows.Scan(&source, &count)
			bySource[source] = count
		}
		stats["by_source"] = bySource
	}

	// Total entities
	var totalEntities int
	s.db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&totalEntities)
	stats["total_entities"] = totalEntities

	// Total insights
	var totalInsights int
	s.db.QueryRow("SELECT COUNT(*) FROM insights").Scan(&totalInsights)
	stats["total_insights"] = totalInsights

	// Total embeddings
	var totalEmbeddings int
	s.db.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&totalEmbeddings)
	stats["total_embeddings"] = totalEmbeddings

	// Date range
	var oldest, newest time.Time
	s.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM events").Scan(&oldest, &newest)
	stats["oldest_event"] = oldest
	stats["newest_event"] = newest

	// Database size (SQLite page_count * page_size)
	var pageCount, pageSize int64
	s.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
	s.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	stats["db_size_bytes"] = pageCount * pageSize

	return stats, nil
}

// CountEventsBySession counts events for a specific session
func (s *Storage) CountEventsBySession(sessionID string) (int, error) {
	var count int
	// Session ID is stored in the context JSON field
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM events
		WHERE json_extract(context, '$.session_id') = ?
	`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count events by session: %w", err)
	}
	return count, nil
}

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
	// Try insert first
	result, err := s.db.Exec(`
		INSERT INTO entities (type, name, first_seen, last_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(type, name) DO UPDATE SET last_seen = ?
	`, entityType, name, time.Now(), time.Now(), time.Now())

	if err != nil {
		return 0, fmt.Errorf("failed to store entity: %w", err)
	}

	// Get the entity ID
	var id int64
	err = s.db.QueryRow(`
		SELECT id FROM entities WHERE type = ? AND name = ?
	`, entityType, name).Scan(&id)

	if err != nil {
		id, _ = result.LastInsertId()
	}

	return id, nil
}

// StoreRelationship stores a relationship between entities
func (s *Storage) StoreRelationship(fromID, toID int64, relationType, eventID string) error {
	_, err := s.db.Exec(`
		INSERT INTO relationships (from_entity_id, to_entity_id, relation_type, event_id)
		VALUES (?, ?, ?, ?)
	`, fromID, toID, relationType, eventID)

	if err != nil {
		return fmt.Errorf("failed to store relationship: %w", err)
	}

	return nil
}

// GetEntity retrieves an entity by type and name
func (s *Storage) GetEntity(entityType, name string) (*Entity, error) {
	var entity Entity
	err := s.db.QueryRow(`
		SELECT id, type, name, first_seen, last_seen
		FROM entities
		WHERE type = ? AND name = ?
	`, entityType, name).Scan(&entity.ID, &entity.Type, &entity.Name, &entity.FirstSeen, &entity.LastSeen)

	if err != nil {
		return nil, err
	}

	return &entity, nil
}

// GetEntityByID retrieves an entity by ID
func (s *Storage) GetEntityByID(id int64) (*Entity, error) {
	var entity Entity
	err := s.db.QueryRow(`
		SELECT id, type, name, first_seen, last_seen
		FROM entities
		WHERE id = ?
	`, id).Scan(&entity.ID, &entity.Type, &entity.Name, &entity.FirstSeen, &entity.LastSeen)

	if err != nil {
		return nil, err
	}

	return &entity, nil
}

// GetEntitiesByType retrieves all entities of a specific type
func (s *Storage) GetEntitiesByType(entityType string) ([]*Entity, error) {
	rows, err := s.db.Query(`
		SELECT id, type, name, first_seen, last_seen
		FROM entities
		WHERE type = ?
		ORDER BY last_seen DESC
	`, entityType)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []*Entity
	for rows.Next() {
		var entity Entity
		if err := rows.Scan(&entity.ID, &entity.Type, &entity.Name, &entity.FirstSeen, &entity.LastSeen); err != nil {
			continue
		}
		entities = append(entities, &entity)
	}

	return entities, nil
}

// GetRelatedEntities finds entities related to the given entity
func (s *Storage) GetRelatedEntities(entityID int64, relationType string) ([]*Entity, error) {
	query := `
		SELECT e.id, e.type, e.name, e.first_seen, e.last_seen
		FROM entities e
		INNER JOIN relationships r ON (r.to_entity_id = e.id OR r.from_entity_id = e.id)
		WHERE (r.from_entity_id = ? OR r.to_entity_id = ?)
		AND e.id != ?
	`

	args := []interface{}{entityID, entityID, entityID}

	if relationType != "" {
		query += " AND r.relation_type = ?"
		args = append(args, relationType)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []*Entity
	seen := make(map[int64]bool)

	for rows.Next() {
		var entity Entity
		if err := rows.Scan(&entity.ID, &entity.Type, &entity.Name, &entity.FirstSeen, &entity.LastSeen); err != nil {
			continue
		}
		if !seen[entity.ID] {
			entities = append(entities, &entity)
			seen[entity.ID] = true
		}
	}

	return entities, nil
}

// GetRelationships retrieves relationships for an entity
func (s *Storage) GetRelationships(entityID int64) ([]*Relationship, error) {
	rows, err := s.db.Query(`
		SELECT id, from_entity_id, to_entity_id, relation_type, event_id, created_at
		FROM relationships
		WHERE from_entity_id = ? OR to_entity_id = ?
		ORDER BY created_at DESC
	`, entityID, entityID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var relationships []*Relationship
	for rows.Next() {
		var rel Relationship
		var fromID, toID int64
		var eventID sql.NullString

		if err := rows.Scan(&rel.ID, &fromID, &toID, &rel.RelationType, &eventID, &rel.CreatedAt); err != nil {
			continue
		}

		// Load from and to entities
		rel.FromEntity, _ = s.GetEntityByID(fromID)
		rel.ToEntity, _ = s.GetEntityByID(toID)
		if eventID.Valid {
			rel.EventID = eventID.String
		}

		relationships = append(relationships, &rel)
	}

	return relationships, nil
}

// Insight represents an LLM-extracted insight
type Insight struct {
	ID         int64
	EventID    string
	Category   string
	Summary    string
	Importance int
	Tags       []string
	Reasoning  string
	CreatedAt  time.Time
}

// StoreInsight stores an insight from LLM analysis
func (s *Storage) StoreInsight(eventID, category, summary string, importance int, tags []string, reasoning string) error {
	return s.StoreInsightWithTimestamp(eventID, category, summary, importance, tags, reasoning, time.Time{})
}

// StoreInsightWithTimestamp stores an insight with a specific timestamp
func (s *Storage) StoreInsightWithTimestamp(eventID, category, summary string, importance int, tags []string, reasoning string, timestamp time.Time) error {
	tagsJSON, _ := json.Marshal(tags)

	if timestamp.IsZero() {
		// Use default (current time via SQLite)
		_, err := s.db.Exec(`
			INSERT INTO insights (event_id, category, summary, importance, tags, reasoning)
			VALUES (?, ?, ?, ?, ?, ?)
		`, eventID, category, summary, importance, string(tagsJSON), reasoning)
		if err != nil {
			return fmt.Errorf("failed to store insight: %w", err)
		}
	} else {
		// Use provided timestamp
		_, err := s.db.Exec(`
			INSERT INTO insights (event_id, category, summary, importance, tags, reasoning, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, eventID, category, summary, importance, string(tagsJSON), reasoning, timestamp)
		if err != nil {
			return fmt.Errorf("failed to store insight with timestamp: %w", err)
		}
	}

	return nil
}

// GetInsightsByCategory retrieves insights by category
func (s *Storage) GetInsightsByCategory(category string, limit int) ([]*Insight, error) {
	rows, err := s.db.Query(`
		SELECT id, event_id, category, summary, importance, tags, reasoning, created_at
		FROM insights
		WHERE category = ?
		ORDER BY importance DESC, created_at DESC
		LIMIT ?
	`, category, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanInsights(rows)
}

// GetRecentInsights retrieves recent insights
func (s *Storage) GetRecentInsights(limit int) ([]*Insight, error) {
	rows, err := s.db.Query(`
		SELECT id, event_id, category, summary, importance, tags, reasoning, created_at
		FROM insights
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanInsights(rows)
}

// GetImportantInsights retrieves high-importance insights
func (s *Storage) GetImportantInsights(minImportance, limit int) ([]*Insight, error) {
	rows, err := s.db.Query(`
		SELECT id, event_id, category, summary, importance, tags, reasoning, created_at
		FROM insights
		WHERE importance >= ?
		ORDER BY importance DESC, created_at DESC
		LIMIT ?
	`, minImportance, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanInsights(rows)
}

// scanInsights helper function to scan insight rows
func (s *Storage) scanInsights(rows *sql.Rows) ([]*Insight, error) {
	var insights []*Insight
	for rows.Next() {
		var insight Insight
		var tagsJSON string

		if err := rows.Scan(
			&insight.ID,
			&insight.EventID,
			&insight.Category,
			&insight.Summary,
			&insight.Importance,
			&tagsJSON,
			&insight.Reasoning,
			&insight.CreatedAt,
		); err != nil {
			continue
		}

		json.Unmarshal([]byte(tagsJSON), &insight.Tags)
		insights = append(insights, &insight)
	}

	return insights, nil
}

// ForgetInsight deletes an insight by ID
func (s *Storage) ForgetInsight(id int64) error {
	result, err := s.db.Exec("DELETE FROM insights WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete insight: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("insight not found")
	}

	return nil
}

// ForgetInsightsByKeyword deletes insights matching a keyword in summary
func (s *Storage) ForgetInsightsByKeyword(keyword string) (int, error) {
	pattern := "%" + keyword + "%"
	result, err := s.db.Exec("DELETE FROM insights WHERE summary LIKE ?", pattern)
	if err != nil {
		return 0, fmt.Errorf("failed to delete insights: %w", err)
	}

	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// SearchInsights searches insights by keyword
func (s *Storage) SearchInsights(keyword string, limit int) ([]*Insight, error) {
	pattern := "%" + keyword + "%"
	rows, err := s.db.Query(`
		SELECT id, event_id, category, summary, importance, tags, reasoning, created_at
		FROM insights
		WHERE summary LIKE ? OR tags LIKE ? OR category LIKE ?
		ORDER BY importance DESC, created_at DESC
		LIMIT ?
	`, pattern, pattern, pattern, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanInsights(rows)
}

// MergeInsights keeps one insight and deletes duplicates, merging their tags.
// This is the compaction operation called by Digest during idle time.
func (s *Storage) MergeInsights(keepID int64, deleteIDs []int64) (int, error) {
	if len(deleteIDs) == 0 {
		return 0, nil
	}

	// Start transaction for atomicity
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get tags from all duplicates to merge into representative
	var allTags []string

	// First get the representative's current tags
	var keepTagsJSON string
	err = tx.QueryRow("SELECT tags FROM insights WHERE id = ?", keepID).Scan(&keepTagsJSON)
	if err != nil {
		return 0, fmt.Errorf("failed to get representative tags: %w", err)
	}
	json.Unmarshal([]byte(keepTagsJSON), &allTags)

	// Collect tags from duplicates
	for _, dupID := range deleteIDs {
		var tagsJSON string
		err := tx.QueryRow("SELECT tags FROM insights WHERE id = ?", dupID).Scan(&tagsJSON)
		if err != nil {
			continue // Skip if not found
		}
		var dupTags []string
		json.Unmarshal([]byte(tagsJSON), &dupTags)
		allTags = append(allTags, dupTags...)
	}

	// Dedupe tags
	tagSet := make(map[string]bool)
	var uniqueTags []string
	for _, t := range allTags {
		if !tagSet[t] {
			tagSet[t] = true
			uniqueTags = append(uniqueTags, t)
		}
	}

	// Update representative with merged tags
	mergedTagsJSON, _ := json.Marshal(uniqueTags)
	_, err = tx.Exec("UPDATE insights SET tags = ? WHERE id = ?", string(mergedTagsJSON), keepID)
	if err != nil {
		return 0, fmt.Errorf("failed to update representative tags: %w", err)
	}

	// Delete duplicates
	deleted := 0
	for _, dupID := range deleteIDs {
		result, err := tx.Exec("DELETE FROM insights WHERE id = ?", dupID)
		if err != nil {
			continue
		}
		affected, _ := result.RowsAffected()
		deleted += int(affected)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit merge: %w", err)
	}

	return deleted, nil
}

// GetInsightByID retrieves a single insight by ID
func (s *Storage) GetInsightByID(id int64) (*Insight, error) {
	var insight Insight
	var tagsJSON string

	err := s.db.QueryRow(`
		SELECT id, event_id, category, summary, importance, tags, reasoning, created_at
		FROM insights
		WHERE id = ?
	`, id).Scan(
		&insight.ID,
		&insight.EventID,
		&insight.Category,
		&insight.Summary,
		&insight.Importance,
		&tagsJSON,
		&insight.Reasoning,
		&insight.CreatedAt,
	)

	if err != nil {
		return nil, err
	}

	json.Unmarshal([]byte(tagsJSON), &insight.Tags)
	return &insight, nil
}

// VectorSearchResult represents a result from vector similarity search
type VectorSearchResult struct {
	ContentID   string
	ContentType string
	Content     string
	Similarity  float64
}

// StoreEmbedding stores a vector embedding for content
func (s *Storage) StoreEmbedding(contentID, contentType string, vector []float32) error {
	// Serialize vector to bytes
	vectorBytes := vectorToBytes(vector)

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO embeddings (content_id, content_type, vector, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, contentID, contentType, vectorBytes)

	return err
}

// SearchByVector finds content similar to the query vector
func (s *Storage) SearchByVector(queryVector []float32, limit int, threshold float64) ([]VectorSearchResult, error) {
	// Get all embeddings
	rows, err := s.db.Query(`
		SELECT e.content_id, e.content_type, e.vector, ev.tool_result
		FROM embeddings e
		LEFT JOIN events ev ON e.content_id = ev.id AND e.content_type = 'event'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var contentID, contentType string
		var vectorBytes []byte
		var content sql.NullString

		if err := rows.Scan(&contentID, &contentType, &vectorBytes, &content); err != nil {
			continue
		}

		storedVector := bytesToVector(vectorBytes)
		similarity := cosineSimilarity(queryVector, storedVector)

		if similarity >= threshold {
			results = append(results, VectorSearchResult{
				ContentID:   contentID,
				ContentType: contentType,
				Content:     content.String,
				Similarity:  similarity,
			})
		}
	}

	// Sort by similarity descending
	sortBySimilarity(results)

	// Limit results
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// vectorToBytes serializes a float32 vector to bytes
func vectorToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

// bytesToVector deserializes bytes to a float32 vector
func bytesToVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		v[i] = math.Float32frombits(bits)
	}
	return v
}

// cosineSimilarity computes cosine similarity between two vectors using SIMD
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	// Convert float32 to float64 for vek
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
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Similarity > results[i].Similarity {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}

// SessionMetadata represents session tracking info for the watch view
type SessionMetadata struct {
	ID           int64
	SessionID    string
	StartedAt    time.Time
	InitialPrompt string
	EventCount   int
	LastAction   string
	LastActionAt time.Time
	ProjectPath  string
}

// CreateOrUpdateSession creates a new session or updates an existing one.
// On first call (new session), it sets started_at and initial_prompt.
// On subsequent calls, it increments event_count and updates last_action.
func (s *Storage) CreateOrUpdateSession(sessionID, initialPrompt, lastAction, projectPath string) error {
	now := time.Now()

	// Try to insert first (new session)
	result, err := s.db.Exec(`
		INSERT INTO sessions (session_id, started_at, initial_prompt, event_count, last_action, last_action_at, project_path)
		VALUES (?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			event_count = event_count + 1,
			last_action = ?,
			last_action_at = ?
	`, sessionID, now, initialPrompt, lastAction, now, projectPath, lastAction, now)

	if err != nil {
		return fmt.Errorf("failed to create/update session: %w", err)
	}

	_ = result
	return nil
}

// UpdateSessionLastAction updates only the last action for a session
func (s *Storage) UpdateSessionLastAction(sessionID, lastAction string) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET last_action = ?, last_action_at = ?, event_count = event_count + 1
		WHERE session_id = ?
	`, lastAction, time.Now(), sessionID)

	if err != nil {
		return fmt.Errorf("failed to update session last action: %w", err)
	}
	return nil
}

// GetRecentSessions retrieves the most recent sessions
func (s *Storage) GetRecentSessions(limit int) ([]*SessionMetadata, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, started_at, initial_prompt, event_count, last_action, last_action_at, project_path
		FROM sessions
		ORDER BY started_at DESC
		LIMIT ?
	`, limit)

	if err != nil {
		return nil, fmt.Errorf("failed to get recent sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*SessionMetadata
	for rows.Next() {
		var sess SessionMetadata
		var initialPrompt, lastAction, projectPath sql.NullString
		var lastActionAt sql.NullTime

		err := rows.Scan(
			&sess.ID,
			&sess.SessionID,
			&sess.StartedAt,
			&initialPrompt,
			&sess.EventCount,
			&lastAction,
			&lastActionAt,
			&projectPath,
		)
		if err != nil {
			continue
		}

		if initialPrompt.Valid {
			sess.InitialPrompt = initialPrompt.String
		}
		if lastAction.Valid {
			sess.LastAction = lastAction.String
		}
		if lastActionAt.Valid {
			sess.LastActionAt = lastActionAt.Time
		}
		if projectPath.Valid {
			sess.ProjectPath = projectPath.String
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// GetSessionByID retrieves a specific session by session_id
func (s *Storage) GetSessionByID(sessionID string) (*SessionMetadata, error) {
	var sess SessionMetadata
	var initialPrompt, lastAction, projectPath sql.NullString
	var lastActionAt sql.NullTime

	err := s.db.QueryRow(`
		SELECT id, session_id, started_at, initial_prompt, event_count, last_action, last_action_at, project_path
		FROM sessions
		WHERE session_id = ?
	`, sessionID).Scan(
		&sess.ID,
		&sess.SessionID,
		&sess.StartedAt,
		&initialPrompt,
		&sess.EventCount,
		&lastAction,
		&lastActionAt,
		&projectPath,
	)

	if err != nil {
		return nil, err
	}

	if initialPrompt.Valid {
		sess.InitialPrompt = initialPrompt.String
	}
	if lastAction.Valid {
		sess.LastAction = lastAction.String
	}
	if lastActionAt.Valid {
		sess.LastActionAt = lastActionAt.Time
	}
	if projectPath.Valid {
		sess.ProjectPath = projectPath.String
	}

	return &sess, nil
}

// GetSessionEvents retrieves events for a specific session (for expanded view)
func (s *Storage) GetSessionEvents(sessionID string, limit int) ([]*events.Event, error) {
	rows, err := s.db.Query(`
		SELECT id, source, event_type, timestamp, tool_name, tool_input, tool_result, context, metadata
		FROM events
		WHERE json_extract(context, '$.session_id') = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, sessionID, limit)

	if err != nil {
		return nil, fmt.Errorf("failed to get session events: %w", err)
	}
	defer rows.Close()

	var eventList []*events.Event
	for rows.Next() {
		var event events.Event
		var toolInputJSON, contextJSON, metadataJSON string

		err := rows.Scan(
			&event.ID,
			&event.Source,
			&event.EventType,
			&event.Timestamp,
			&event.ToolName,
			&toolInputJSON,
			&event.ToolResult,
			&contextJSON,
			&metadataJSON,
		)

		if err != nil {
			continue
		}

		json.Unmarshal([]byte(toolInputJSON), &event.ToolInput)
		json.Unmarshal([]byte(contextJSON), &event.Context)
		json.Unmarshal([]byte(metadataJSON), &event.Metadata)

		eventList = append(eventList, &event)
	}

	return eventList, nil
}
