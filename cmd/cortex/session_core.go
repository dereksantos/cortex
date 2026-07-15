package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/shellrisk"
	"github.com/dereksantos/cortex/internal/tools"
)

type CortexArgs []string

func (a CortexArgs) Request() *AgentRequest {
	return &AgentRequest{
		Model:       defaultModel,
		Messages:    []Message{{Role: RoleSystem, Content: systemPromptContent(projectInstructions())}},
		Temperature: defaultTemperature,
		Tools:       toolSet,
		MaxTokens:   codeMaxOutputTokens,
	}
}

// systemPromptContent builds the system message content: the base
// SystemPrompt plus an optional "# Project instructions (AGENTS.md)"
// section when instructions is non-empty. Shared by CortexArgs.Request()
// (CWD-implicit, via projectInstructions()) and applyProjectByName
// (project_workspace.go, M3.5's --project, via Workspace.Instructions())
// so the two stay provably identical modulo their instructions source.
func systemPromptContent(instructions string) string {
	content := SystemPrompt
	if instructions != "" {
		content += "\n\n# Project instructions (AGENTS.md)\n\n" + instructions
	}
	return content
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
	workspace        *Workspace
	deleteRoot       string
	allowDelete      bool
	quiet            bool
	confirmRisky     func(question string) bool
	classifyShell    shellrisk.ClassifyFn
	turnIntent       string
	// onThinking, when set, is invoked with active=true on the first
	// reasoning delta of a model call and active=false once its answer
	// content starts (or the call ends without one) — the served-session SSE
	// handler's hook (serve_stream.go) so the web UI gets a "thinking" event
	// instead of dead air while a quiet session deliberates. nil (the
	// default, including every non-served session) leaves send()'s quiet
	// path on the plain blocking Send it already used.
	onThinking    func(active bool)
	SessionID     string
	transcript    *os.File
	capturer      *capture.Capture
	memory        *memory.Store
	ws            *cache.WorkingSet
	outline       []cache.OutlineEntry
	outlineFolded string // digest of previously folded outline entries (P4); rides the front of the outline zone

	// awaitingScanRootsReply is armed by MaybeGreet (M1.7) right after a
	// first-run greeting fires; the REPL read loop's next call to
	// MaybeCaptureScanRoots (scanroots.go) treats that reply as the
	// answer to "where does your code live" and persists it.
	awaitingScanRootsReply bool

	sessionStart    time.Time
	turns           int
	turnNo          int // 1-based ordinal of the in-flight turn; 0 between turns (stamped into transcript entries)
	tokensIn        int
	tokensOut       int
	reasoningTokens int // completion_tokens_details.reasoning_tokens summed across the session (0 if never reported)
	costUSD         float64
	injectedChars   int
	captures        int
	injections      int

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
	if !cfg.scanEnabled() {
		req.Tools = toolsExcept(req.Tools, tools.FunctionScanLandscape)
	}

	cs := &CortexSession{
		Args:         &args,
		Request:      req,
		Config:       cfg,
		workspace:    WorkspaceFromCWD(),
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
	// nil pointers mean defaults: enabled.
	t := &cs.Config.Tools
	switch toolName {
	case tools.FunctionWebSearch, tools.FunctionFetchURL:
		return t.EnableWeb == nil || *t.EnableWeb
	case tools.FunctionAgent:
		return t.EnableAgent == nil || *t.EnableAgent
	case tools.FunctionScanLandscape:
		return t.EnableScan == nil || *t.EnableScan
	case tools.FunctionContextEvict:
		return t.EnableContextEvict == nil || *t.EnableContextEvict
	case tools.FunctionContextMerge:
		return t.EnableContextMerge == nil || *t.EnableContextMerge
	case tools.FunctionContextAdjustWatermarks:
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

// MergeOutlineEntries replaces the contiguous outline entries from
// startCitation through endCitation with a single merged entry. The merged
// entry carries ONE spanning citation — @session/<id>#m<firstStart>-<lastEnd>
// — so recall still resolves every original message (turn spans partition the
// message log, so the span between two outline citations is contiguous even
// if an entry in between was evicted). Returns the spanning citation.
func (cs *CortexSession) MergeOutlineEntries(startCitation, endCitation string) (string, error) {
	sm := citationRe.FindStringSubmatch(startCitation)
	em := citationRe.FindStringSubmatch(endCitation)
	if sm == nil || em == nil {
		return "", fmt.Errorf("citations must be @session/<id>#m<start>-<end> coordinates, as shown in the outline")
	}
	if sm[1] != em[1] {
		return "", fmt.Errorf("citations reference different sessions (%s vs %s)", sm[1], em[1])
	}

	startIdx, endIdx := -1, -1
	for i, e := range cs.outline {
		if e.Citation == startCitation {
			startIdx = i
		}
		if e.Citation == endCitation {
			endIdx = i
		}
	}
	if startIdx == -1 {
		return "", fmt.Errorf("start citation %s not found in the outline", startCitation)
	}
	if endIdx == -1 {
		return "", fmt.Errorf("end citation %s not found in the outline", endCitation)
	}
	if endIdx <= startIdx {
		return "", fmt.Errorf("range_end must come after range_start in the outline")
	}

	spanning := fmt.Sprintf("@session/%s#m%s-%s", sm[1], sm[2], em[3])
	merged := mergeOutlineEntries(cs.outline[startIdx:endIdx+1], spanning)
	cs.outline = append(cs.outline[:startIdx], append([]cache.OutlineEntry{merged}, cs.outline[endIdx+1:]...)...)
	return spanning, nil
}

// mergeOutlineEntries folds a run of outline entries into one: user heads join
// on newlines (bounded by outlineUserCap), actions concatenate in order, and
// the reply head keeps the last non-empty one — the state the run ended in.
func mergeOutlineEntries(entries []cache.OutlineEntry, citation string) cache.OutlineEntry {
	users := make([]string, 0, len(entries))
	var actions []string
	replyHead := ""
	for _, e := range entries {
		if e.User != "" {
			users = append(users, e.User)
		}
		actions = append(actions, e.Actions...)
		if e.ReplyHead != "" {
			replyHead = e.ReplyHead
		}
	}
	user := strings.Join(users, "\n")
	if r := []rune(user); len(r) > outlineUserCap {
		user = string(r[:outlineUserCap]) + "… (truncated; recall the citation below for the rest)"
	}
	return cache.OutlineEntry{
		Turn:      entries[0].Turn,
		User:      user,
		Actions:   actions,
		ReplyHead: replyHead,
		Citation:  citation,
	}
}

// OutlineLen returns the number of outline entries.
func (cs *CortexSession) OutlineLen() int {
	return len(cs.outline)
}

// AdjustWatermarks adjusts the working set watermarks by the given deltas.
// Bounded (±W/4) to prevent abuse. Returns (oldHigh, oldLow, newHigh, newLow, error).
func (cs *CortexSession) AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error) {
	if cs.ws == nil {
		return 0, 0, 0, 0, fmt.Errorf("working set not available")
	}
	oldHigh, oldLow := cs.ws.GetWatermarks()
	newHigh, newLow, err := cs.ws.AdjustWatermarks(highDelta, lowDelta)
	return oldHigh, oldLow, newHigh, newLow, err
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
	// Update LastPromptTokens to reflect current context size
	// This ensures the display gauge updates as tool results are appended
	cs.LastPromptTokens = cs.currentContextSize()
}

// currentContextSize estimates the current context size from all messages.
// It sums len(Content) for each message plus len(Function.Name)+len(Function.Arguments)
// for each ToolCall, then converts to tokens using cache.TokensOf.
func (cs *CortexSession) currentContextSize() int {
	sum := 0
	for _, msg := range cs.Request.Messages {
		sum += len(msg.Content)
		for _, call := range msg.ToolCalls {
			sum += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return cache.TokensOf(sum)
}
