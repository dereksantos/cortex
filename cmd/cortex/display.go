package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// gutterPrefix renders the "HH:MM:SS  " shown before every printed line. The
// REPL is glyph-free by decision (2026-07-19): there is no per-role icon
// anymore, and the timestamp no longer carries a per-role color either
// (2026-07-19) — every timestamp, on every line (user/assistant/tool alike),
// is plain gray so the gutter reads as one consistent margin rather than a
// row of role-coded tags.
func gutterPrefix(ts time.Time) string {
	return fmt.Sprintf("%s  ", withColor(ts.Format("15:04:05"), gray))
}

func (m Message) render(ts time.Time) string {
	return gutterPrefix(ts) + m.Content
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
