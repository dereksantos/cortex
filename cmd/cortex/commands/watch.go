// Package commands provides CLI command implementations.
package commands

import (
	"encoding/json"
	"fmt"
	"os"

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
	interactive := true // Default to interactive if TTY

	for _, arg := range ctx.Args {
		switch arg {
		case "--json":
			jsonOutput = true
			interactive = false
		case "--no-animate":
			noAnimate = true
			interactive = false
		case "--no-interactive":
			interactive = false
		case "-h", "--help":
			fmt.Println("Usage: cortex watch [flags]")
			fmt.Println("\nFlags:")
			fmt.Println("  --json             Machine-readable JSON output")
			fmt.Println("  --no-animate       Static output (single snapshot)")
			fmt.Println("  --no-interactive   Disable keyboard interaction")
			fmt.Println("\nInteractive controls:")
			fmt.Println("  ↑/↓                Scroll activity log")
			fmt.Println("  d                  Start/stop daemon")
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
		printWatchStatic(cfg, store)
		return nil
	}

	// Check if we're in a TTY for interactive mode
	if interactive && !term.IsTerminal(int(os.Stdin.Fd())) {
		interactive = false
	}

	if interactive {
		runInteractiveWatch(cfg, store)
	} else {
		runAnimatedWatch(cfg, store)
	}

	return nil
}

// watchUIState holds the interactive watch UI state.
type watchUIState struct {
	scrollOffset int // Scroll offset for log feed (0 = bottom/newest)
	data         *WatchData
}

// runAnimatedWatch runs the non-interactive animated watch.
func runAnimatedWatch(cfg *config.Config, store *storage.Storage) {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	notifyTermSignals(sigChan)

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
	renderDashboard(data, animFrame)

	for {
		select {
		case <-ticker.C:
			animFrame++
			// Refresh data every frame (file reads are fast)
			data.Refresh(cfg, store)
			// Clear and redraw (prevents ghost content)
			fmt.Print(tui.ClearAndHome())
			renderDashboard(data, animFrame)
		case <-sigChan:
			fmt.Print(tui.CursorShow) // Show cursor
			fmt.Println("\n\nStopped watching.")
			return
		}
	}
}

// runInteractiveWatch runs the interactive watch with keyboard input.
func runInteractiveWatch(cfg *config.Config, store *storage.Storage) {
	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	notifyTermSignals(sigChan)

	// Put terminal in raw mode for keyboard input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		// Fall back to non-interactive mode
		runAnimatedWatch(cfg, store)
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
		data: data,
	}

	// Animation state
	animFrame := 0

	// Enter alternate screen buffer and hide cursor
	fmt.Print(tui.AltScreenEnter + tui.CursorHide)
	defer fmt.Print(tui.CursorShow + tui.AltScreenLeave) // Restore on exit

	// Initial render
	fmt.Print(tui.ClearAndHome())
	renderInteractiveDashboard(cfg, state, animFrame)

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
			renderInteractiveDashboard(cfg, state, animFrame)

		case key := <-keyChan:
			// Handle escape sequences (arrow keys)
			if len(escapeSeq) > 0 {
				escapeSeq = append(escapeSeq, key)
				if len(escapeSeq) == 3 {
					// Process escape sequence
					if escapeSeq[0] == 27 && escapeSeq[1] == 91 {
						switch escapeSeq[2] {
						case 65: // Up arrow - scroll up (older entries)
							maxScroll := len(state.data.Activity) - 1
							if maxScroll < 0 {
								maxScroll = 0
							}
							if state.scrollOffset < maxScroll {
								state.scrollOffset++
							}
						case 66: // Down arrow - scroll down (newer entries)
							if state.scrollOffset > 0 {
								state.scrollOffset--
							}
						}
					}
					escapeSeq = escapeSeq[:0]
					// Redraw after key
					fmt.Print(tui.ClearAndHome())
					renderInteractiveDashboard(cfg, state, animFrame)
				}
				continue
			}

			switch key {
			case 27: // Escape - start escape sequence
				escapeSeq = append(escapeSeq, key)
			case 'q', 3: // q or Ctrl+C
				return
			case 'd': // d - toggle daemon
				toggleDaemon(cfg)
				// Give daemon time to start/stop, then refresh
				time.Sleep(500 * time.Millisecond)
				state.data.Refresh(cfg, store)
				fmt.Print(tui.ClearAndHome())
				renderInteractiveDashboard(cfg, state, animFrame)
			}

		case <-sigChan:
			return
		}
	}
}

// toggleDaemon starts or stops the daemon based on current state.
func toggleDaemon(cfg *config.Config) {
	if IsDaemonRunning(cfg.ContextDir) {
		StopDaemon(cfg.ContextDir)
	} else {
		StartDaemonBackground(cfg.ContextDir)
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
func printWatchStatic(cfg *config.Config, store *storage.Storage) {
	data := NewWatchData(cfg, store)
	renderDashboard(data, 0)
}

// rawPrint prints a line with \r\n for raw terminal mode.
// In raw mode, \n alone doesn't return to column 1.
func rawPrint(format string, args ...interface{}) {
	fmt.Printf(format+"\r\n", args...)
}

// rawPrintln prints a string with \r\n for raw terminal mode.
func rawPrintln(s string) {
	fmt.Print(s + "\r\n")
}

// renderDashboard renders the simplified dashboard view (used by both animated and static modes).
// Layout: Stats header, Log feed, Commands footer - no boxes.
func renderDashboard(data *WatchData, frame int) {
	// Stats header
	icon, modeName, _ := data.ModeStatus(frame, true)
	abr := data.ABR()
	abrStr := "-"
	if abr > 0 {
		abrStr = fmt.Sprintf("%.0f%%", abr*100)
	}
	fmt.Printf("%s %s  Events: %d  Insights: %d  ABR: %s\n",
		icon, modeName, data.TotalEvents, data.TotalInsights, abrStr)
	fmt.Println()

	// Log feed
	if len(data.Activity) > 0 {
		for _, entry := range data.Activity {
			timeStr := entry.Timestamp.Format("15:04:05")
			fmt.Printf("%s [%s] %s\n", timeStr, entry.Mode, entry.Description)
		}
	} else {
		fmt.Println("No activity yet. Start daemon with: cortex daemon &")
	}
	fmt.Println()

	// Commands footer
	fmt.Println("d:daemon  q:quit")
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

// renderInteractiveDashboard renders the simplified interactive watch view.
// Layout: Stats header, scrollable Log feed, Commands footer - no boxes.
func renderInteractiveDashboard(cfg *config.Config, state *watchUIState, frame int) {
	data := state.data

	// Stats header
	icon, modeName, _ := data.ModeStatus(frame, true)
	abr := data.ABR()
	abrStr := "-"
	if abr > 0 {
		abrStr = fmt.Sprintf("%.0f%%", abr*100)
	}
	rawPrint("%s %s  Events: %d  Insights: %d  ABR: %s",
		icon, modeName, data.TotalEvents, data.TotalInsights, abrStr)
	rawPrint("")

	// Log feed (with scroll support)
	maxLogLines := 15
	if len(data.Activity) > 0 {
		totalEntries := len(data.Activity)
		startIdx := state.scrollOffset
		endIdx := startIdx + maxLogLines
		if endIdx > totalEntries {
			endIdx = totalEntries
		}
		if startIdx >= totalEntries {
			startIdx = totalEntries - 1
			if startIdx < 0 {
				startIdx = 0
			}
		}

		// Show scroll indicator if there are more entries above
		if state.scrollOffset > 0 {
			rawPrint("  ↑ %d more", state.scrollOffset)
		}

		// Activity is stored newest-first, so reverse for display
		visibleEntries := data.Activity[startIdx:endIdx]
		for i := len(visibleEntries) - 1; i >= 0; i-- {
			entry := visibleEntries[i]
			timeStr := entry.Timestamp.Format("15:04:05")
			rawPrint("%s [%s] %s", timeStr, entry.Mode, entry.Description)
		}
	} else {
		rawPrint("No activity yet. Start daemon with: cortex daemon &")
	}
	rawPrint("")

	// Commands footer with daemon status
	daemonStatus := "off"
	if IsDaemonRunning(cfg.ContextDir) {
		daemonStatus = "on"
	}
	rawPrint("d:daemon(%s)  ↑↓:scroll  q:quit", daemonStatus)
}
