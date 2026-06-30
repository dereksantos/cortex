package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loopui"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

func (cs *CortexSession) EnableMemory() {
	dir := contextDir()
	if mem, err := memory.New(dir); err == nil {
		cs.memory = mem
	}
	cfg := &config.Config{ContextDir: dir, ProjectRoot: filepath.Dir(dir)}
	cs.capturer = capture.New(cfg)
}

func (cs *CortexSession) resolveEmbedder() llm.Embedder {
	if e := cs.newSpecEmbedder(cs.Config.resolveBinding(roleEmbed, cs.Fleet)); e != nil {
		return e
	}
	if !localEmbedEnabled() {
		return nil
	}
	h := llm.NewHugotEmbedder()
	h.Warm()
	return h
}

func localEmbedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_LOCAL_EMBED"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

func (cs *CortexSession) newSpecEmbedder(spec ModelSpec) llm.Embedder {
	if strings.TrimSpace(spec.Model) == "" {
		return nil
	}
	base := strings.TrimRight(spec.Endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return llm.NewOpenAICompatEmbedder(llm.EndpointConfig{
		Name:    "embedder",
		BaseURL: base,
		APIKey:  resolveKey(spec),
	}, spec.Model)
}

const captureExcerptCap = 280

func turnArtifacts(turnMsgs []Message) (outcome, answer string) {
	var files, cmds []string
	seen := map[string]bool{}
	for _, m := range turnMsgs {
		for _, tc := range m.ToolCalls {
			switch tc.Function.Name {
			case FunctionWriteFile, FunctionEditFile:
				if p, err := tc.StringArg("path"); err == nil && !seen["f:"+p] {
					seen["f:"+p] = true
					files = append(files, p)
				}
			case FunctionBash:
				if c, err := tc.StringArg("command"); err == nil && !seen["c:"+c] {
					seen["c:"+c] = true
					cmds = append(cmds, c)
				}
			}
		}
		if m.Role != RoleUser && m.Role != RoleTool && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
			answer = m.Content
		}
	}
	var parts []string
	if len(files) > 0 {
		parts = append(parts, "edited: "+strings.Join(files, ", "))
	}
	if len(cmds) > 0 {
		parts = append(parts, "ran: "+strings.Join(cmds, "; "))
	}
	return strings.Join(parts, " | "), answer
}

func turnUsedTools(turnMsgs []Message) bool {
	for _, m := range turnMsgs {
		if len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func (cs *CortexSession) captureTurn(userMsg string, turnMsgs []Message) {
	if cs.capturer == nil || strings.TrimSpace(userMsg) == "" {
		return
	}
	outcome, answer := turnArtifacts(turnMsgs)
	summary := userMsg
	if outcome != "" {
		summary += "\n[" + outcome + "]"
	}
	if answer != "" {
		if len(answer) > captureExcerptCap {
			answer = answer[:captureExcerptCap] + "…"
		}
		summary += "\n→ " + answer
	}
	if err := cs.capturer.CaptureEvent(&events.Event{
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": userMsg},
		ToolResult: summary,
		Context:    events.EventContext{SessionID: cs.SessionID, ProjectPath: contextDir()},
		Metadata:   map[string]any{"verified": turnUsedTools(turnMsgs)},
	}); err == nil {
		cs.captures++
	}
}

func (cs *CortexSession) Close() {
	if cs.transcript != nil {
		cs.transcript.Close()
		cs.transcript = nil
	}
}

func (cs *CortexSession) contextStrategy() string {
	if cs.memory != nil {
		return "memory"
	}
	return "none"
}

func (cs *CortexSession) sessionSummary() string {
	dur := time.Since(cs.sessionStart).Round(time.Second)
	cost := ""
	if cs.costUSD > 0 {
		cost = " · " + humanCost(cs.costUSD)
	}
	header := fmt.Sprintf("%d turns · %s", cs.turns, dur)
	body := fmt.Sprintf("%s in / %s out%s · %d captured · %d memory injections",
		humanK(cs.tokensIn), humanK(cs.tokensOut), cost,
		cs.captures, cs.injections)
	return header + "\n" + body
}

func humanCost(c float64) string { return loopui.HumanCost(c) }

func (cs *CortexSession) emitSessionMetrics() {
	if cs.SessionID == "" {
		return
	}
	p := journal.EvalCellResultPayload{
		SchemaVersion:         "1",
		RunID:                 cs.SessionID,
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		ScenarioID:            "repl-session",
		Harness:               "loop",
		Provider:              "openai-compat",
		Model:                 cs.Request.Model,
		Backend:               cs.Request.BaseURL,
		ContextStrategy:       cs.contextStrategy(),
		CortexVersion:         version(),
		Temperature:           cs.Request.Temperature,
		TokensIn:              cs.tokensIn,
		TokensOut:             cs.tokensOut,
		InjectedContextTokens: cs.injectedChars / 4,
		LatencyMs:             time.Since(cs.sessionStart).Milliseconds(),
		AgentTurnsTotal:       cs.turns,
		Notes: fmt.Sprintf("captures=%d injections=%d",
			cs.captures, cs.injections),
	}
	entry, err := journal.NewEvalCellResultEntry(p)
	if err != nil {
		return
	}
	w, err := journal.NewWriter(journal.WriterOpts{
		ClassDir: filepath.Join(contextDir(), "journal", "eval"),
		Fsync:    journal.FsyncPerBatch,
	})
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = w.Append(entry)
}

func humanK(n int) string { return loopui.HumanK(n) }

func ctxColor(used, max int) string { return loopui.ContextColor(used, max, compactThreshold) }
