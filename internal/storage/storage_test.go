package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
)

func setupTestStorage(t *testing.T) (*Storage, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "cortex-storage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Create db directory
	if err := os.MkdirAll(filepath.Join(tempDir, "db"), 0755); err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create db dir: %v", err)
	}

	cfg := &config.Config{
		ContextDir: tempDir,
	}

	store, err := New(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create storage: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tempDir)
	}

	return store, cleanup
}

func createTestEvent(id, toolName, toolResult string) *events.Event {
	return &events.Event{
		ID:         id,
		Source:     events.SourceClaude,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   toolName,
		ToolInput:  map[string]interface{}{"file_path": "/test/file.go"},
		ToolResult: toolResult,
		Context: events.EventContext{
			ProjectPath: "/test/project",
			SessionID:   "test-session",
		},
	}
}

func TestNew(t *testing.T) {
	t.Run("creates storage successfully", func(t *testing.T) {
		store, cleanup := setupTestStorage(t)
		defer cleanup()

		if store == nil {
			t.Fatal("expected non-nil storage")
		}
		if store.db == nil {
			t.Fatal("expected non-nil database connection")
		}
	})

	t.Run("fails with invalid path", func(t *testing.T) {
		cfg := &config.Config{
			ContextDir: "/nonexistent/path/that/cannot/exist",
		}

		_, err := New(cfg)
		if err == nil {
			t.Fatal("expected error for invalid path")
		}
	})
}

func TestStoreAndGetEvent(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	t.Run("stores and retrieves event", func(t *testing.T) {
		event := createTestEvent("test-event-1", "Edit", "file modified")

		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("failed to store event: %v", err)
		}

		retrieved, err := store.GetEvent("test-event-1")
		if err != nil {
			t.Fatalf("failed to get event: %v", err)
		}

		if retrieved.ID != event.ID {
			t.Errorf("expected ID %s, got %s", event.ID, retrieved.ID)
		}
		if retrieved.ToolName != event.ToolName {
			t.Errorf("expected ToolName %s, got %s", event.ToolName, retrieved.ToolName)
		}
		if retrieved.ToolResult != event.ToolResult {
			t.Errorf("expected ToolResult %s, got %s", event.ToolResult, retrieved.ToolResult)
		}
		if string(retrieved.Source) != string(event.Source) {
			t.Errorf("expected Source %s, got %s", event.Source, retrieved.Source)
		}
	})

	t.Run("returns error for non-existent event", func(t *testing.T) {
		_, err := store.GetEvent("non-existent-id")
		if err == nil {
			t.Fatal("expected error for non-existent event")
		}
	})

	t.Run("handles duplicate event ID", func(t *testing.T) {
		event := createTestEvent("duplicate-id", "Write", "first")
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("failed to store first event: %v", err)
		}

		event2 := createTestEvent("duplicate-id", "Write", "second")
		err := store.StoreEvent(event2)
		if err == nil {
			t.Fatal("expected error for duplicate event ID")
		}
	})
}

func TestGetRecentEvents(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store multiple events with different timestamps
	for i := 0; i < 5; i++ {
		event := createTestEvent(
			"recent-event-"+string(rune('a'+i)),
			"Edit",
			"result "+string(rune('a'+i)),
		)
		event.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("failed to store event %d: %v", i, err)
		}
	}

	t.Run("retrieves limited events", func(t *testing.T) {
		events, err := store.GetRecentEvents(3)
		if err != nil {
			t.Fatalf("failed to get recent events: %v", err)
		}

		if len(events) != 3 {
			t.Errorf("expected 3 events, got %d", len(events))
		}
	})

	t.Run("returns events in descending order", func(t *testing.T) {
		events, err := store.GetRecentEvents(5)
		if err != nil {
			t.Fatalf("failed to get recent events: %v", err)
		}

		for i := 1; i < len(events); i++ {
			if events[i].Timestamp.After(events[i-1].Timestamp) {
				t.Error("events not in descending order by timestamp")
			}
		}
	})
}

func TestSearchEvents(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store events with searchable content
	events := []struct {
		id     string
		tool   string
		result string
	}{
		{"search-1", "Edit", "modified authentication module"},
		{"search-2", "Write", "created new user handler"},
		{"search-3", "Edit", "fixed authentication bug"},
		{"search-4", "Bash", "ran database migration"},
	}

	for _, e := range events {
		event := createTestEvent(e.id, e.tool, e.result)
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("failed to store event: %v", err)
		}
	}

	t.Run("finds events by result content", func(t *testing.T) {
		results, err := store.SearchEvents("authentication", 10)
		if err != nil {
			t.Fatalf("failed to search events: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("expected 2 results for 'authentication', got %d", len(results))
		}
	})

	t.Run("finds events by tool name", func(t *testing.T) {
		results, err := store.SearchEvents("Edit", 10)
		if err != nil {
			t.Fatalf("failed to search events: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("expected 2 results for 'Edit', got %d", len(results))
		}
	})

	t.Run("returns empty for no matches", func(t *testing.T) {
		results, err := store.SearchEvents("nonexistent", 10)
		if err != nil {
			t.Fatalf("failed to search events: %v", err)
		}

		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})
}

func TestGetStats(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store some events
	for i := 0; i < 3; i++ {
		event := createTestEvent("stats-event-"+string(rune('a'+i)), "Edit", "result")
		if err := store.StoreEvent(event); err != nil {
			t.Fatalf("failed to store event: %v", err)
		}
	}

	stats, err := store.GetStats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	t.Run("returns total events count", func(t *testing.T) {
		total, ok := stats["total_events"].(int)
		if !ok {
			t.Fatal("total_events not found or wrong type")
		}
		if total != 3 {
			t.Errorf("expected 3 total events, got %d", total)
		}
	})

	t.Run("returns events by source", func(t *testing.T) {
		bySource, ok := stats["by_source"].(map[string]int)
		if !ok {
			t.Fatal("by_source not found or wrong type")
		}
		if bySource["claude"] != 3 {
			t.Errorf("expected 3 claude events, got %d", bySource["claude"])
		}
	})
}

func TestStoreAndGetEntity(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	t.Run("stores and retrieves entity", func(t *testing.T) {
		id, err := store.StoreEntity("file", "/src/main.go")
		if err != nil {
			t.Fatalf("failed to store entity: %v", err)
		}
		if id == 0 {
			t.Fatal("expected non-zero entity ID")
		}

		entity, err := store.GetEntity("file", "/src/main.go")
		if err != nil {
			t.Fatalf("failed to get entity: %v", err)
		}

		if entity.Type != "file" {
			t.Errorf("expected type 'file', got '%s'", entity.Type)
		}
		if entity.Name != "/src/main.go" {
			t.Errorf("expected name '/src/main.go', got '%s'", entity.Name)
		}
	})

	t.Run("updates last_seen on duplicate", func(t *testing.T) {
		_, err := store.StoreEntity("concept", "authentication")
		if err != nil {
			t.Fatalf("failed to store entity first time: %v", err)
		}

		time.Sleep(10 * time.Millisecond)

		_, err = store.StoreEntity("concept", "authentication")
		if err != nil {
			t.Fatalf("failed to store entity second time: %v", err)
		}

		entity, err := store.GetEntity("concept", "authentication")
		if err != nil {
			t.Fatalf("failed to get entity: %v", err)
		}

		if entity.LastSeen.Before(entity.FirstSeen) {
			t.Error("last_seen should be after or equal to first_seen")
		}
	})

	t.Run("retrieves entity by ID", func(t *testing.T) {
		id, _ := store.StoreEntity("pattern", "singleton")

		entity, err := store.GetEntityByID(id)
		if err != nil {
			t.Fatalf("failed to get entity by ID: %v", err)
		}

		if entity.Name != "singleton" {
			t.Errorf("expected name 'singleton', got '%s'", entity.Name)
		}
	})
}

func TestGetEntitiesByType(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Store entities of different types
	store.StoreEntity("file", "/src/a.go")
	store.StoreEntity("file", "/src/b.go")
	store.StoreEntity("concept", "auth")
	store.StoreEntity("pattern", "factory")

	t.Run("retrieves only entities of specified type", func(t *testing.T) {
		files, err := store.GetEntitiesByType("file")
		if err != nil {
			t.Fatalf("failed to get entities: %v", err)
		}

		if len(files) != 2 {
			t.Errorf("expected 2 file entities, got %d", len(files))
		}

		for _, e := range files {
			if e.Type != "file" {
				t.Errorf("expected type 'file', got '%s'", e.Type)
			}
		}
	})
}

func TestStoreAndGetRelationship(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// Create entities
	fileID, _ := store.StoreEntity("file", "/src/auth.go")
	conceptID, _ := store.StoreEntity("concept", "authentication")

	t.Run("stores and retrieves relationship", func(t *testing.T) {
		err := store.StoreRelationship(fileID, conceptID, "implements", "event-123")
		if err != nil {
			t.Fatalf("failed to store relationship: %v", err)
		}

		rels, err := store.GetRelationships(fileID)
		if err != nil {
			t.Fatalf("failed to get relationships: %v", err)
		}

		if len(rels) != 1 {
			t.Fatalf("expected 1 relationship, got %d", len(rels))
		}

		if rels[0].RelationType != "implements" {
			t.Errorf("expected relation type 'implements', got '%s'", rels[0].RelationType)
		}
	})

	t.Run("finds related entities", func(t *testing.T) {
		related, err := store.GetRelatedEntities(fileID, "")
		if err != nil {
			t.Fatalf("failed to get related entities: %v", err)
		}

		if len(related) != 1 {
			t.Fatalf("expected 1 related entity, got %d", len(related))
		}

		if related[0].Name != "authentication" {
			t.Errorf("expected related entity 'authentication', got '%s'", related[0].Name)
		}
	})
}

func TestStoreAndGetInsight(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	// First store an event (insights reference events)
	event := createTestEvent("insight-event-1", "Edit", "modified auth")
	store.StoreEvent(event)

	t.Run("stores and retrieves insight by category", func(t *testing.T) {
		err := store.StoreInsight(
			"insight-event-1",
			"decision",
			"Use JWT for authentication",
			8,
			[]string{"auth", "security"},
			"JWT provides stateless authentication",
		)
		if err != nil {
			t.Fatalf("failed to store insight: %v", err)
		}

		insights, err := store.GetInsightsByCategory("decision", 10)
		if err != nil {
			t.Fatalf("failed to get insights: %v", err)
		}

		if len(insights) != 1 {
			t.Fatalf("expected 1 insight, got %d", len(insights))
		}

		insight := insights[0]
		if insight.Summary != "Use JWT for authentication" {
			t.Errorf("unexpected summary: %s", insight.Summary)
		}
		if insight.Importance != 8 {
			t.Errorf("expected importance 8, got %d", insight.Importance)
		}
		if len(insight.Tags) != 2 {
			t.Errorf("expected 2 tags, got %d", len(insight.Tags))
		}
	})
}

func TestGetRecentInsights(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("recent-insight-event", "Edit", "test")
	store.StoreEvent(event)

	// Store multiple insights
	for i := 0; i < 5; i++ {
		store.StoreInsight(
			"recent-insight-event",
			"pattern",
			"Pattern "+string(rune('A'+i)),
			i+1,
			[]string{"test"},
			"reasoning",
		)
	}

	t.Run("retrieves limited recent insights", func(t *testing.T) {
		insights, err := store.GetRecentInsights(3)
		if err != nil {
			t.Fatalf("failed to get recent insights: %v", err)
		}

		if len(insights) != 3 {
			t.Errorf("expected 3 insights, got %d", len(insights))
		}
	})
}

func TestGetImportantInsights(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("important-insight-event", "Edit", "test")
	store.StoreEvent(event)

	// Store insights with varying importance
	importances := []int{3, 5, 7, 9, 2}
	for i, imp := range importances {
		store.StoreInsight(
			"important-insight-event",
			"decision",
			"Decision "+string(rune('A'+i)),
			imp,
			[]string{},
			"reasoning",
		)
	}

	t.Run("filters by minimum importance", func(t *testing.T) {
		insights, err := store.GetImportantInsights(7, 10)
		if err != nil {
			t.Fatalf("failed to get important insights: %v", err)
		}

		if len(insights) != 2 {
			t.Errorf("expected 2 insights with importance >= 7, got %d", len(insights))
		}

		for _, insight := range insights {
			if insight.Importance < 7 {
				t.Errorf("insight importance %d is below minimum 7", insight.Importance)
			}
		}
	})

	t.Run("orders by importance descending", func(t *testing.T) {
		insights, err := store.GetImportantInsights(1, 10)
		if err != nil {
			t.Fatalf("failed to get insights: %v", err)
		}

		for i := 1; i < len(insights); i++ {
			if insights[i].Importance > insights[i-1].Importance {
				t.Error("insights not ordered by importance descending")
			}
		}
	})
}
