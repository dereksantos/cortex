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
