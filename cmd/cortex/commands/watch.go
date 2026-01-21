// Package commands provides CLI command implementations.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
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

// watchUIState holds the interactive watch UI state.
type watchUIState struct {
	selectedSession int // Index of selected session (0-based)
	expandedSession int // Index of expanded session (-1 = none)
	data            *WatchData
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

	// Initialize watch data
	data := NewWatchData(cfg, store)

	// Enter alternate screen buffer and hide cursor
	fmt.Print(tui.AltScreenEnter + tui.CursorHide)
	defer fmt.Print(tui.CursorShow + tui.AltScreenLeave) // Restore on exit

	// Initial render
	fmt.Print(tui.ClearAndHome())
	renderDashboard(data, animFrame, retrievalOnly, backgroundOnly)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Refresh data every frame (file reads are fast)
			data.Refresh(cfg, store)
			// Clear and redraw (prevents ghost content)
			fmt.Print(tui.ClearAndHome())
			renderDashboard(data, animFrame, retrievalOnly, backgroundOnly)
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

	// Initialize watch data and UI state
	data := NewWatchData(cfg, store)
	state := &watchUIState{
		selectedSession: 0,
		expandedSession: -1,
		data:            data,
	}

	// Animation state
	animFrame := 0

	// Enter alternate screen buffer and hide cursor
	fmt.Print(tui.AltScreenEnter + tui.CursorHide)
	defer fmt.Print(tui.CursorShow + tui.AltScreenLeave) // Restore on exit

	// Initial render
	fmt.Print(tui.ClearAndHome())
	renderInteractiveDashboard(cfg, store, state, animFrame, retrievalOnly, backgroundOnly)

	// Escape sequence state
	escapeSeq := make([]byte, 0, 3)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Refresh all data every frame
			state.data.Refresh(cfg, store)
			// Clear screen and redraw (prevents ghost content)
			fmt.Print(tui.ClearAndHome())
			renderInteractiveDashboard(cfg, store, state, animFrame, retrievalOnly, backgroundOnly)

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
							if state.expandedSession == -1 && state.selectedSession < len(state.data.Sessions)-1 {
								state.selectedSession++
							}
						}
					}
					escapeSeq = escapeSeq[:0]
					// Redraw after key
					fmt.Print(tui.ClearAndHome())
					renderInteractiveDashboard(cfg, store, state, animFrame, retrievalOnly, backgroundOnly)
				}
				continue
			}

			switch key {
			case 27: // Escape - start escape sequence or collapse
				if state.expandedSession != -1 {
					state.expandedSession = -1
					fmt.Print(tui.ClearAndHome())
					renderInteractiveDashboard(cfg, store, state, animFrame, retrievalOnly, backgroundOnly)
				} else {
					escapeSeq = append(escapeSeq, key)
				}
			case 13: // Enter - toggle expand
				if state.expandedSession == state.selectedSession {
					state.expandedSession = -1
				} else if len(state.data.Sessions) > 0 && state.selectedSession < len(state.data.Sessions) {
					state.expandedSession = state.selectedSession
				}
				fmt.Print(tui.ClearAndHome()) // Clear and redraw for expansion
				renderInteractiveDashboard(cfg, store, state, animFrame, retrievalOnly, backgroundOnly)
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
		DaemonState       *intcognition.DaemonState       `json:"daemon_state,omitempty"`
		RetrievalStats    *intcognition.RetrievalStats    `json:"retrieval_stats,omitempty"`
		BackgroundMetrics *intcognition.BackgroundMetrics `json:"background_metrics,omitempty"`
		RecentActivity    []intcognition.ActivityLogEntry `json:"recent_activity,omitempty"`
		Stats             map[string]interface{}          `json:"stats,omitempty"`
	}

	output := WatchOutput{}

	// Get daemon state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	output.DaemonState, _ = intcognition.ReadDaemonState(statePath)

	// Get retrieval stats
	output.RetrievalStats, _ = intcognition.ReadRetrievalStats(cfg.ContextDir)

	// Get background metrics
	output.BackgroundMetrics, _ = intcognition.ReadBackgroundMetrics(cfg.ContextDir)

	// Get recent activity
	output.RecentActivity, _ = intcognition.ReadRecentActivity(cfg.ContextDir, 10)

	// Get storage stats
	output.Stats, _ = store.GetStats()

	data, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(data))
}

// printWatchStatic outputs a single snapshot without animation.
func printWatchStatic(cfg *config.Config, store *storage.Storage, retrievalOnly, backgroundOnly bool) {
	data := NewWatchData(cfg, store)
	renderDashboard(data, 0, retrievalOnly, backgroundOnly)
}

// Dashboard layout constants
const (
	dashboardWidth = 61
	col1Width      = 28
	col2Width      = 28
)

// rawPrint prints a line with \r\n for raw terminal mode.
// In raw mode, \n alone doesn't return to column 1.
func rawPrint(format string, args ...interface{}) {
	fmt.Printf(format+"\r\n", args...)
}

// rawPrintln prints a string with \r\n for raw terminal mode.
func rawPrintln(s string) {
	fmt.Print(s + "\r\n")
}

// renderDashboard renders the main dashboard view (used by both animated and static modes).
func renderDashboard(data *WatchData, frame int, retrievalOnly, backgroundOnly bool) {
	// Mode header
	icon, name, desc := data.ModeStatus(frame, true)
	for _, line := range tui.HeaderPanel(icon, name, desc, dashboardWidth) {
		fmt.Println(line)
	}

	// Main content panels
	if !retrievalOnly && !backgroundOnly {
		// Two-column split: Retrieval | Background
		leftLines := renderRetrievalColumn(data)
		rightLines := renderBackgroundColumn(data)
		for _, line := range tui.SplitPanel("Retrieval", "Background", leftLines, rightLines, col1Width, col2Width) {
			fmt.Println(line)
		}
	} else if backgroundOnly {
		// Background only
		lines := []string{
			fmt.Sprintf("Events: %d  Insights: %d", data.TotalEvents, data.TotalInsights),
		}
		if data.Background != nil {
			lines = append(lines, fmt.Sprintf("Think: budget %d/%d  Dream: queue %d",
				data.Background.ThinkBudget, data.Background.ThinkMaxBudget, data.Background.DreamQueueDepth))
		}
		for _, line := range tui.Panel("Background", lines, dashboardWidth) {
			fmt.Println(line)
		}
	} else if retrievalOnly {
		// Retrieval only
		lines := renderRetrievalColumn(data)
		for _, line := range tui.Panel("Retrieval", lines, dashboardWidth) {
			fmt.Println(line)
		}
	}

	// Activity feed
	fmt.Println()
	renderActivityFeed(data, frame, dashboardWidth)

	// Footer
	fmt.Printf("\nPress Ctrl+C to stop. Refreshing every 300ms...\n")
}

// renderRetrievalColumn builds the retrieval stats column content.
func renderRetrievalColumn(data *WatchData) []string {
	lines := make([]string, 0, 4)

	if data.Retrieval != nil {
		lines = append(lines, fmt.Sprintf("Queries: %d", data.Retrieval.TotalRetrievals))

		if data.Retrieval.LastMode != "" {
			mode := strings.ToUpper(data.Retrieval.LastMode[:1]) + data.Retrieval.LastMode[1:]
			lines = append(lines, fmt.Sprintf("Last: %dms (%s)", data.Retrieval.LastReflexMs, mode))
		}

		lines = append(lines, fmt.Sprintf("Results: %d -> %s", data.Retrieval.LastResults, data.Retrieval.LastDecision))

		// ABR metric
		abr := data.ABR()
		if abr > 0 {
			lines = append(lines, fmt.Sprintf("ABR: %.0f%%", abr*100))
		}
	} else {
		lines = append(lines, "No retrievals yet")
	}

	return lines
}

// renderBackgroundColumn builds the background stats column content.
func renderBackgroundColumn(data *WatchData) []string {
	lines := make([]string, 0, 4)

	lines = append(lines, fmt.Sprintf("Events: %d", data.TotalEvents))
	lines = append(lines, fmt.Sprintf("Insights: %d", data.TotalInsights))

	if data.Background != nil {
		lines = append(lines, fmt.Sprintf("Think: %s (budget %d)", data.ThinkStatus(), data.Background.ThinkBudget))
		lines = append(lines, fmt.Sprintf("Dream: %s (queue %d)", data.DreamStatus(), data.Background.DreamQueueDepth))
	}

	// Topic weights
	topics := data.TopTopics(3, 0.3)
	if len(topics) > 0 {
		parts := make([]string, len(topics))
		for i, t := range topics {
			parts[i] = fmt.Sprintf("%s(%.1f)", t.Topic, t.Weight)
		}
		lines = append(lines, "Topics: "+strings.Join(parts, ", "))
	}

	return lines
}

// renderActivityFeed renders the activity log panel.
func renderActivityFeed(data *WatchData, frame int, width int) {
	chars := tui.BoxChars(tui.StyleSingle)

	// Header
	fmt.Println(chars.TopLeft + tui.HLine(width-2, tui.StyleSingle) + chars.TopRight)
	fmt.Printf("%s %s %s\n", chars.Vertical, tui.Pad("Activity Feed", width-4), chars.Vertical)
	fmt.Println(chars.VerticalRight + tui.HLine(width-2, tui.StyleSingle) + chars.VerticalLeft)

	if len(data.Activity) > 0 {
		for _, entry := range data.Activity {
			timeStr := entry.Timestamp.Format("15:04:05")
			modeIcon := getAnimatedModeSpinner(entry.Mode, frame)
			desc := tui.Truncate(entry.Description, width-20)
			line := fmt.Sprintf(" %s %s %s", timeStr, tui.Pad(modeIcon, 2), desc)
			fmt.Printf("%s%s%s\n", chars.Vertical, tui.Pad(line, width-2), chars.Vertical)
		}
	} else {
		line := " No activity yet. Start daemon with: cortex daemon &"
		fmt.Printf("%s%s%s\n", chars.Vertical, tui.Pad(line, width-2), chars.Vertical)
	}

	fmt.Println(chars.BottomLeft + tui.HLine(width-2, tui.StyleSingle) + chars.BottomRight)
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

// renderInteractiveDashboard renders the interactive watch view with sessions panel.
func renderInteractiveDashboard(cfg *config.Config, store *storage.Storage, state *watchUIState, frame int, retrievalOnly, backgroundOnly bool) {
	data := state.data
	chars := tui.BoxChars(tui.StyleSingle)

	// Mode header
	icon, name, desc := data.ModeStatus(frame, true)
	modeStr := fmt.Sprintf(" %s %s", icon, name)
	if desc != "" {
		modeStr += "  " + tui.Truncate(desc, dashboardWidth-tui.VisibleWidth(modeStr)-3)
	}

	rawPrintln(chars.TopLeft + tui.HLine(dashboardWidth-2, tui.StyleSingle) + chars.TopRight)
	rawPrint("%s%s%s", chars.Vertical, tui.Pad(modeStr, dashboardWidth-2), chars.Vertical)

	// Quick stats line
	statsLine := fmt.Sprintf(" Events: %d  Insights: %d", data.TotalEvents, data.TotalInsights)
	if data.Retrieval != nil && data.Retrieval.TotalRetrievals > 0 {
		statsLine += fmt.Sprintf("  Queries: %d", data.Retrieval.TotalRetrievals)
	}
	if data.Background != nil {
		statsLine += fmt.Sprintf("  ABR: %.0f%%", data.ABR()*100)
	}
	rawPrint("%s%s%s", chars.Vertical, tui.Pad(statsLine, dashboardWidth-2), chars.Vertical)

	// Sessions panel header
	sessionCount := len(data.Sessions)
	sessionTitle := fmt.Sprintf(" Sessions (%d)", sessionCount)
	rawPrintln(chars.VerticalRight + tui.HLine(dashboardWidth-2, tui.StyleSingle) + chars.VerticalLeft)
	rawPrint("%s%s%s", chars.Vertical, tui.Pad(sessionTitle, dashboardWidth-2), chars.Vertical)
	rawPrintln(chars.VerticalRight + tui.HLine(dashboardWidth-2, tui.StyleSingle) + chars.VerticalLeft)

	if sessionCount == 0 {
		rawPrint("%s%s%s", chars.Vertical, tui.Pad(" No sessions yet. Use Claude Code to start.", dashboardWidth-2), chars.Vertical)
	} else {
		for i, sess := range data.Sessions {
			// Format: [>/  ] HH:MM  "prompt..."  [N evts]
			selector := "  "
			if i == state.selectedSession {
				selector = "> "
			}

			timeStr := sess.StartedAt.Format("15:04")
			promptSnippet := tui.Truncate(sess.InitialPrompt, 20)
			if promptSnippet == "" {
				promptSnippet = "(no prompt)"
			}

			line := fmt.Sprintf("%s%s  \"%s\"  [%d evts]", selector, timeStr, promptSnippet, sess.EventCount)
			rawPrint("%s%s%s", chars.Vertical, tui.Pad(line, dashboardWidth-2), chars.Vertical)

			// If this session is expanded, show details
			if i == state.expandedSession {
				renderSessionDetails(store, data, sess, dashboardWidth, chars)
			}
		}
	}

	rawPrintln(chars.BottomLeft + tui.HLine(dashboardWidth-2, tui.StyleSingle) + chars.BottomRight)

	// Controls hint
	if state.expandedSession == -1 {
		rawPrint("")
		rawPrintln("Up/Down: Navigate  Enter: Expand  q: Quit")
	} else {
		rawPrint("")
		rawPrintln("Esc: Collapse  q: Quit")
	}
}

// renderSessionDetails shows detailed session info when expanded.
func renderSessionDetails(store *storage.Storage, data *WatchData, sess *storage.SessionMetadata, width int, chars tui.BoxCharSet) {
	// Blank line
	rawPrint("%s%s%s", chars.Vertical, tui.Pad("", width-2), chars.Vertical)

	// Full initial prompt
	if sess.InitialPrompt != "" {
		promptLine := "   Initial: \"" + tui.Truncate(sess.InitialPrompt, width-18) + "\""
		rawPrint("%s%s%s", chars.Vertical, tui.Pad(promptLine, width-2), chars.Vertical)
	}

	// Duration
	duration := time.Since(sess.StartedAt).Round(time.Minute)
	durationStr := fmt.Sprintf("   Duration: %v", duration)
	rawPrint("%s%s%s", chars.Vertical, tui.Pad(durationStr, width-2), chars.Vertical)

	// Recent activity for this session
	sessionEvents, err := store.GetSessionEvents(sess.SessionID, 5)
	if err == nil && len(sessionEvents) > 0 {
		rawPrint("%s%s%s", chars.Vertical, tui.Pad("   Recent Activity:", width-2), chars.Vertical)
		for _, evt := range sessionEvents {
			timeStr := evt.Timestamp.Format("15:04:05")
			action := evt.ToolName
			if action == "" {
				action = string(evt.EventType)
			}
			line := fmt.Sprintf("     %s  %s", timeStr, tui.Truncate(action, width-20))
			rawPrint("%s%s%s", chars.Vertical, tui.Pad(line, width-2), chars.Vertical)
		}
	}

	// Topic weights
	topics := data.TopTopics(3, 0.3)
	if len(topics) > 0 {
		parts := make([]string, len(topics))
		for i, t := range topics {
			parts[i] = fmt.Sprintf("%s(%.1f)", t.Topic, t.Weight)
		}
		topicsStr := "   Topics: " + strings.Join(parts, ", ")
		rawPrint("%s%s%s", chars.Vertical, tui.Pad(topicsStr, width-2), chars.Vertical)
	}

	// Blank line
	rawPrint("%s%s%s", chars.Vertical, tui.Pad("", width-2), chars.Vertical)
}
