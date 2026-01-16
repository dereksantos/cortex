// Package commands provides CLI command implementations.
package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/cognition/sources"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/queue"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// DaemonCommand implements the daemon background processor.
type DaemonCommand struct{}

func init() {
	Register(&DaemonCommand{})
}

// Name returns the command name.
func (c *DaemonCommand) Name() string { return "daemon" }

// Description returns the command description.
func (c *DaemonCommand) Description() string { return "Run background context processor" }

// Execute runs the daemon command.
func (c *DaemonCommand) Execute(ctx *Context) error {
	cfg := ctx.Config
	store := ctx.Storage

	// Create queue manager
	queueMgr := queue.New(cfg, store)

	// Create and start processor
	proc := processor.New(cfg, store, queueMgr)
	if err := proc.Start(); err != nil {
		return fmt.Errorf("failed to start processor: %w", err)
	}

	// Initialize LLM provider for cognitive modes
	var llmProvider llm.Provider
	anthropic := llm.NewAnthropicClient(cfg)
	ollama := llm.NewOllamaClient(cfg)
	if anthropic.IsAvailable() {
		llmProvider = anthropic
	} else if ollama.IsAvailable() {
		llmProvider = ollama
	}

	// Initialize embedder with fallback: Ollama (768-dim) -> Hugot (384-dim)
	// Note: Different dimensions are handled by storage, but using a single
	// embedder consistently per database is recommended for best results.
	hugotEmbedder := llm.NewHugotEmbedder()
	embedder := llm.NewFallbackEmbedder(ollama, hugotEmbedder)

	// Create Cortex cognitive pipeline
	cortex, err := intcognition.New(store, llmProvider, embedder, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not initialize cognitive pipeline: %v\n", err)
		// Continue without cognitive features
	}

	// Create state writer for real-time cognitive mode status
	stateWriter := intcognition.NewStateWriter(cfg.ContextDir)
	if cortex != nil {
		cortex.SetStateWriter(stateWriter)

		// Route events through cognition pipeline when processor handles them
		proc.SetEventCallback(func(evts []*events.Event) {
			cortex.IngestBatch(context.Background(), evts)
		})

		// Register dream sources for background exploration
		cortex.RegisterSource(sources.NewProjectSource(cfg.ProjectRoot))
		cortex.RegisterSource(sources.NewCortexSource(store))

		// Register Claude history source
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
			cortex.RegisterSource(sources.NewClaudeHistorySource(claudeProjectsDir))
		}

		// Register transcript queue source (from Stop hooks)
		transcriptQueueDir := filepath.Join(cfg.ContextDir, "transcript_queue")
		cortex.RegisterSource(sources.NewTranscriptQueueSource(transcriptQueueDir))

		// Register git source for commit history exploration
		cortex.RegisterSource(sources.NewGitSource(cfg.ProjectRoot))
	}

	// Load persisted session
	sessionPersister := intcognition.NewSessionPersister(cfg.ContextDir)
	persistedSession, err := sessionPersister.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Could not load session: %v\n", err)
	} else if cortex != nil {
		// Restore session state to Think's SessionContext
		sessionCtx := cortex.SessionContext()
		if persistedSession != nil && sessionCtx != nil {
			sessionCtx.TopicWeights = persistedSession.TopicWeights
			sessionCtx.WarmCache = persistedSession.WarmCache
			sessionCtx.ResolvedContradictions = persistedSession.ResolvedContradictions
			sessionCtx.LastUpdated = persistedSession.LastUpdated
			fmt.Println("   Restored session state from previous run")
		}
	}

	// Create session saver for periodic saves
	sessionSaver := intcognition.NewSessionSaver(sessionPersister, 30*time.Second)

	fmt.Println("Cortex daemon started")
	fmt.Println("   Processing events every 5 seconds...")
	fmt.Println("   Session persisted every 30 seconds...")
	fmt.Println("   Status updates every 2 seconds...")
	fmt.Println("   Cognitive modes check every 10 seconds...")
	fmt.Println("   Press Ctrl+C to stop")

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Periodic session save ticker
	saveTicker := time.NewTicker(30 * time.Second)
	defer saveTicker.Stop()

	// Periodic state update ticker for stats
	stateTicker := time.NewTicker(2 * time.Second)
	defer stateTicker.Stop()

	// Periodic cognitive mode ticker (Dream when idle, Think when active)
	cognitiveTicker := time.NewTicker(10 * time.Second)
	defer cognitiveTicker.Stop()

	// Idle threshold for Dream triggering (30 seconds without events)
	idleThreshold := 30 * time.Second

	// Write initial state
	updateDaemonStats(store, stateWriter)

	// Main daemon loop
	done := false
	for !done {
		select {
		case <-stateTicker.C:
			// Periodic state update with current stats
			updateDaemonStats(store, stateWriter)
		case <-saveTicker.C:
			// Periodic session save
			if cortex != nil {
				sessionSaver.MarkDirty()
				if sessionSaver.MaybeSave(cortex.SessionContext()) {
					// Silent save - no output needed
				}
			}
		case <-cognitiveTicker.C:
			// Trigger cognitive modes based on activity
			if cortex != nil {
				activityLogger := intcognition.NewActivityLogger(cfg.ContextDir)
				if isUserIdle(store, idleThreshold) {
					// Idle - run Dream for background exploration
					go func() {
						result, err := cortex.MaybeDream(context.Background())
						if err == nil && result != nil && result.Status == cognition.DreamRan {
							activityLogger.Log(&intcognition.ActivityLogEntry{
								Mode:        "dream",
								Description: fmt.Sprintf("explored %d items, %d insights", result.Operations, result.Insights),
								LatencyMs:   result.Duration.Milliseconds(),
							})
						}
					}()
				} else {
					// Active - run Think for session pattern learning
					go func() {
						result, err := cortex.MaybeThink(context.Background())
						if err == nil && result != nil && result.Status == cognition.ThinkRan {
							activityLogger.Log(&intcognition.ActivityLogEntry{
								Mode:        "think",
								Description: fmt.Sprintf("processed %d operations", result.Operations),
								LatencyMs:   result.Duration.Milliseconds(),
							})
						}
					}()
				}
			}
		case <-sigChan:
			done = true
		}
	}

	fmt.Println("\nStopping daemon...")

	// Save session on graceful shutdown
	if cortex != nil {
		if err := sessionSaver.ForceSave(cortex.SessionContext()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Could not save session: %v\n", err)
		} else {
			fmt.Println("   Session state saved")
		}
	}

	// Clean up state file
	stateWriter.WriteMode("idle", "")

	proc.Stop()
	fmt.Println("Daemon stopped")

	return nil
}

// updateDaemonStats updates the daemon state file with current stats.
// Only writes idle state if no cognitive mode is currently active.
func updateDaemonStats(store *storage.Storage, stateWriter *intcognition.StateWriter) {
	if store == nil || stateWriter == nil {
		return
	}

	// Check if a cognitive mode is currently active - don't overwrite it
	currentState, _ := intcognition.ReadDaemonState(stateWriter.Path())
	if currentState != nil && currentState.Mode != "" && currentState.Mode != "idle" {
		// A cognitive mode is active and fresh - don't overwrite with idle
		// Just update stats in-place by re-writing with same mode
		stats, err := store.GetStats()
		if err != nil {
			return
		}
		totalEvents := 0
		if val, ok := stats["total_events"].(int); ok {
			totalEvents = val
		}
		totalInsights := 0
		if val, ok := stats["total_insights"].(int); ok {
			totalInsights = val
		}
		stateWriter.WriteModeWithStats(currentState.Mode, currentState.Description, totalEvents, totalInsights)
		return
	}

	stats, err := store.GetStats()
	if err != nil {
		return
	}

	totalEvents := 0
	if val, ok := stats["total_events"].(int); ok {
		totalEvents = val
	}

	totalInsights := 0
	if val, ok := stats["total_insights"].(int); ok {
		totalInsights = val
	}

	// Write idle state with stats (no cognitive mode is active)
	stateWriter.WriteModeWithStats("idle", "", totalEvents, totalInsights)
}

// isUserIdle checks if the user has been idle based on recent captured events.
// Returns true if no events in the last idleThreshold duration.
func isUserIdle(store *storage.Storage, idleThreshold time.Duration) bool {
	if store == nil {
		return true
	}

	recentEvents, err := store.GetRecentEvents(1)
	if err != nil || len(recentEvents) == 0 {
		return true // No events = idle
	}

	timeSince := time.Since(recentEvents[0].Timestamp)
	return timeSince > idleThreshold
}

// RunDaemon provides a direct entry point for running the daemon.
// This is used by main.go when storage is already opened.
func RunDaemon(cfg *config.Config, store *storage.Storage) error {
	cmd := &DaemonCommand{}
	return cmd.Execute(&Context{
		Config:  cfg,
		Storage: store,
		Args:    []string{},
	})
}
