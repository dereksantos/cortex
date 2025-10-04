// Package storage provides event sourcing storage with SQLite
package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

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

	-- Full-text search index
	CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
		event_id,
		tool_name,
		tool_result,
		content='events',
		content_rowid='rowid'
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

// SearchEvents performs full-text search on events
func (s *Storage) SearchEvents(query string, limit int) ([]*events.Event, error) {
	// Simple LIKE search for now (we'll enhance with FTS later)
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

	// Date range
	var oldest, newest time.Time
	s.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM events").Scan(&oldest, &newest)
	stats["oldest_event"] = oldest
	stats["newest_event"] = newest

	return stats, nil
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
	tagsJSON, _ := json.Marshal(tags)

	_, err := s.db.Exec(`
		INSERT INTO insights (event_id, category, summary, importance, tags, reasoning)
		VALUES (?, ?, ?, ?, ?, ?)
	`, eventID, category, summary, importance, string(tagsJSON), reasoning)

	if err != nil {
		return fmt.Errorf("failed to store insight: %w", err)
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
