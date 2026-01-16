// Package commands provides CLI command implementations.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/internal/tui"
	"github.com/dereksantos/cortex/pkg/config"
)

// WatchCommand implements the watch functionality.
type WatchCommand struct{}

func init() {
	Register(&WatchCommand{})
}

// Name returns the command name.
func (c *WatchCommand) Name() string { return "watch" }

// Description returns the command description.
func (c *WatchCommand) Description() string { return "Watch cognitive activity in real-time" }

// Execute runs the watch command.
func (c *WatchCommand) Execute(ctx *Context) error {
	// Parse flags from ctx.Args
	jsonOutput := false
	noAnimate := false
	retrievalOnly := false
	backgroundOnly := false
	interactive := true // Default to interactive if TTY

	for _, arg := range ctx.Args {
		switch arg {
		case "--json":
			jsonOutput = true
			interactive = false
		case "--no-animate":
			noAnimate = true
			interactive = false
		case "--retrieval-only":
			retrievalOnly = true
		case "--background-only":
			backgroundOnly = true
		case "--no-interactive":
			interactive = false
		case "-h", "--help":
			fmt.Println("Usage: cortex watch [flags]")
			fmt.Println("\nFlags:")
			fmt.Println("  --json             Machine-readable JSON output")
			fmt.Println("  --no-animate       Static output (single snapshot)")
			fmt.Println("  --no-interactive   Disable keyboard interaction")
			fmt.Println("  --retrieval-only   Show only retrieval stats")
			fmt.Println("  --background-only  Show only background (daemon) stats")
			fmt.Println("\nInteractive controls:")
			fmt.Println("  Up/Down arrows     Navigate sessions")
			fmt.Println("  Enter              Expand/collapse session details")
			fmt.Println("  q or Ctrl+C        Quit")
			return nil
		}
	}

	cfg := ctx.Config
	store := ctx.Storage

	// If JSON output, print once and exit
	if jsonOutput {
		printWatchJSON(cfg, store)
		return nil
	}

	// If no-animate, print once and exit
	if noAnimate {
		printWatchStatic(cfg, store, retrievalOnly, backgroundOnly)
		return nil
	}

	// Check if we're in a TTY for interactive mode
	if interactive && !term.IsTerminal(int(os.Stdin.Fd())) {
		interactive = false
	}

	if interactive {
		runInteractiveWatch(cfg, store, retrievalOnly, backgroundOnly)
	} else {
		runAnimatedWatch(cfg, store, retrievalOnly, backgroundOnly)
	}

	return nil
}

// watchState holds the interactive watch UI state.
type watchState struct {
	selectedSession int // Index of selected session (0-based)
	expandedSession int // Index of expanded session (-1 = none)
	sessions        []*storage.SessionMetadata
}

// runAnimatedWatch runs the non-interactive animated watch.
func runAnimatedWatch(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool) {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Animation ticker (refresh every 300ms)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	// Animation state
	animFrame := 0

	// Clear screen and hide cursor
	fmt.Print(tui.ClearAndHome() + tui.CursorHide)
	defer fmt.Print(tui.CursorShow) // Show cursor on exit

	// Initial render
	printWatchAnimated(cfg, store, retrievalOnly, backgroundOnly, animFrame)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Move cursor to top and redraw
			fmt.Print(tui.CursorHome)
			printWatchAnimated(cfg, store, retrievalOnly, backgroundOnly, animFrame)
		case <-sigChan:
			fmt.Print(tui.CursorShow) // Show cursor
			fmt.Println("\n\nStopped watching.")
			return
		}
	}
}

// runInteractiveWatch runs the interactive watch with keyboard input.
func runInteractiveWatch(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool) {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Put terminal in raw mode for keyboard input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Fall back to non-interactive mode
		runAnimatedWatch(cfg, store, retrievalOnly, backgroundOnly)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Animation ticker (refresh every 300ms)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	// Keyboard input channel
	keyChan := make(chan byte, 10)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			keyChan <- buf[0]
		}
	}()

	// Watch state
	state := &watchState{
		selectedSession: 0,
		expandedSession: -1,
	}

	// Animation state
	animFrame := 0

	// Clear screen and hide cursor
	fmt.Print(tui.ClearAndHome() + tui.CursorHide)
	defer fmt.Print(tui.CursorShow + tui.ClearAndHome()) // Show cursor and clear on exit

	// Initial data load
	state.sessions, _ = store.GetRecentSessions(3)

	// Initial render
	printInteractiveWatch(cfg, store, state, retrievalOnly, backgroundOnly, animFrame)

	// Escape sequence state
	escapeSeq := make([]byte, 0, 3)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Refresh session data periodically
			if animFrame%10 == 0 { // Every ~3 seconds
				state.sessions, _ = store.GetRecentSessions(3)
			}
			// Move cursor to top and redraw
			fmt.Print(tui.CursorHome)
			printInteractiveWatch(cfg, store, state, retrievalOnly, backgroundOnly, animFrame)

		case key := <-keyChan:
			// Handle escape sequences (arrow keys)
			if len(escapeSeq) > 0 {
				escapeSeq = append(escapeSeq, key)
				if len(escapeSeq) == 3 {
					// Process escape sequence
					if escapeSeq[0] == 27 && escapeSeq[1] == 91 {
						switch escapeSeq[2] {
						case 65: // Up arrow
							if state.expandedSession == -1 && state.selectedSession > 0 {
								state.selectedSession--
							}
						case 66: // Down arrow
							if state.expandedSession == -1 && state.selectedSession < len(state.sessions)-1 {
								state.selectedSession++
							}
						}
					}
					escapeSeq = escapeSeq[:0]
					// Redraw after key
					fmt.Print(tui.CursorHome)
					printInteractiveWatch(cfg, store, state, retrievalOnly, backgroundOnly, animFrame)
				}
				continue
			}

			switch key {
			case 27: // Escape - start escape sequence or collapse
				if state.expandedSession != -1 {
					state.expandedSession = -1
					fmt.Print(tui.CursorHome)
					printInteractiveWatch(cfg, store, state, retrievalOnly, backgroundOnly, animFrame)
				} else {
					escapeSeq = append(escapeSeq, key)
				}
			case 13: // Enter - toggle expand
				if state.expandedSession == state.selectedSession {
					state.expandedSession = -1
				} else if len(state.sessions) > 0 && state.selectedSession < len(state.sessions) {
					state.expandedSession = state.selectedSession
				}
				fmt.Print(tui.ClearAndHome()) // Clear and redraw for expansion
				printInteractiveWatch(cfg, store, state, retrievalOnly, backgroundOnly, animFrame)
			case 'q', 3: // q or Ctrl+C
				return
			}

		case <-sigChan:
			return
		}
	}
}

// printWatchJSON outputs all watch data as JSON.
func printWatchJSON(cfg *config.Config, store *storage.Storage) {
	type WatchOutput struct {
		DaemonState    *intcognition.DaemonState      `json:"daemon_state,omitempty"`
		RetrievalStats *intcognition.RetrievalStats   `json:"retrieval_stats,omitempty"`
		RecentActivity []intcognition.ActivityLogEntry `json:"recent_activity,omitempty"`
		Stats          map[string]interface{}         `json:"stats,omitempty"`
	}

	output := WatchOutput{}

	// Get daemon state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	output.DaemonState, _ = intcognition.ReadDaemonState(statePath)

	// Get retrieval stats
	output.RetrievalStats, _ = intcognition.ReadRetrievalStats(cfg.ContextDir)

	// Get recent activity
	output.RecentActivity, _ = intcognition.ReadRecentActivity(cfg.ContextDir, 10)

	// Get storage stats
	output.Stats, _ = store.GetStats()

	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
}

// printWatchStatic outputs a single snapshot without animation.
func printWatchStatic(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool) {
	// Get all state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)
	retrievalStats, _ := intcognition.ReadRetrievalStats(cfg.ContextDir)
	recentActivity, _ := intcognition.ReadRecentActivity(cfg.ContextDir, 5)
	stats, _ := store.GetStats()

	// Session data
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	var topicWeights map[string]float64
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(sessionData, &session) == nil {
			topicWeights = session.TopicWeights
		}
	}

	// Column widths for pipe-style table
	col1Width := 28 // Background column
	col2Width := 28 // Retrieval column

	// Get stats
	events := 0
	insights := 0
	if daemonState != nil {
		events = daemonState.Stats.Events
		insights = daemonState.Stats.Insights
	}
	if statsEvents, ok := stats["total_events"].(int); ok && statsEvents > events {
		events = statsEvents
	}
	if statsInsights, ok := stats["total_insights"].(int); ok && statsInsights > insights {
		insights = statsInsights
	}

	// Mode status
	modeIcon := "○"
	modeName := "IDLE"
	modeDesc := ""
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		modeIcon = getModeSpinner(daemonState.Mode)
		modeName = strings.ToUpper(daemonState.Mode)
		modeDesc = daemonState.Description
		if modeDesc == "" {
			modeDesc = getDefaultModeDescription(daemonState.Mode)
		}
	}

	// Print mode header
	fmt.Printf("┌%s┐\n", strings.Repeat("─", col1Width+col2Width+3))
	modeStr := fmt.Sprintf(" %s %s", modeIcon, modeName)
	fmt.Printf("│%s│\n", tui.Pad(modeStr, col1Width+col2Width+3))
	if modeDesc != "" {
		descStr := fmt.Sprintf(" %s", tui.Truncate(modeDesc, col1Width+col2Width+1))
		fmt.Printf("│%s│\n", tui.Pad(descStr, col1Width+col2Width+3))
	}

	// Two-column table
	if !retrievalOnly && !backgroundOnly {
		fmt.Printf("├%s┬%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
		fmt.Printf("│ %s │ %s │\n", tui.Pad("Background", col1Width-1), tui.Pad("Retrieval", col2Width-1))
		fmt.Printf("├%s┼%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))

		bgEvents := fmt.Sprintf("Events: %d", events)
		retQueries := ""
		if retrievalStats != nil {
			retQueries = fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(bgEvents, col1Width-1), tui.Pad(retQueries, col2Width-1))

		bgInsights := fmt.Sprintf("Insights: %d", insights)
		retLatency := ""
		if retrievalStats != nil && retrievalStats.LastMode != "" {
			modeTitle := strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
			retLatency = fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(bgInsights, col1Width-1), tui.Pad(retLatency, col2Width-1))

		topicsStr := ""
		if len(topicWeights) > 0 {
			topicList := make([]string, 0)
			for topic, weight := range topicWeights {
				if weight > 0.3 {
					topicList = append(topicList, fmt.Sprintf("%s(%.1f)", topic, weight))
				}
			}
			if len(topicList) > 0 {
				topicsStr = strings.Join(topicList[:min(2, len(topicList))], ", ")
			}
		}
		retResults := ""
		if retrievalStats != nil {
			retResults = fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(topicsStr, col1Width-1), tui.Pad(retResults, col2Width-1))

		fmt.Printf("└%s┴%s┘\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
	} else if backgroundOnly {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Events: %d  Insights: %d", events, insights), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else if retrievalOnly && retrievalStats != nil {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals), col1Width+col2Width+1))
		modeTitle := "N/A"
		if retrievalStats.LastMode != "" {
			modeTitle = strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
		}
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle), col1Width+col2Width+1))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else {
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	}

	// Recent activity table
	totalWidth := col1Width + col2Width + 3
	fmt.Println()
	fmt.Printf("┌%s┐\n", strings.Repeat("─", totalWidth))
	fmt.Printf("│ %s │\n", tui.Pad("Recent Activity", totalWidth-2))
	fmt.Printf("├%s┬%s┬%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
	fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad("Time", 8), tui.Pad("Mode", 6), tui.Pad("Description", totalWidth-23))
	fmt.Printf("├%s┼%s┼%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	if len(recentActivity) > 0 {
		for _, entry := range recentActivity {
			timeStr := entry.Timestamp.Format("15:04:05")
			entryModeIcon := getModeSpinner(entry.Mode)
			desc := tui.Truncate(entry.Description, totalWidth-25)
			fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad(timeStr, 8), tui.Pad(entryModeIcon, 6), tui.Pad(desc, totalWidth-23))
		}
	} else {
		fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad("--:--:--", 8), tui.Pad("○", 6), tui.Pad("No activity yet", totalWidth-23))
	}

	fmt.Printf("└%s┴%s┴%s┘\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
}

// printWatchAnimated outputs the animated watch display.
func printWatchAnimated(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool, frame int) {
	// Get all state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)
	retrievalStats, _ := intcognition.ReadRetrievalStats(cfg.ContextDir)
	recentActivity, _ := intcognition.ReadRecentActivity(cfg.ContextDir, 5)
	stats, _ := store.GetStats()

	// Session data
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	var topicWeights map[string]float64
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(sessionData, &session) == nil {
			topicWeights = session.TopicWeights
		}
	}

	// Column widths for pipe-style table
	col1Width := 28 // Background column
	col2Width := 28 // Retrieval column

	// Get stats
	events := 0
	insights := 0
	if daemonState != nil {
		events = daemonState.Stats.Events
		insights = daemonState.Stats.Insights
	}
	if statsEvents, ok := stats["total_events"].(int); ok && statsEvents > events {
		events = statsEvents
	}
	if statsInsights, ok := stats["total_insights"].(int); ok && statsInsights > insights {
		insights = statsInsights
	}

	// Mode status
	modeIcon := "○"
	modeName := "IDLE"
	modeDesc := ""
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		modeIcon = getAnimatedModeSpinner(daemonState.Mode, frame)
		modeName = strings.ToUpper(daemonState.Mode) + "ING"
		if daemonState.Mode == "dream" {
			modeName = "DREAMING"
		} else if daemonState.Mode == "think" {
			modeName = "THINKING"
		}
		modeDesc = daemonState.Description
		if modeDesc == "" {
			modeDesc = getDefaultModeDescription(daemonState.Mode)
		}
	}

	// Print mode header
	fmt.Printf("┌%s┐\n", strings.Repeat("─", col1Width+col2Width+3))
	modeStr := fmt.Sprintf(" %s %s", modeIcon, modeName)
	fmt.Printf("│%s│\n", tui.Pad(modeStr, col1Width+col2Width+3))
	if modeDesc != "" {
		descStr := fmt.Sprintf(" %s", tui.Truncate(modeDesc, col1Width+col2Width+1))
		fmt.Printf("│%s│\n", tui.Pad(descStr, col1Width+col2Width+3))
	}

	// Two-column table header
	if !retrievalOnly && !backgroundOnly {
		fmt.Printf("├%s┬%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
		fmt.Printf("│ %s │ %s │\n", tui.Pad("Background", col1Width-1), tui.Pad("Retrieval", col2Width-1))
		fmt.Printf("├%s┼%s┤\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))

		// Stats rows
		bgEvents := fmt.Sprintf("Events: %d", events)
		retQueries := ""
		if retrievalStats != nil {
			retQueries = fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(bgEvents, col1Width-1), tui.Pad(retQueries, col2Width-1))

		bgInsights := fmt.Sprintf("Insights: %d", insights)
		retLatency := ""
		if retrievalStats != nil && retrievalStats.LastMode != "" {
			modeTitle := strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
			retLatency = fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(bgInsights, col1Width-1), tui.Pad(retLatency, col2Width-1))

		// Topics
		topicsStr := ""
		if len(topicWeights) > 0 {
			topicList := make([]string, 0)
			for topic, weight := range topicWeights {
				if weight > 0.3 {
					topicList = append(topicList, fmt.Sprintf("%s(%.1f)", topic, weight))
				}
			}
			if len(topicList) > 0 {
				topicsStr = strings.Join(topicList[:min(2, len(topicList))], ", ")
			}
		}
		retResults := ""
		if retrievalStats != nil {
			retResults = fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision)
		}
		fmt.Printf("│ %s │ %s │\n", tui.Pad(topicsStr, col1Width-1), tui.Pad(retResults, col2Width-1))

		fmt.Printf("└%s┴%s┘\n", strings.Repeat("─", col1Width+1), strings.Repeat("─", col2Width+1))
	} else if backgroundOnly {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Events: %d  Insights: %d", events, insights), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else if retrievalOnly && retrievalStats != nil {
		fmt.Printf("├%s┤\n", strings.Repeat("─", col1Width+col2Width+3))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Queries: %d", retrievalStats.TotalRetrievals), col1Width+col2Width+1))
		modeTitle := "N/A"
		if retrievalStats.LastMode != "" {
			modeTitle = strings.ToUpper(retrievalStats.LastMode[:1]) + retrievalStats.LastMode[1:]
		}
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Last: %dms (%s)", retrievalStats.LastReflexMs, modeTitle), col1Width+col2Width+1))
		fmt.Printf("│ %s │\n", tui.Pad(fmt.Sprintf("Results: %d → %s", retrievalStats.LastResults, retrievalStats.LastDecision), col1Width+col2Width+1))
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	} else {
		fmt.Printf("└%s┘\n", strings.Repeat("─", col1Width+col2Width+3))
	}

	// Recent activity table
	totalWidth := col1Width + col2Width + 3
	fmt.Println()
	fmt.Printf("┌%s┐\n", strings.Repeat("─", totalWidth))
	fmt.Printf("│ %s │\n", tui.Pad("Recent Activity", totalWidth-2))
	fmt.Printf("├%s┬%s┬%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))
	fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad("Time", 8), tui.Pad("Mode", 6), tui.Pad("Description", totalWidth-23))
	fmt.Printf("├%s┼%s┼%s┤\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	if len(recentActivity) > 0 {
		for _, entry := range recentActivity {
			timeStr := entry.Timestamp.Format("15:04:05")
			entryModeIcon := getAnimatedModeSpinner(entry.Mode, frame)
			desc := tui.Truncate(entry.Description, totalWidth-25)
			fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad(timeStr, 8), tui.Pad(entryModeIcon, 6), tui.Pad(desc, totalWidth-23))
		}
	} else {
		fmt.Printf("│ %s │ %s │ %s │\n", tui.Pad("--:--:--", 8), tui.Pad("○", 6), tui.Pad("No activity yet. Start daemon or use Cortex.", totalWidth-23))
	}

	fmt.Printf("└%s┴%s┴%s┘\n", strings.Repeat("─", 10), strings.Repeat("─", 8), strings.Repeat("─", totalWidth-21))

	fmt.Printf("\nPress Ctrl+C to stop. Refreshing every 300ms...\n")
}

// getAnimatedModeSpinner returns an animated spinner for a cognitive mode.
func getAnimatedModeSpinner(mode string, frame int) string {
	switch mode {
	case "dream":
		// Breathing, organic
		spinners := []string{"○", "◔", "◑", "◕", "●", "◕", "◑", "◔"}
		return spinners[frame%len(spinners)]
	case "think":
		// Braille dots, subtle
		spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return spinners[frame%len(spinners)]
	case "reflect":
		// Arrow cycle
		spinners := []string{"→", "↘", "↓", "↙", "←", "↖", "↑", "↗"}
		return spinners[frame%len(spinners)]
	case "reflex":
		// Bouncing dot
		spinners := []string{"∙", "•", "●", "•", "∙"}
		return spinners[frame%len(spinners)]
	case "resolve":
		// Ellipsis
		spinners := []string{"·  ", "·· ", "···", "·· ", "·  ", "   "}
		return spinners[frame%len(spinners)]
	case "insight", "digest":
		// Special marker
		spinners := []string{"✦", "★", "✦", "☆"}
		return spinners[frame%len(spinners)]
	default:
		return "●"
	}
}

// printInteractiveWatch renders the interactive watch view with sessions panel.
func printInteractiveWatch(cfg *config.Config, store *storage.Storage, state *watchState, retrievalOnly, backgroundOnly bool, frame int) {
	// Get all state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	daemonState, _ := intcognition.ReadDaemonState(statePath)
	retrievalStats, _ := intcognition.ReadRetrievalStats(cfg.ContextDir)
	stats, _ := store.GetStats()

	// Column width
	totalWidth := 61

	// Get stats
	events := 0
	insights := 0
	if daemonState != nil {
		events = daemonState.Stats.Events
		insights = daemonState.Stats.Insights
	}
	if statsEvents, ok := stats["total_events"].(int); ok && statsEvents > events {
		events = statsEvents
	}
	if statsInsights, ok := stats["total_insights"].(int); ok && statsInsights > insights {
		insights = statsInsights
	}

	// Mode status
	modeIcon := "○"
	modeName := "IDLE"
	modeDesc := ""
	if daemonState != nil && daemonState.Mode != "" && daemonState.Mode != "idle" {
		modeIcon = getAnimatedModeSpinner(daemonState.Mode, frame)
		modeName = strings.ToUpper(daemonState.Mode) + "ING"
		if daemonState.Mode == "dream" {
			modeName = "DREAMING"
		} else if daemonState.Mode == "think" {
			modeName = "THINKING"
		}
		modeDesc = daemonState.Description
		if modeDesc == "" {
			modeDesc = getDefaultModeDescription(daemonState.Mode)
		}
	}

	// Print mode header
	fmt.Printf("┌%s┐\n", strings.Repeat("─", totalWidth))
	modeStr := fmt.Sprintf(" %s %s", modeIcon, modeName)
	if modeDesc != "" {
		modeStr += "  " + tui.Truncate(modeDesc, totalWidth-len(modeStr)-3)
	}
	fmt.Printf("│%s│\n", tui.Pad(modeStr, totalWidth))

	// Quick stats line
	statsLine := fmt.Sprintf(" Events: %d  Insights: %d", events, insights)
	if retrievalStats != nil && retrievalStats.TotalRetrievals > 0 {
		statsLine += fmt.Sprintf("  Queries: %d", retrievalStats.TotalRetrievals)
	}
	fmt.Printf("│%s│\n", tui.Pad(statsLine, totalWidth))

	// Sessions panel
	fmt.Printf("├%s┤\n", strings.Repeat("─", totalWidth))
	fmt.Printf("│%s│\n", tui.Pad(" Sessions", totalWidth))
	fmt.Printf("├%s┤\n", strings.Repeat("─", totalWidth))

	if len(state.sessions) == 0 {
		fmt.Printf("│%s│\n", tui.Pad(" No sessions yet. Use Claude Code to start.", totalWidth))
	} else {
		for i, sess := range state.sessions {
			// Format: [>/  ] HH:MM  "prompt..."  Last: action   N evts
			selector := "  "
			if i == state.selectedSession {
				selector = "> "
			}

			timeStr := sess.StartedAt.Format("15:04")
			promptSnippet := tui.Truncate(sess.InitialPrompt, 15)
			if promptSnippet == "" {
				promptSnippet = "(no prompt)"
			}
			lastAction := tui.Truncate(sess.LastAction, 18)
			if lastAction == "" {
				lastAction = "-"
			}

			line := fmt.Sprintf("%s%s  \"%s\"  Last: %s  %d evts",
				selector, timeStr, promptSnippet, lastAction, sess.EventCount)
			fmt.Printf("│%s│\n", tui.Pad(line, totalWidth))

			// If this session is expanded, show details
			if i == state.expandedSession {
				renderExpandedSession(cfg, store, sess, totalWidth)
			}
		}
	}

	fmt.Printf("└%s┘\n", strings.Repeat("─", totalWidth))

	// Controls hint
	if state.expandedSession == -1 {
		fmt.Printf("\nUp/Down: Navigate  Enter: Expand  q: Quit\n")
	} else {
		fmt.Printf("\nEsc: Collapse  q: Quit\n")
	}
}

// renderExpandedSession shows detailed session info.
func renderExpandedSession(cfg *config.Config, store *storage.Storage, sess *storage.SessionMetadata, totalWidth int) {
	// Session details box
	fmt.Printf("│%s│\n", tui.Pad("", totalWidth))

	// Full initial prompt
	if sess.InitialPrompt != "" {
		promptLine := "   Initial: \"" + tui.Truncate(sess.InitialPrompt, totalWidth-16) + "\""
		fmt.Printf("│%s│\n", tui.Pad(promptLine, totalWidth))
	}

	// Duration
	duration := time.Since(sess.StartedAt).Round(time.Minute)
	durationStr := fmt.Sprintf("   Duration: %v", duration)
	fmt.Printf("│%s│\n", tui.Pad(durationStr, totalWidth))

	// Recent activity for this session
	sessionEvents, err := store.GetSessionEvents(sess.SessionID, 5)
	if err == nil && len(sessionEvents) > 0 {
		fmt.Printf("│%s│\n", tui.Pad("   Recent Activity:", totalWidth))
		for _, evt := range sessionEvents {
			timeStr := evt.Timestamp.Format("15:04:05")
			action := evt.ToolName
			if action == "" {
				action = string(evt.EventType)
			}
			line := fmt.Sprintf("     %s  %s", timeStr, tui.Truncate(action, totalWidth-18))
			fmt.Printf("│%s│\n", tui.Pad(line, totalWidth))
		}
	}

	// Topic weights if available
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(sessionData, &session) == nil && len(session.TopicWeights) > 0 {
			topicList := make([]string, 0)
			for topic, weight := range session.TopicWeights {
				if weight > 0.3 {
					topicList = append(topicList, fmt.Sprintf("%s(%.1f)", topic, weight))
				}
			}
			if len(topicList) > 0 {
				topicsStr := "   Topics: " + strings.Join(topicList[:min(3, len(topicList))], " ")
				fmt.Printf("│%s│\n", tui.Pad(topicsStr, totalWidth))
			}
		}
	}

	fmt.Printf("│%s│\n", tui.Pad("", totalWidth))
}
