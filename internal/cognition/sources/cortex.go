package sources

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// CortexSource samples from Cortex's own database (events, insights, entities).
type CortexSource struct {
	storage *storage.Storage
	rng     *rand.Rand
}

// NewCortexSource creates a new CortexSource.
func NewCortexSource(store *storage.Storage) *CortexSource {
	return &CortexSource{
		storage: store,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Name returns the source identifier.
func (c *CortexSource) Name() string {
	return "cortex"
}

// Sample returns random items from Cortex's database.
func (c *CortexSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	var items []cognition.DreamItem

	// Sample from recent events
	events, err := c.storage.GetRecentEvents(n * 2)
	if err == nil && len(events) > 0 {
		// Randomly select some events
		c.rng.Shuffle(len(events), func(i, j int) {
			events[i], events[j] = events[j], events[i]
		})

		limit := n / 2
		if limit > len(events) {
			limit = len(events)
		}

		for i := 0; i < limit; i++ {
			event := events[i]
			content := fmt.Sprintf("Tool: %s\nResult: %s", event.ToolName, event.ToolResult)
			if len(content) > 2000 {
				content = content[:2000] + "..."
			}

			items = append(items, cognition.DreamItem{
				ID:      "event:" + event.ID,
				Source:  "cortex",
				Content: content,
				Path:    event.ToolName,
				Metadata: map[string]any{
					"event_type": event.EventType,
					"timestamp":  event.Timestamp,
				},
			})
		}
	}

	// Sample from insights
	insights, err := c.storage.GetRecentInsights(n * 2)
	if err == nil && len(insights) > 0 {
		c.rng.Shuffle(len(insights), func(i, j int) {
			insights[i], insights[j] = insights[j], insights[i]
		})

		limit := n / 2
		if limit > len(insights) {
			limit = len(insights)
		}

		for i := 0; i < limit; i++ {
			insight := insights[i]
			content := fmt.Sprintf("Category: %s\nSummary: %s\nReasoning: %s",
				insight.Category, insight.Summary, insight.Reasoning)

			items = append(items, cognition.DreamItem{
				ID:      fmt.Sprintf("insight:%d", insight.ID),
				Source:  "cortex",
				Content: content,
				Path:    insight.Category,
				Metadata: map[string]any{
					"importance": insight.Importance,
					"tags":       insight.Tags,
					"event_id":   insight.EventID,
				},
			})
		}
	}

	// Sample from entities
	entityTypes := []string{"decision", "pattern", "concept", "file"}
	for _, entityType := range entityTypes {
		entities, err := c.storage.GetEntitiesByType(entityType)
		if err != nil || len(entities) == 0 {
			continue
		}

		// Pick one random entity of this type
		entity := entities[c.rng.Intn(len(entities))]

		// Get related entities
		related, _ := c.storage.GetRelatedEntities(entity.ID, "")
		relatedNames := make([]string, 0, len(related))
		for _, r := range related {
			relatedNames = append(relatedNames, r.Name)
		}

		content := fmt.Sprintf("Entity: %s (type: %s)\nRelated: %v",
			entity.Name, entity.Type, relatedNames)

		items = append(items, cognition.DreamItem{
			ID:      fmt.Sprintf("entity:%d", entity.ID),
			Source:  "cortex",
			Content: content,
			Path:    entity.Type + "/" + entity.Name,
			Metadata: map[string]any{
				"first_seen": entity.FirstSeen,
				"last_seen":  entity.LastSeen,
				"related":    relatedNames,
			},
		})
	}

	// Limit to n items
	if len(items) > n {
		c.rng.Shuffle(len(items), func(i, j int) {
			items[i], items[j] = items[j], items[i]
		})
		items = items[:n]
	}

	return items, nil
}
