package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// gutter reports the role color for a message line. The REPL is glyph-free
// by decision (2026-07-19): there is no per-role icon anymore, so the
// timestamp itself carries the color that used to sit on the dropped icon
// (❯◆▸✻) — gray is reserved for secondary/note lines (streaming.go's
// breadcrumb and thought-stat).
func (m Message) gutter() (color string) {
	switch m.Role {
	case RoleUser:
		return cyan
	case RoleTool:
		return green
	default:
		return blue
	}
}

func gutterPrefix(color string, ts time.Time) string {
	return fmt.Sprintf("%s  ", withColor(ts.Format("15:04:05"), color))
}

func (m Message) render(ts time.Time) string {
	return gutterPrefix(m.gutter(), ts) + m.Content
}

func (m Message) Print() {
	fmt.Println(m.render(time.Now()))
}

func (cs *CortexSession) Prompt() string {
	win := cs.windowSize()
	status := withColor(fmt.Sprintf("cortex %s | %s | ", version(), cs.Request.Model), gray)
	// Use LastPromptTokens which is updated in Append() to reflect current context
	gauge := withColor(fmt.Sprintf("%s/%s", humanK(cs.LastPromptTokens), humanK(win)), ctxColor(cs.LastPromptTokens, win))
	cost := ""
	if cs.costUSD > 0 {
		cost = withColor(" | "+humanCost(cs.costUSD), gray)
	}
	return fmt.Sprintf("%s%s%s  %s ", status, gauge, cost, withColor(promptGlyph, cyan))
}

func streamingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOOP_STREAM"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}
