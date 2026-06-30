package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
		Temperature: 0,
		Tools:       toolSet,
		MaxTokens:   codeMaxOutputTokens,
	}
}

type CortexSession struct {
	Args             *CortexArgs
	Request          *AgentRequest
	LastPromptTokens int
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

	sessionStart  time.Time
	turns         int
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

	return &CortexSession{
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
