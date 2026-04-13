package storage

import (
	"os"
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
		if store.dataDir == "" {
			t.Fatal("expected non-empty data directory")
		}
	})

	t.Run("fails with invalid path", func(t *testing.T) {
		cfg := &config.Config{
			ContextDir: "/nonexistent/\x00invalid/path",
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

func TestMergeInsights(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("merge-event", "Edit", "test")
	store.StoreEvent(event)

	// Store 3 similar insights (simulating duplicates)
	store.StoreInsight("merge-event", "decision", "Use JWT for auth", 8, []string{"auth", "jwt"}, "reason1")
	store.StoreInsight("merge-event", "decision", "Use JWT tokens for authentication", 7, []string{"auth", "tokens"}, "reason2")
	store.StoreInsight("merge-event", "decision", "JWT authentication decision", 6, []string{"security"}, "reason3")

	// Get all insights to find their IDs
	insights, err := store.GetInsightsByCategory("decision", 10)
	if err != nil {
		t.Fatalf("failed to get insights: %v", err)
	}

	if len(insights) != 3 {
		t.Fatalf("expected 3 insights before merge, got %d", len(insights))
	}

	// Keep the first (highest importance), delete the others
	keepID := insights[0].ID
	deleteIDs := []int64{insights[1].ID, insights[2].ID}

	t.Run("merges insights and combines tags", func(t *testing.T) {
		deleted, err := store.MergeInsights(keepID, deleteIDs)
		if err != nil {
			t.Fatalf("failed to merge insights: %v", err)
		}

		if deleted != 2 {
			t.Errorf("expected 2 deleted, got %d", deleted)
		}

		// Check remaining insights
		remaining, _ := store.GetInsightsByCategory("decision", 10)
		if len(remaining) != 1 {
			t.Errorf("expected 1 insight after merge, got %d", len(remaining))
		}

		// Check tags were merged
		survivor := remaining[0]
		expectedTags := map[string]bool{"auth": true, "jwt": true, "tokens": true, "security": true}
		for _, tag := range survivor.Tags {
			if !expectedTags[tag] {
				t.Errorf("unexpected tag: %s", tag)
			}
			delete(expectedTags, tag)
		}
		if len(expectedTags) > 0 {
			t.Errorf("missing expected tags: %v", expectedTags)
		}
	})

	t.Run("handles empty delete list", func(t *testing.T) {
		deleted, err := store.MergeInsights(keepID, []int64{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deleted != 0 {
			t.Errorf("expected 0 deleted for empty list, got %d", deleted)
		}
	})
}

func TestStoreInsightWithSession(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("session-insight-event", "Edit", "test")
	store.StoreEvent(event)

	t.Run("stores insight with session_id and source_type", func(t *testing.T) {
		err := store.StoreInsightWithSession(
			"session-insight-event",
			"decision",
			"Use Zustand for state management",
			8,
			[]string{"state", "react"},
			"reasoning",
			"sess-123-abc",
			"project",
		)
		if err != nil {
			t.Fatalf("failed to store insight with session: %v", err)
		}

		// Retrieve and verify
		insights, err := store.GetInsightsBySession("sess-123-abc", 10)
		if err != nil {
			t.Fatalf("failed to get insights by session: %v", err)
		}

		if len(insights) != 1 {
			t.Fatalf("expected 1 insight, got %d", len(insights))
		}

		insight := insights[0]
		if insight.SessionID != "sess-123-abc" {
			t.Errorf("expected session_id 'sess-123-abc', got '%s'", insight.SessionID)
		}
		if insight.SourceType != "project" {
			t.Errorf("expected source_type 'project', got '%s'", insight.SourceType)
		}
		if insight.Summary != "Use Zustand for state management" {
			t.Errorf("unexpected summary: %s", insight.Summary)
		}
	})

	t.Run("handles nil session_id gracefully", func(t *testing.T) {
		err := store.StoreInsightWithSession(
			"session-insight-event",
			"pattern",
			"Another pattern",
			5,
			[]string{"test"},
			"",
			"", // empty session_id
			"git",
		)
		if err != nil {
			t.Fatalf("failed to store insight without session: %v", err)
		}

		// Verify it was stored
		insights, err := store.GetInsightsBySourceType("git", 10)
		if err != nil {
			t.Fatalf("failed to get insights by source_type: %v", err)
		}

		if len(insights) != 1 {
			t.Fatalf("expected 1 insight, got %d", len(insights))
		}

		if insights[0].SessionID != "" {
			t.Errorf("expected empty session_id, got '%s'", insights[0].SessionID)
		}
	})
}

func TestGetInsightsBySession(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("session-test-event", "Edit", "test")
	store.StoreEvent(event)

	// Store insights for different sessions
	store.StoreInsightWithSession("session-test-event", "decision", "Decision A", 7, []string{"a"}, "", "session-1", "project")
	store.StoreInsightWithSession("session-test-event", "decision", "Decision B", 8, []string{"b"}, "", "session-1", "project")
	store.StoreInsightWithSession("session-test-event", "decision", "Decision C", 9, []string{"c"}, "", "session-2", "cortex")

	t.Run("retrieves only insights from specified session", func(t *testing.T) {
		insights, err := store.GetInsightsBySession("session-1", 10)
		if err != nil {
			t.Fatalf("failed to get insights by session: %v", err)
		}

		if len(insights) != 2 {
			t.Errorf("expected 2 insights for session-1, got %d", len(insights))
		}

		for _, insight := range insights {
			if insight.SessionID != "session-1" {
				t.Errorf("got insight from wrong session: %s", insight.SessionID)
			}
		}
	})

	t.Run("returns empty for non-existent session", func(t *testing.T) {
		insights, err := store.GetInsightsBySession("non-existent", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(insights) != 0 {
			t.Errorf("expected 0 insights for non-existent session, got %d", len(insights))
		}
	})
}

func TestMarkInsightRetrieved(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("retrieved-test-event", "Edit", "test")
	store.StoreEvent(event)

	// Store an insight
	store.StoreInsight("retrieved-test-event", "decision", "Test decision", 7, []string{"test"}, "")

	insights, _ := store.GetRecentInsights(1)
	if len(insights) == 0 {
		t.Fatal("expected at least 1 insight")
	}
	insightID := insights[0].ID

	t.Run("marks insight as retrieved", func(t *testing.T) {
		// Verify initially not retrieved
		insight, _ := store.GetInsightByID(insightID)
		if insight.WasRetrieved {
			t.Error("expected was_retrieved to be false initially")
		}

		// Mark as retrieved
		err := store.MarkInsightRetrieved(insightID)
		if err != nil {
			t.Fatalf("failed to mark insight as retrieved: %v", err)
		}

		// Verify it's now retrieved
		insight, _ = store.GetInsightByID(insightID)
		if !insight.WasRetrieved {
			t.Error("expected was_retrieved to be true after marking")
		}
	})
}

func TestGetUnretrievedInsights(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("unretrieved-test-event", "Edit", "test")
	store.StoreEvent(event)

	// Store some insights
	store.StoreInsight("unretrieved-test-event", "decision", "Decision 1", 7, []string{"a"}, "")
	store.StoreInsight("unretrieved-test-event", "decision", "Decision 2", 8, []string{"b"}, "")
	store.StoreInsight("unretrieved-test-event", "decision", "Decision 3", 9, []string{"c"}, "")

	// Mark one as retrieved
	insights, _ := store.GetRecentInsights(10)
	if len(insights) < 3 {
		t.Fatalf("expected at least 3 insights, got %d", len(insights))
	}
	store.MarkInsightRetrieved(insights[0].ID)

	t.Run("returns only unretrieved insights", func(t *testing.T) {
		unretrieved, err := store.GetUnretrievedInsights(10)
		if err != nil {
			t.Fatalf("failed to get unretrieved insights: %v", err)
		}

		if len(unretrieved) != 2 {
			t.Errorf("expected 2 unretrieved insights, got %d", len(unretrieved))
		}

		for _, insight := range unretrieved {
			if insight.WasRetrieved {
				t.Error("got a retrieved insight when requesting unretrieved")
			}
		}
	})
}

func TestMarkInsightsRetrieved(t *testing.T) {
	store, cleanup := setupTestStorage(t)
	defer cleanup()

	event := createTestEvent("batch-retrieved-event", "Edit", "test")
	store.StoreEvent(event)

	// Store several insights
	for i := 0; i < 5; i++ {
		store.StoreInsight("batch-retrieved-event", "pattern", "Pattern", i+1, []string{}, "")
	}

	insights, _ := store.GetRecentInsights(10)
	ids := make([]int64, len(insights))
	for i, insight := range insights {
		ids[i] = insight.ID
	}

	t.Run("marks multiple insights as retrieved", func(t *testing.T) {
		// Mark first 3
		err := store.MarkInsightsRetrieved(ids[:3])
		if err != nil {
			t.Fatalf("failed to mark insights as retrieved: %v", err)
		}

		// Verify
		unretrieved, _ := store.GetUnretrievedInsights(10)
		if len(unretrieved) != 2 {
			t.Errorf("expected 2 unretrieved after marking 3, got %d", len(unretrieved))
		}
	})

	t.Run("handles empty list gracefully", func(t *testing.T) {
		err := store.MarkInsightsRetrieved([]int64{})
		if err != nil {
			t.Fatalf("unexpected error for empty list: %v", err)
		}
	})
}
