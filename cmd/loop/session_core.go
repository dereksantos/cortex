package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

type CortexArgs []string

func (a CortexArgs) Request() *AgentRequest {
	content := SystemPrompt
	if inst := projectInstructions(); inst != "" {
		content += "\n\n# Project instructions (AGENTS.md)\n\n" + inst
	}
	return &AgentRequest{
		Model:       defaultModel,
		Messages:    []Message{{Role: RoleSystem, Content: content}},
		Temperature: defaultTemperature,
		Tools:       toolSet,
		MaxTokens:   codeMaxOutputTokens,
	}
}

type CortexSession struct {
	Args             *CortexArgs
	Request          *AgentRequest
	LastPromptTokens int
	LastCachedTokens int
	Window           int
	Study            ModelSpec
	Fleet            Fleet
	Config           *Config
	deleteRoot       string
	allowDelete      bool
	quiet            bool
	confirmRisky     func(question string) bool
	classifyShell    shellrisk.ClassifyFn
	turnIntent       string
	SessionID        string
	transcript       *os.File
	capturer         *capture.Capture
	memory           *memory.Store
	ws               *cache.WorkingSet
	outline          []cache.OutlineEntry
	outlineFolded    string // digest of previously folded outline entries (P4); rides the front of the outline zone

	sessionStart  time.Time
	turns         int
	turnNo        int // 1-based ordinal of the in-flight turn; 0 between turns (stamped into transcript entries)
	tokensIn      int
	tokensOut     int
	costUSD       float64
	injectedChars int
	captures      int
	injections    int

	md      *markdownRenderer
	mdWidth int
	live    *lineedit.Anchor
}

func (cs *CortexSession) markdown() *markdownRenderer {
	if cs.quiet {
		return nil
	}
	w := terminalWidth()
	if cs.live != nil {
		w = cs.live.Width()
	} else if !renderEnabled() {
		return nil
	}
	if cs.md == nil || cs.mdWidth != w {
		cs.md, cs.mdWidth = newMarkdownRenderer(w), w
	}
	return cs.md
}

func (cs *CortexSession) SetModel(model string) { cs.Request.Model = model }

func (cs *CortexSession) windowSize() int {
	if cs.Window > 0 {
		return cs.Window
	}
	return fallbackWindow
}

// newWorkingSet builds the demotion policy for the current window: the
// hydrated tail may grow to half the window and drains to a third
// (docs/context-architecture.md budgets). base is the message-log index
// where turn content starts.
func (cs *CortexSession) newWorkingSet(base int) *cache.WorkingSet {
	return cache.New(base, cs.windowSize()/2, cs.windowSize()/3)
}

func NewCortexSession() *CortexSession {
	args := CortexArgs(os.Args)
	req := args.Request()
	cfg := LoadConfig()

	var fleet Fleet
	if !cfg.isOpenRouter() {
		fleet = discoverFleet(context.Background(), cfg.backendEndpoint())
		if fleet == nil {
			fmt.Println(withColor(fmt.Sprintf("note: model discovery unavailable at %s — set backend in .cortex/config.json or pin models", cfg.backendEndpoint()), yellow))
		}
	}
	code := cfg.resolveBinding(roleCode, fleet)
	study := cfg.resolveBinding(roleStudy, fleet)

	if g := sharedSwapGroup(fleet, code, study); g != "" {
		fmt.Println(withColor(fmt.Sprintf("warning: code (%s) and study (%s) share swap_group %q — they evict each other every turn; route one to different silicon", code.Model, study.Model, g), yellow))
	}

	req.Model = code.Model
	req.BaseURL = code.Endpoint
	req.APIKey = resolveKey(code)
	req.ChatTemplateKwargs = code.TemplateKwargs()
	req.MaxTokens = code.maxOut(codeMaxOutputTokens)
	req.Temperature = code.temperature(defaultTemperature)

	if cfg.isOpenRouter() {
		req.Usage = &usageInclude{Include: true}
	}

	allowDelete := cfg.deleteEnabled()
	deleteRoot := "."
	if cfg != nil && cfg.Tools.DeleteRoot != "" {
		deleteRoot = cfg.Tools.DeleteRoot
	}
	if abs, err := filepath.Abs(deleteRoot); err == nil {
		deleteRoot = abs
	}
	if !allowDelete {
		req.Tools = toolsExcept(req.Tools, FunctionRemove)
	}

	cs := &CortexSession{
		Args:         &args,
		Request:      req,
		Config:       cfg,
		Window:       code.Window,
		Study:        study,
		Fleet:        fleet,
		deleteRoot:   deleteRoot,
		allowDelete:  allowDelete,
		sessionStart: time.Now(),
	}
	cs.ws = cs.newWorkingSet(1)
	return cs
}

// IsToolEnabled reports whether a context window tool is enabled via config.
func (cs *CortexSession) IsToolEnabled(toolName string) bool {
	if cs.Config == nil {
		return true // default: all tools enabled
	}
	// Check if Tools has any config set (all pointers are nil means defaults)
	t := &cs.Config.Tools
	if t.EnableContextSummarize == nil &&
		t.EnableContextEvict == nil &&
		t.EnableContextMerge == nil &&
		t.EnableContextReorder == nil &&
		t.EnableContextAdjustWatermarks == nil &&
		t.AllowDelete == nil &&
		t.DeleteRoot == "" {
		return true // default: all tools enabled
	}
	switch toolName {
	case "context_summarize":
		return t.EnableContextSummarize == nil || *t.EnableContextSummarize
	case "context_evict":
		return t.EnableContextEvict == nil || *t.EnableContextEvict
	case "context_merge":
		return t.EnableContextMerge == nil || *t.EnableContextMerge
	case "context_reorder":
		return t.EnableContextReorder == nil || *t.EnableContextReorder
	case "context_adjust_watermarks":
		return t.EnableContextAdjustWatermarks == nil || *t.EnableContextAdjustWatermarks
	}
	return true // unknown tools enabled by default
}

// ValidateToolCall provides dynamic validation for tool calls beyond config.
// Returns (true, "") if valid, (false, message) if invalid.
func (cs *CortexSession) ValidateToolCall(tc ToolCall) (bool, string) {
	switch tc.Function.Name {
	case "context_adjust_watermarks":
		// Validate watermarks are within bounds (±W/4)
		if cs != nil {
			w := cs.windowSize()
			bound := w / 4
			if highDelta, _ := tc.IntArg("high_delta"); highDelta != 0 {
				if highDelta < -bound || highDelta > bound {
					return false, fmt.Sprintf("high_delta %d is out of bounds (±%d for window size %d)", highDelta, bound, w)
				}
			}
			if lowDelta, _ := tc.IntArg("low_delta"); lowDelta != 0 {
				if lowDelta < -bound || lowDelta > bound {
					return false, fmt.Sprintf("low_delta %d is out of bounds (±%d for window size %d)", lowDelta, bound, w)
				}
			}
		}
	}
	return true, ""
}

// GetOutline returns the current outline entries.
// Used by context tools to read the outline.
func (cs *CortexSession) GetOutline() []cache.OutlineEntry {
	return cs.outline
}

// RemoveOutlineEntry removes an outline entry by citation.
// Returns true if the entry was found and removed.
// This is idempotent (safe to call multiple times).
func (cs *CortexSession) RemoveOutlineEntry(citation string) bool {
	for i := 0; i < len(cs.outline); i++ {
		if cs.outline[i].Citation == citation {
			cs.outline = append(cs.outline[:i], cs.outline[i+1:]...)
			return true
		}
	}
	return false
}

// OutlineLen returns the number of outline entries.
func (cs *CortexSession) OutlineLen() int {
	return len(cs.outline)
}

func toolsExcept(ts []Tool, name string) []Tool {
	out := make([]Tool, 0, len(ts))
	for _, t := range ts {
		if t.Function.Name != name {
			out = append(out, t)
		}
	}
	return out
}

func (cs *CortexSession) PrintArgs() {
	fmt.Printf("Cortex Model: %s Temp:%f\n", cs.Request.Model, cs.Request.Temperature)
}

func (cs *CortexSession) Append(message Message) {
	cs.Request.Messages = append(cs.Request.Messages, message)
	cs.writeTranscript(message)
}
