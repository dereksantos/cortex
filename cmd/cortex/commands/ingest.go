// Package commands provides CLI command implementations.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"log"

	"github.com/dereksantos/cortex/integrations/claude"
	"github.com/dereksantos/cortex/integrations/cursor"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
	projreg "github.com/dereksantos/cortex/pkg/registry"
)

// emitCorrection writes a feedback.correction journal entry for the user-
// supplied correction text. Best-effort: errors logged.
func emitCorrection(cfg *config.Config, content string) {
	if cfg == nil || cfg.ContextDir == "" {
		return
	}
	classDir := filepath.Join(cfg.ContextDir, "journal", "feedback")
	// GradedID is "correction:user-text" — free-form corrections don't
	// yet target a specific derivation. The projection (B3) attaches
	// these to recent insights heuristically; better resolution lands
	// when corrections include a specific target.
	entry, err := journal.NewFeedbackEntry(journal.TypeFeedbackCorrection, journal.FeedbackPayload{
		GradedID:    "free-form",
		Note:        content,
		Replacement: content,
	})
	if err != nil {
		log.Printf("correct: build feedback entry: %v", err)
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: classDir,
		Fsync:    journal.FsyncPerEntry,
	})
	if err != nil {
		log.Printf("correct: open journal writer: %v", err)
		return
	}
	defer w.Close()
	if _, err := w.Append(entry); err != nil {
		log.Printf("correct: append journal entry: %v", err)
	}
}

// CaptureCommand implements event capture from stdin.
type CaptureCommand struct{}

// IngestCommand implements moving queued events to database.
type IngestCommand struct{}

// AnalyzeCommand implements LLM analysis on recent events.
type AnalyzeCommand struct{}

// ProcessCommand implements combined ingest + analyze (backward compat).
type ProcessCommand struct{}

// FeedCommand implements manual knowledge seeding from files.
type FeedCommand struct{}

func init() {
	Register(&CaptureCommand{})
	Register(&IngestCommand{})
	Register(&AnalyzeCommand{})
	Register(&ProcessCommand{})
	Register(&FeedCommand{})
}

// Name returns the command name.
func (c *CaptureCommand) Name() string { return "capture" }

// Description returns the command description.
func (c *CaptureCommand) Description() string { return "Capture event from stdin (used by AI tools)" }

// Execute runs the capture command.
func (c *CaptureCommand) Execute(ctx *Context) error {
	// Parse flags first — bulk mode resolves its own config from --workdir
	// and short-circuits the cwd-walking loadCaptureConfig() path used by
	// the AI-tool-integration flows below.
	source := "claude" // default
	captureType := ""
	content := ""
	bulk := false
	workdir := ""

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch {
		case arg == "--source" && i+1 < len(ctx.Args):
			source = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--type="):
			captureType = strings.TrimPrefix(arg, "--type=")
		case arg == "--type" && i+1 < len(ctx.Args):
			captureType = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--content="):
			content = strings.TrimPrefix(arg, "--content=")
		case arg == "--content" && i+1 < len(ctx.Args):
			content = ctx.Args[i+1]
			i++
		case arg == "--bulk":
			bulk = true
		case arg == "--workdir" && i+1 < len(ctx.Args):
			workdir = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--workdir="):
			workdir = strings.TrimPrefix(arg, "--workdir=")
		}
	}

	if bulk {
		return executeBulkCapture(workdir, os.Stdin)
	}

	cfg := ctx.Config
	if cfg == nil {
		var err error
		cfg, err = loadCaptureConfig()
		if err != nil {
			// Silent failure for capture
			os.Exit(0)
		}
	}

	// If --type and --content are provided, create event directly from CLI
	if captureType != "" && content != "" {
		event := &events.Event{
			Source:    events.SourceClaude,
			EventType: events.EventToolUse,
			Timestamp: time.Now(),
			ToolName:  "Capture",
			ToolInput: map[string]interface{}{
				"type":    captureType,
				"content": content,
			},
			ToolResult: content,
			Context: events.EventContext{
				ProjectPath: cfg.ProjectRoot,
			},
			Metadata: map[string]interface{}{
				"capture_type": captureType,
				"source":       "cli",
			},
		}

		cap := capture.New(cfg)
		if err := cap.CaptureEvent(event); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to capture: %v\n", err)
			os.Exit(1)
		}
		// Slice B2: corrections also emit a feedback.correction journal
		// entry so the projection (B3) can mark related derivations as
		// superseded. GradedID is empty for free-form corrections;
		// downstream consumers match on Note text.
		if captureType == "correction" {
			emitCorrection(cfg, content)
		}
		fmt.Printf("Captured %s: %s\n", captureType, truncateString(content, 60))
		os.Exit(0)
	}

	// Read stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		os.Exit(0)
	}

	var event *events.Event

	// Convert based on source
	switch source {
	case "claude":
		event, err = claude.ConvertToEvent(data, cfg.ProjectRoot)
	case "cursor":
		event, err = cursor.ConvertToEvent(data, cfg.ProjectRoot)
	default:
		// Try Claude format as fallback
		event, err = claude.ConvertToEvent(data, cfg.ProjectRoot)
	}

	if err != nil {
		// Try direct capture as fallback
		cap := capture.New(cfg)
		_ = cap.CaptureFromStdin()
		os.Exit(0)
	}

	// Capture the converted event
	cap := capture.New(cfg)
	_ = cap.CaptureEvent(event)

	os.Exit(0)
	return nil
}

// Name returns the command name.
func (c *IngestCommand) Name() string { return "ingest" }

// Description returns the command description.
func (c *IngestCommand) Description() string { return "Move queued events to database" }

// Execute runs the ingest command.
func (c *IngestCommand) Execute(ctx *Context) error {
	// --workdir short-circuits the dual-config (project-local queue,
	// global storage) resolution below: benchmarks call ingest against
	// a single isolated <workdir>/.cortex where queue and storage are
	// co-located.
	workdir := ""
	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		if arg == "--workdir" && i+1 < len(ctx.Args) {
			workdir = ctx.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--workdir=") {
			workdir = strings.TrimPrefix(arg, "--workdir=")
		}
	}

	cfg := ctx.Config
	store := ctx.Storage
	if workdir != "" {
		var err error
		cfg, store, err = openWorkdirContext(workdir)
		if err != nil {
			return err
		}
		defer store.Close()
	} else if cfg == nil || store == nil {
		var err error
		// Use capture config for queue path (project-local)
		captureCfg, captureErr := loadCaptureConfig()
		if captureErr != nil {
			return fmt.Errorf("failed to load config: %w", captureErr)
		}
		// Use storage config for storage (global)
		cfg, err = loadStorageConfig()
		if err != nil {
			return fmt.Errorf("failed to load storage config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()

		// Use project-local queue, not global
		cfg.ContextDir = captureCfg.ContextDir
	}

	// Drain the journal: project capture.event entries past the cursor
	// into SQLite. Replaces the pre-journal queue.ProcessPending path.
	var procOpts []processor.Option
	if opt, cleanup, err := openEvalProjector(); err == nil {
		procOpts = append(procOpts, opt)
		defer cleanup()
	}
	proc := processor.New(cfg, store, procOpts...)
	processed, err := proc.RunBatch()
	if err != nil {
		return fmt.Errorf("failed to drain journal: %w", err)
	}

	fmt.Printf("Ingested %d events to database\n", processed)

	// Generate embeddings if vector search is enabled
	if cfg.EnableVector && processed > 0 {
		ollamaClient := llm.NewOllamaClient(cfg)
		hugotEmbedder := llm.NewHugotEmbedder()
		embedder := llm.NewFallbackEmbedder(ollamaClient, hugotEmbedder)
		if embedder.IsEmbeddingAvailable() {
			bgCtx := context.Background()
			events, _ := store.GetRecentEvents(processed)
			embedded := 0
			for _, event := range events {
				if event.ToolResult != "" {
					vec, err := embedder.Embed(bgCtx, event.ToolResult)
					if err == nil {
						store.StoreEmbedding(event.ID, "event", vec)
						embedded++
					}
				}
			}
			if embedded > 0 {
				fmt.Printf("Generated %d embeddings\n", embedded)
			}
		}
	}

	return nil
}

// Name returns the command name.
func (c *AnalyzeCommand) Name() string { return "analyze" }

// Description returns the command description.
func (c *AnalyzeCommand) Description() string { return "Run LLM analysis on recent events" }

// Execute runs the analyze command.
func (c *AnalyzeCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Load config and storage if not provided
	if cfg == nil || store == nil {
		var err error
		cfg, err = loadStorageConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()
	}

	// Get limit from args (default: 10)
	limit := 10
	if len(ctx.Args) >= 1 {
		fmt.Sscanf(ctx.Args[0], "%d", &limit)
	}

	// Get recent events
	recentEvents, err := store.GetRecentEvents(limit)
	if err != nil {
		return fmt.Errorf("failed to get recent events: %w", err)
	}

	if len(recentEvents) == 0 {
		fmt.Println("No events to analyze")
		return nil
	}

	fmt.Printf("Analyzing %d events with LLM...\n", len(recentEvents))

	// Use LLM directly for analysis (cognition modes handle this normally)
	var llmProvider llm.Provider
	if p, _, err := llm.NewLLMClient(cfg); err == nil {
		llmProvider = p
	} else if ollama := llm.NewOllamaClient(cfg); ollama.IsAvailable() {
		llmProvider = ollama
	}

	if llmProvider == nil {
		fmt.Println("No LLM available (check Ollama or ANTHROPIC_API_KEY)")
		return nil
	}

	// Analyze events and store insights
	analyzed := 0
	for _, event := range recentEvents {
		if err := AnalyzeEventWithLLM(event, store, llmProvider); err == nil {
			analyzed++
		}
	}

	if analyzed > 0 {
		fmt.Printf("Analyzed %d events\n", analyzed)
	} else {
		fmt.Println("No events were analyzed")
	}

	return nil
}

// Name returns the command name.
func (c *ProcessCommand) Name() string { return "process" }

// Description returns the command description.
func (c *ProcessCommand) Description() string { return "Process queue + analyze (backward compat)" }

// Execute runs the process command (ingest + analyze).
func (c *ProcessCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Load config and storage if not provided
	if cfg == nil || store == nil {
		var err error
		captureCfg, captureErr := loadCaptureConfig()
		if captureErr != nil {
			return fmt.Errorf("failed to load config: %w", captureErr)
		}
		cfg, err = loadStorageConfig()
		if err != nil {
			return fmt.Errorf("failed to load storage config: %w", err)
		}

		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()

		cfg.ContextDir = captureCfg.ContextDir
	}

	// Drain the journal: project capture.event entries past the cursor.
	var procOpts []processor.Option
	if opt, cleanup, err := openEvalProjector(); err == nil {
		procOpts = append(procOpts, opt)
		defer cleanup()
	}
	proc := processor.New(cfg, store, procOpts...)
	processed, err := proc.RunBatch()
	if err != nil {
		return fmt.Errorf("failed to drain journal: %w", err)
	}

	fmt.Printf("Processed %d events\n", processed)

	// If events were processed, run analysis immediately
	if processed > 0 {
		// Get LLM provider via the unified surface (OpenRouter → Anthropic).
		var llmProvider llm.Provider
		if p, _, err := llm.NewLLMClient(cfg); err == nil {
			llmProvider = p
		} else if ollama := llm.NewOllamaClient(cfg); ollama.IsAvailable() {
			llmProvider = ollama
		}

		if llmProvider == nil {
			fmt.Println("No LLM available for analysis")
			return nil
		}

		// Analyze recent events
		recentEvents, err := store.GetRecentEvents(processed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to get recent events: %v\n", err)
			return nil
		}

		fmt.Printf("Analyzing %d events with LLM...\n", len(recentEvents))

		// Run analysis synchronously for immediate results
		analyzed := 0
		for _, event := range recentEvents {
			if err := AnalyzeEventWithLLM(event, store, llmProvider); err == nil {
				analyzed++
			}
		}

		if analyzed > 0 {
			fmt.Printf("Analyzed %d events\n", analyzed)
		}
	}

	return nil
}

// Name returns the command name.
func (c *FeedCommand) Name() string { return "feed" }

// Description returns the command description.
func (c *FeedCommand) Description() string { return "Seed knowledge from files or directories" }

// Execute runs the feed command.
func (c *FeedCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	if cfg == nil || store == nil {
		var err error
		cfg, err = loadStorageConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		store, err = storage.New(cfg)
		if err != nil {
			return fmt.Errorf("failed to open storage: %w", err)
		}
		defer store.Close()
	}

	// Parse args
	raw := false
	var paths []string
	dir := ""
	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch {
		case arg == "--raw":
			raw = true
		case arg == "--dir" && i+1 < len(ctx.Args):
			dir = ctx.Args[i+1]
			i++
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		default:
			paths = append(paths, arg)
		}
	}

	// Collect files from --dir
	if dir != "" {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if isFeedableFile(path) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to walk directory %s: %w", dir, err)
		}
	}

	if len(paths) == 0 {
		fmt.Println("Usage: cortex feed <file> [<file>...] [--dir <dir>] [--raw]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --raw       Store file content directly without LLM analysis")
		fmt.Println("  --dir DIR   Recursively process files in directory")
		return nil
	}

	// Get LLM provider (only needed for non-raw mode). Unified surface
	// first, Ollama as a local fallback.
	var llmProvider llm.Provider
	if !raw {
		if p, _, err := llm.NewLLMClient(cfg); err == nil {
			llmProvider = p
		} else if ollama := llm.NewOllamaClient(cfg); ollama.IsAvailable() {
			llmProvider = ollama
		}
		if llmProvider == nil {
			fmt.Println("No LLM available. Use --raw to store without analysis, or set OPEN_ROUTER_API_KEY / ANTHROPIC_API_KEY / start Ollama.")
			return nil
		}
	}

	fmt.Printf("Feeding %d file(s)...\n", len(paths))

	fed := 0
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Skip %s: %v\n", path, err)
			continue
		}

		if len(content) == 0 {
			continue
		}

		if raw {
			if err := feedRaw(cfg, path, string(content)); err != nil {
				fmt.Fprintf(os.Stderr, "  Error %s: %v\n", path, err)
				continue
			}
		} else {
			if err := feedWithLLM(cfg, store, llmProvider, path, string(content)); err != nil {
				fmt.Fprintf(os.Stderr, "  Error %s: %v\n", path, err)
				continue
			}
		}
		fed++
		fmt.Printf("  Fed: %s\n", path)
	}

	fmt.Printf("Done. %d/%d files processed.\n", fed, len(paths))
	return nil
}

// feedRaw stores file content directly as a knowledge file without LLM processing.
func feedRaw(cfg *config.Config, filePath, content string) error {
	category := "insights"
	knowledgeDir := cfg.KnowledgePath(category)
	if err := os.MkdirAll(knowledgeDir, 0755); err != nil {
		return fmt.Errorf("failed to create knowledge dir: %w", err)
	}

	slug := feedSlugify(filepath.Base(filePath))
	outPath := filepath.Join(knowledgeDir, slug+".md")

	fileContent := fmt.Sprintf("---\ncategory: %s\nsource: %s\ncreated: %s\n---\n\n%s\n",
		category,
		filePath,
		time.Now().Format(time.RFC3339),
		content,
	)

	return os.WriteFile(outPath, []byte(fileContent), 0644)
}

// feedWithLLM processes file content through LLM to extract insights.
func feedWithLLM(cfg *config.Config, store *storage.Storage, provider llm.Provider, filePath, content string) error {
	// Truncate large files
	if len(content) > 8000 {
		content = content[:8000] + "\n... (truncated)"
	}

	prompt := fmt.Sprintf(`Analyze this document for durable knowledge — decisions, patterns, constraints, and domain context.

File: %s

Content:
%s

Extract insights as a JSON array. Each insight should be:
{
  "category": "decision|pattern|constraint|insight|strategy",
  "summary": "1-3 sentence insight",
  "importance": 1-10,
  "tags": ["tag1", "tag2"]
}

If nothing significant, respond: NO_INSIGHT`, filePath, content)

	response, err := provider.GenerateWithSystem(context.Background(), prompt, llm.AnalysisSystemPrompt)
	if err != nil {
		return err
	}

	if strings.Contains(strings.ToUpper(response), "NO_INSIGHT") {
		return nil
	}

	// Parse response as JSON array of insights
	var insights []struct {
		Category   string   `json:"category"`
		Summary    string   `json:"summary"`
		Importance int      `json:"importance"`
		Tags       []string `json:"tags"`
	}

	// Try to extract JSON from response
	cleaned := extractJSON(response)
	if err := json.Unmarshal([]byte(cleaned), &insights); err != nil {
		// Try single object
		var single struct {
			Category   string   `json:"category"`
			Summary    string   `json:"summary"`
			Importance int      `json:"importance"`
			Tags       []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(cleaned), &single); err != nil {
			return fmt.Errorf("failed to parse LLM response: %w", err)
		}
		insights = append(insights, single)
	}

	// Store insights in DB and write to knowledge files
	for _, ins := range insights {
		if ins.Summary == "" {
			continue
		}

		store.StoreInsight("", ins.Category, ins.Summary, ins.Importance, ins.Tags, "")

		// Write to knowledge file
		category := ins.Category
		if category == "" {
			category = "insights"
		}
		knowledgeDir := cfg.KnowledgePath(category)
		os.MkdirAll(knowledgeDir, 0755)

		slug := feedSlugify(ins.Summary)
		outPath := filepath.Join(knowledgeDir, slug+".md")

		tagsStr := ""
		if len(ins.Tags) > 0 {
			tagsStr = fmt.Sprintf("tags: [%s]\n", strings.Join(ins.Tags, ", "))
		}

		fileContent := fmt.Sprintf("---\ncategory: %s\nimportance: %d\n%ssource: %s\ncreated: %s\n---\n\n%s\n",
			category,
			ins.Importance,
			tagsStr,
			filePath,
			time.Now().Format(time.RFC3339),
			ins.Summary,
		)

		os.WriteFile(outPath, []byte(fileContent), 0644)
	}

	return nil
}

// extractJSON tries to extract a JSON array or object from LLM response text.
func extractJSON(s string) string {
	// Find first [ or {
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		return s
	}

	opener := s[start]
	var closer byte = '}'
	if opener == '[' {
		closer = ']'
	}

	// Find matching closer
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}

	return s[start:]
}

// isFeedableFile returns true if the file extension is suitable for feeding.
func isFeedableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	feedable := map[string]bool{
		".md": true, ".txt": true, ".rst": true,
		".go": true, ".py": true, ".js": true, ".ts": true,
		".yaml": true, ".yml": true, ".json": true, ".toml": true,
		".sh": true, ".bash": true,
		".sql": true, ".graphql": true,
		".html": true, ".css": true,
		".java": true, ".rs": true, ".rb": true, ".php": true,
		".c": true, ".cpp": true, ".h": true,
		".tf": true, ".dockerfile": true,
	}
	if feedable[ext] {
		return true
	}
	// Also check for extensionless files like Makefile, Dockerfile
	base := strings.ToLower(filepath.Base(path))
	return base == "makefile" || base == "dockerfile" || base == "readme"
}

// feedSlugify converts text to a filesystem-safe slug for feed output.
func feedSlugify(text string) string {
	s := strings.ToLower(text)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		if idx := strings.LastIndex(s, "-"); idx > 30 {
			s = s[:idx]
		}
	}
	if s == "" {
		s = "feed"
	}
	return s
}

// --- Helper functions ---

// loadCaptureConfig loads config for capture, finding the project root by
// walking up from cwd. The ContextDir stays project-local (for queue writes),
// while GlobalDir points to ~/.cortex/ (for storage reads).
func loadCaptureConfig() (*config.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// Walk up from cwd looking for .cortex/config.json
	projectRoot := findProjectRoot(cwd)
	if projectRoot == "" {
		return nil, fmt.Errorf("no .cortex/ found in %s or parents", cwd)
	}

	configPath := filepath.Join(projectRoot, ".cortex", "config.json")
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	// Ensure ContextDir points to project-local .cortex/ (for queue writes)
	cfg.ContextDir = filepath.Join(projectRoot, ".cortex")
	cfg.ProjectRoot = projectRoot

	return cfg, nil
}

// loadStorageConfig returns a config suitable for opening global storage.
// Uses loadCaptureConfig for project context, then overrides ContextDir to ~/.cortex/.
func loadStorageConfig() (*config.Config, error) {
	cfg, err := loadCaptureConfig()
	if err != nil {
		return nil, err
	}
	cfg.ContextDir = projreg.GlobalDir()
	cfg.GlobalDir = projreg.GlobalDir()
	return cfg, nil
}

// findProjectRoot walks up from dir looking for a .cortex/ directory.
// Returns the project root path, or empty string if not found.
func findProjectRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".cortex")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root
		}
		dir = parent
	}
}

// executeBulkCapture reads NDJSON from r — one events.Event per line —
// and writes each into the journal at <workdir>/.cortex/. Used by
// benchmarks that need to hydrate hundreds of events per instance
// without paying fork+exec overhead per event.
//
// Workdir is required and resolved to an absolute path. .cortex/ is
// created if missing (the journal subsystem MkdirAlls its segment
// directories). Events with a zero Timestamp are stamped with time.Now()
// so callers can omit it.
//
// Returns the count printed to stdout on full success. The first parse
// or capture error aborts the batch and returns a wrapped error naming
// the offending line number (1-indexed). Partial state is left in the
// journal — callers that need atomicity should write to a fresh workdir.
func executeBulkCapture(workdir string, r io.Reader) error {
	if strings.TrimSpace(workdir) == "" {
		return fmt.Errorf("--bulk requires --workdir")
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return fmt.Errorf("--workdir: %w", err)
	}
	cfg := &config.Config{
		ContextDir:  filepath.Join(abs, ".cortex"),
		ProjectRoot: abs,
	}
	cap := capture.New(cfg)

	scanner := bufio.NewScanner(r)
	// Default 64KB line cap is too small for tool_result fields that
	// carry multi-KB chunks (e.g. NIAH haystack chunks up to chunkSize).
	// 16MB max line accommodates worst-case captures without OOM risk.
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)

	count := 0
	line := 0
	for scanner.Scan() {
		line++
		b := scanner.Bytes()
		if len(b) == 0 {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			return fmt.Errorf("line %d: parse: %w", line, err)
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now()
		}
		if err := cap.CaptureEvent(&ev); err != nil {
			return fmt.Errorf("line %d: capture: %w", line, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	fmt.Printf("Captured %d events\n", count)
	return nil
}
