// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

func init() {
	Register(&SearchCommand{})
	Register(&RecentCommand{})
	Register(&InsightsCommand{})
	Register(&EntitiesCommand{})
	Register(&GraphCommand{})
}

// SearchCommand implements context search.
type SearchCommand struct{}

// Name returns the command name.
func (c *SearchCommand) Name() string { return "search" }

// Description returns the command description.
func (c *SearchCommand) Description() string { return "Search context using cognitive retrieval" }

// Execute runs the search command.
func (c *SearchCommand) Execute(ctx *Context) error {
	// Parse flags
	searchFlags := flag.NewFlagSet("search", flag.ExitOnError)
	modeFlag := searchFlags.String("mode", "fast", "Retrieval mode: fast (Reflex only) or full (Reflex + Reflect)")
	limitFlag := searchFlags.Int("limit", 5, "Maximum number of results")
	searchFlags.Parse(ctx.Args)

	args := searchFlags.Args()
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: cortex search [--mode=fast|full] [--limit=N] <query>\n")
		return fmt.Errorf("missing query argument")
	}

	query := strings.Join(args, " ")

	// Determine retrieval mode
	var mode cognition.RetrieveMode
	switch *modeFlag {
	case "full":
		mode = cognition.Full
	default:
		mode = cognition.Fast
	}

	cfg := ctx.Config
	store := ctx.Storage

	// Initialize LLM provider (required for Full mode, optional for Fast)
	var llmProvider llm.Provider
	if mode == cognition.Full {
		// Try Anthropic first, then Ollama
		anthropic := llm.NewAnthropicClient(cfg)
		if anthropic.IsAvailable() {
			llmProvider = anthropic
		} else {
			ollama := llm.NewOllamaClient(cfg)
			if ollama.IsAvailable() {
				llmProvider = ollama
			}
		}
		if llmProvider == nil {
			fmt.Fprintf(os.Stderr, "Warning: No LLM provider available, falling back to fast mode\n")
			mode = cognition.Fast
		}
	}

	// Initialize embedder with fallback: Ollama -> Hugot
	ollamaClient := llm.NewOllamaClient(cfg)
	hugotEmbedder := llm.NewHugotEmbedder()
	embedder := llm.NewFallbackEmbedder(ollamaClient, hugotEmbedder)

	// Create Cortex cognitive pipeline
	cortex, err := intcognition.New(store, llmProvider, embedder, cfg)
	if err != nil {
		return fmt.Errorf("failed to create cognitive pipeline: %w", err)
	}

	// Build query
	q := cognition.Query{
		Text:  query,
		Limit: *limitFlag,
	}

	// Retrieve using cognitive pipeline
	start := time.Now()
	result, err := cortex.Retrieve(context.Background(), q, mode)
	elapsed := time.Since(start)

	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Log the query to activity log
	activityLogger := intcognition.NewActivityLogger(cfg.ContextDir)
	resultCount := 0
	if result != nil {
		resultCount = len(result.Results)
	}
	activityLogger.LogQuery(query, resultCount, elapsed.Milliseconds())

	// Determine mode string for output
	modeStr := "Fast (Reflex)"
	if mode == cognition.Full {
		modeStr = "Full (Reflex + Reflect)"
	}

	// Display results
	if result == nil || len(result.Results) == 0 {
		// Fallback: search events directly when Reflex returns nothing.
		// Extract terms from natural language query for better matching.
		terms := intcognition.ExtractTerms(query)
		var eventResults []*events.Event
		var searchErr error
		if len(terms) > 0 {
			eventResults, searchErr = store.SearchEventsMultiTerm(terms, *limitFlag)
		} else {
			eventResults, searchErr = store.SearchEvents(query, *limitFlag)
		}
		if searchErr != nil || len(eventResults) == 0 {
			fmt.Println("No results found")
			return nil
		}
		fmt.Printf("Mode: %s (fallback) | Results: %d | Time: %v\n\n", modeStr, len(eventResults), elapsed.Round(time.Millisecond))
		for i, ev := range eventResults {
			preview := ev.ToolResult
			if preview == "" {
				preview = ev.ToolName
			}
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			fmt.Printf("%d. [%s] %s\n", i+1, ev.EventType, preview)
			fmt.Println()
		}
		return nil
	}
	fmt.Printf("Mode: %s | Results: %d | Time: %v\n\n", modeStr, len(result.Results), elapsed.Round(time.Millisecond))

	// Show results
	for i, r := range result.Results {
		preview := r.Content
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		fmt.Printf("%d. [%.0f%% match] %s\n", i+1, r.Score*100, preview)
		fmt.Println()
	}

	return nil
}

// RecentCommand shows recent events.
type RecentCommand struct{}

// Name returns the command name.
func (c *RecentCommand) Name() string { return "recent" }

// Description returns the command description.
func (c *RecentCommand) Description() string { return "Show recent captured events" }

// Execute runs the recent command.
func (c *RecentCommand) Execute(ctx *Context) error {
	limit := 10
	if len(ctx.Args) >= 1 {
		fmt.Sscanf(ctx.Args[0], "%d", &limit)
	}

	store := ctx.Storage

	// Get recent events
	recentEvents, err := store.GetRecentEvents(limit)
	if err != nil {
		return fmt.Errorf("failed to get recent events: %w", err)
	}

	// Display results
	if len(recentEvents) == 0 {
		fmt.Println("No events found")
		return nil
	}

	fmt.Printf("Recent %d events:\n\n", len(recentEvents))
	for i, event := range recentEvents {
		fmt.Printf("%d. [%s] %s - %s\n", i+1, event.Source, event.ToolName, event.Timestamp.Format("2006-01-02 15:04"))
		if filePath, ok := event.ToolInput["file_path"].(string); ok {
			fmt.Printf("   File: %s\n", filePath)
		}
		fmt.Println()
	}

	return nil
}

// InsightsCommand shows extracted insights.
type InsightsCommand struct{}

// Name returns the command name.
func (c *InsightsCommand) Name() string { return "insights" }

// Description returns the command description.
func (c *InsightsCommand) Description() string { return "Show extracted insights" }

// Execute runs the insights command.
func (c *InsightsCommand) Execute(ctx *Context) error {
	category := ""
	limit := 10

	if len(ctx.Args) >= 1 {
		category = ctx.Args[0]
	}
	if len(ctx.Args) >= 2 {
		fmt.Sscanf(ctx.Args[1], "%d", &limit)
	}

	store := ctx.Storage

	// Get insights
	var insightList []*storage.Insight
	var err error
	if category != "" {
		insightList, err = store.GetInsightsByCategory(category, limit)
	} else {
		insightList, err = store.GetRecentInsights(limit)
	}

	if err != nil {
		return fmt.Errorf("failed to get insights: %w", err)
	}

	// Display results
	if len(insightList) == 0 {
		fmt.Println("No insights found")
		return nil
	}

	if category != "" {
		fmt.Printf("%s Insights:\n\n", category)
	} else {
		fmt.Printf("Recent Insights:\n\n")
	}

	for i, insight := range insightList {
		importance := ""
		for j := 0; j < insight.Importance && j < 5; j++ {
			importance += "*"
		}

		fmt.Printf("%d. [%s] %s %s\n", i+1, insight.Category, insight.Summary, importance)
		if len(insight.Tags) > 0 {
			fmt.Printf("   Tags: %v\n", insight.Tags)
		}
		fmt.Printf("   %s\n", insight.CreatedAt.Format("2006-01-02 15:04"))
		fmt.Println()
	}

	return nil
}

// EntitiesCommand shows knowledge graph entities.
type EntitiesCommand struct{}

// Name returns the command name.
func (c *EntitiesCommand) Name() string { return "entities" }

// Description returns the command description.
func (c *EntitiesCommand) Description() string { return "Show knowledge graph entities" }

// Execute runs the entities command.
func (c *EntitiesCommand) Execute(ctx *Context) error {
	entityType := ""
	if len(ctx.Args) >= 1 {
		entityType = ctx.Args[0]
	}

	store := ctx.Storage

	// Get entities
	var entityList []*storage.Entity
	var err error
	if entityType != "" {
		entityList, err = store.GetEntitiesByType(entityType)
	} else {
		// Get all entity types
		types := []string{"decision", "pattern", "insight", "strategy"}
		for _, t := range types {
			typeEntities, _ := store.GetEntitiesByType(t)
			entityList = append(entityList, typeEntities...)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to get entities: %w", err)
	}

	// Display results
	if len(entityList) == 0 {
		fmt.Println("No entities found")
		return nil
	}

	fmt.Printf("Entities:\n\n")
	for i, entity := range entityList {
		fmt.Printf("%d. [%s] %s\n", i+1, entity.Type, entity.Name)
		fmt.Printf("   First seen: %s, Last seen: %s\n",
			entity.FirstSeen.Format("2006-01-02"),
			entity.LastSeen.Format("2006-01-02"))
		fmt.Println()
	}

	return nil
}

// GraphCommand shows entity relationships.
type GraphCommand struct{}

// Name returns the command name.
func (c *GraphCommand) Name() string { return "graph" }

// Description returns the command description.
func (c *GraphCommand) Description() string { return "Show entity relationship graph" }

// Execute runs the graph command.
func (c *GraphCommand) Execute(ctx *Context) error {
	if len(ctx.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: cortex graph <entity_type> <entity_name>\n")
		return fmt.Errorf("missing arguments")
	}

	entityType := ctx.Args[0]
	entityName := ctx.Args[1]

	store := ctx.Storage

	// Get entity
	entity, err := store.GetEntity(entityType, entityName)
	if err != nil {
		return fmt.Errorf("entity not found: %w", err)
	}

	// Get relationships
	relationships, err := store.GetRelationships(entity.ID)
	if err != nil {
		return fmt.Errorf("failed to get relationships: %w", err)
	}

	// Display entity and relationships
	fmt.Printf("Knowledge Graph for: %s (%s)\n\n", entity.Name, entity.Type)
	fmt.Printf("First seen: %s\n", entity.FirstSeen.Format("2006-01-02"))
	fmt.Printf("Last seen: %s\n\n", entity.LastSeen.Format("2006-01-02"))

	if len(relationships) == 0 {
		fmt.Println("No relationships found")
		return nil
	}

	fmt.Printf("Relationships (%d):\n\n", len(relationships))
	for i, rel := range relationships {
		if rel.FromEntity != nil && rel.ToEntity != nil {
			fmt.Printf("%d. %s -[%s]-> %s\n",
				i+1,
				rel.FromEntity.Name,
				rel.RelationType,
				rel.ToEntity.Name)
		}
	}

	return nil
}

// AnalyzeEventWithLLM analyzes an event using the LLM and stores the insight.
// Used by CLI commands for sync analysis (daemon uses cognition modes instead).
func AnalyzeEventWithLLM(event *events.Event, st *storage.Storage, provider llm.Provider) error {
	if provider == nil || !provider.IsAvailable() {
		return fmt.Errorf("LLM not available")
	}

	// Skip routine events
	if event.ToolName == "Read" || event.ToolName == "Grep" || event.ToolName == "Glob" {
		return fmt.Errorf("skipped routine event")
	}

	// Build prompt for analysis
	eventDesc := fmt.Sprintf("Tool: %s\n", event.ToolName)
	if filePath, ok := event.ToolInput["file_path"].(string); ok {
		eventDesc += fmt.Sprintf("File: %s\n", filePath)
	}
	if event.ToolResult != "" && len(event.ToolResult) < 500 {
		eventDesc += fmt.Sprintf("Result: %s\n", event.ToolResult)
	}

	prompt := fmt.Sprintf(`Analyze this development event for durable insights:

%s

Extract any decisions, patterns, or constraints. Respond in JSON:
{
  "category": "decision|pattern|constraint|correction",
  "summary": "1-2 sentence insight",
  "importance": 1-10,
  "tags": ["tag1", "tag2"]
}

If nothing significant, respond: NO_INSIGHT`, eventDesc)

	response, err := provider.GenerateWithSystem(context.Background(), prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return err
	}

	// Check for NO_INSIGHT
	if strings.Contains(strings.ToUpper(response), "NO_INSIGHT") {
		return fmt.Errorf("no insight found")
	}

	// Parse JSON response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 {
		return fmt.Errorf("invalid response format")
	}

	var result struct {
		Category   string   `json:"category"`
		Summary    string   `json:"summary"`
		Importance int      `json:"importance"`
		Tags       []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(response[start:end+1]), &result); err != nil {
		return err
	}

	// Store insight
	return st.StoreInsight(
		event.ID,
		result.Category,
		result.Summary,
		result.Importance,
		result.Tags,
		"",
	)
}

// ExtractKeyTerms removes common words and extracts key terms from a prompt.
func ExtractKeyTerms(prompt string) string {
	// Remove common words and extract key terms
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"is": true, "are": true, "was": true, "were": true, "be": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "should": true, "could": true,
		"can": true, "may": true, "might": true, "must": true,
		"i": true, "you": true, "he": true, "she": true, "it": true,
		"we": true, "they": true, "them": true, "their": true, "my": true,
		"help": true, "me": true, "please": true, "want": true, "need": true,
		"how": true, "what": true, "when": true, "where": true, "why": true,
	}

	words := strings.Fields(strings.ToLower(prompt))
	var keyTerms []string

	for _, word := range words {
		// Clean word (remove punctuation)
		word = strings.Trim(word, ",.!?;:\"'")
		// Skip if stop word or too short
		if len(word) < 3 || stopWords[word] {
			continue
		}
		keyTerms = append(keyTerms, word)
	}

	return strings.Join(keyTerms, " ")
}

// FindRelevantInsights finds insights matching the query terms.
func FindRelevantInsights(insights []*storage.Insight, query string, limit int) []*storage.Insight {
	type scoredInsight struct {
		insight *storage.Insight
		score   int
	}

	var scored []scoredInsight
	queryTerms := strings.Fields(strings.ToLower(query))

	for _, insight := range insights {
		score := 0
		searchText := strings.ToLower(insight.Summary + " " + strings.Join(insight.Tags, " ") + " " + insight.Category)

		// Count matching terms
		for _, term := range queryTerms {
			if strings.Contains(searchText, term) {
				score++
			}
		}

		if score > 0 {
			scored = append(scored, scoredInsight{insight, score})
		}
	}

	// Sort by score (descending)
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Return top N
	var result []*storage.Insight
	for i := 0; i < limit && i < len(scored); i++ {
		result = append(result, scored[i].insight)
	}

	return result
}
