package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/tools"
)

const (
	iconCortex  = tools.IconCortex
	iconTool    = tools.IconTool
	iconUser    = tools.IconUser
	iconThought = tools.IconThought
)

func (m Message) gutter() (icon, color string) {
	switch m.Role {
	case RoleUser:
		return iconUser, cyan
	case RoleTool:
		return iconTool, green
	default:
		return iconCortex, blue
	}
}

func gutterPrefix(icon, color string, ts time.Time) string {
	return fmt.Sprintf("%s %s  ", withColor(icon, color), withColor(ts.Format("15:04:05"), gray))
}

func (m Message) render(ts time.Time) string {
	icon, color := m.gutter()
	return gutterPrefix(icon, color, ts) + m.Content
}

func (m Message) Print() {
	fmt.Println(m.render(time.Now()))
}

func (cs *CortexSession) Prompt() string {
	win := cs.windowSize()
	status := withColor(fmt.Sprintf("cortex %s · %s · ", version(), cs.Request.Model), gray)
	gauge := withColor(fmt.Sprintf("%s/%s", humanK(cs.LastPromptTokens), humanK(win)), ctxColor(cs.LastPromptTokens, win))
	cost := ""
	if cs.costUSD > 0 {
		cost = withColor(" · "+humanCost(cs.costUSD), gray)
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
