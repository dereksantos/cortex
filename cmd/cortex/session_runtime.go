package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/loopui"
	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/userhome"
	"github.com/dereksantos/cortex/pkg/config"
	"github.com/dereksantos/cortex/pkg/events"
	"github.com/dereksantos/cortex/pkg/llm"
)

// EnableMemory wires both memory tiers (docs/cross-source-learning.md piece
// 1): the project-tier store under this project's .cortex/memory (unchanged
// from before this doc — cs.memory) and the user-tier store at
// ~/.cortex/memory (internal/userhome.Path — cs.userMemory), the SAME
// internal/memory.Store implementation pointed at a different root, shared
// by every project on the machine. A failure to resolve either root (e.g. no
// writable home directory) leaves that tier nil, which every memory tool and
// memoryIndexNote already treat as "unavailable" rather than a fatal error.
func (cs *CortexSession) EnableMemory() {
	dir := cs.ContextDir()
	if mem, err := memory.New(dir); err == nil {
		cs.memory = mem
	}
	if userDir, err := userhome.Path("memory"); err == nil {
		if userMem, err := memory.New(userDir); err == nil {
			cs.userMemory = userMem
		}
	}
	cfg := &config.Config{ContextDir: dir, ProjectRoot: filepath.Dir(dir)}
	cs.capturer = capture.New(cfg)
}

// resolveEmbedder resolves the `embed` role to a remote OpenAI-compatible
// embedder, or nil when the role is unbound. Nil is the normal case: nothing
// wires an embedder into capture today, and memory_search is text-based.
//
// The in-process Hugot embedder that used to back this as a local default was
// removed — it downloaded an ONNX model on first use to serve a semantic-search
// path that no caller reached, and dragged x/crypto/ssh and x/net through
// hugot.DownloadModel for the privilege. The remote path below is the seam a
// future semantic memory_search would build on; see docs/memory-tools.md.
func (cs *CortexSession) resolveEmbedder() llm.Embedder {
	return cs.newSpecEmbedder(cs.Config.resolveBinding(roleEmbed, cs.Fleet))
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

// Bounds for the web-tool artifact lines turnArtifacts records — the same
// "bounded" discipline captureExcerptCap already applies to the final
// answer, extended to web_search/fetch_url so a large page or a chatty
// result list can't blow up the capture summary (docs/cross-source-learning.md
// piece 3's second capture-prerequisite fix).
const (
	webArtifactTitleCapChars   = 80 // per-result title/URL cap in a searched: line
	webArtifactResultsShown    = 3  // top-N search results recorded per call
	webArtifactExcerptCapChars = 200
)

func turnArtifacts(turnMsgs []Message) (outcome, answer string) {
	results := toolResultsByID(turnMsgs)
	var files, cmds, searches, fetches []string
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
			case FunctionWebSearch:
				if q, err := tc.StringArg("query"); err == nil && q != "" && !seen["sw:"+q] {
					seen["sw:"+q] = true
					searches = append(searches, formatWebSearchArtifact(q, results[tc.ID]))
				}
			case FunctionFetchURL:
				if u, err := tc.StringArg("url"); err == nil && u != "" && !seen["fu:"+u] {
					seen["fu:"+u] = true
					fetches = append(fetches, formatFetchURLArtifact(u, results[tc.ID]))
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
	if len(searches) > 0 {
		parts = append(parts, "searched: "+strings.Join(searches, " | "))
	}
	if len(fetches) > 0 {
		parts = append(parts, "fetched: "+strings.Join(fetches, " | "))
	}
	return strings.Join(parts, " | "), answer
}

// toolResultsByID indexes turnMsgs' tool-result messages by ToolCallID.
// web_search/fetch_url are the only calls whose capture-worthy detail
// (result count, titles/URLs, response size) lives in what the tool
// returned rather than in the call's own arguments — files/cmds only need
// the args (path/command), so they never needed this lookup.
func toolResultsByID(turnMsgs []Message) map[string]string {
	out := make(map[string]string)
	for _, m := range turnMsgs {
		if m.Role == RoleTool && m.ToolCallID != "" {
			out[m.ToolCallID] = m.Content
		}
	}
	return out
}

// formatWebSearchArtifact renders one web_search call's capture line: the
// query plus a bounded look at what it found. result is the tool's raw
// returned text (internal/tools' formatSearchResults output: "N. Title\n
// URL\n   Snippet", blank-line separated) — parsed defensively since it's
// free text the tool owns, not a schema this package shares with it.
func formatWebSearchArtifact(query, result string) string {
	q := truncate(query, webArtifactTitleCapChars)
	result = strings.TrimSpace(result)
	if result == "" {
		return fmt.Sprintf("%q (no result captured)", q)
	}
	if result == "(no search results)" {
		return fmt.Sprintf("%q: 0 results", q)
	}
	entries := strings.Split(result, "\n\n")
	var top []string
	for i, entry := range entries {
		if i >= webArtifactResultsShown {
			break
		}
		lines := strings.SplitN(entry, "\n", 3)
		if len(lines) < 2 {
			continue
		}
		title := strings.TrimPrefix(strings.TrimSpace(lines[0]), fmt.Sprintf("%d. ", i+1))
		url := strings.TrimSpace(lines[1])
		top = append(top, fmt.Sprintf("%s (%s)", truncate(title, webArtifactTitleCapChars), url))
	}
	return fmt.Sprintf("%q: %d result(s): %s", q, len(entries), strings.Join(top, "; "))
}

// formatFetchURLArtifact renders one fetch_url call's capture line: the URL
// plus a bounded excerpt of what came back. result is the tool's raw
// returned text (internal/tools' fetchURL output — "URL: ...\nTitle:
// ...\nContent-Type: ...\n\n<extracted text>"); size is that captured text's
// length, not the original HTTP response's (fetch_url already caps that
// internally before this package ever sees it).
func formatFetchURLArtifact(rawURL, result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return fmt.Sprintf("%s (no result captured)", rawURL)
	}
	excerpt := truncate(strings.Join(strings.Fields(result), " "), webArtifactExcerptCapChars)
	return fmt.Sprintf("%s (%d bytes): %s", rawURL, len(result), excerpt)
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
		cap := cs.Config.captureExcerptCapChars()
		if len(answer) > cap {
			answer = answer[:cap] + "…"
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
		Context:    events.EventContext{SessionID: cs.SessionID, ProjectPath: cs.ContextDir()},
		// "turn" is this turn's ordinal within cs.SessionID's transcript
		// (cs.turns, already incremented above to match the value
		// writeTranscript stamped every message of this turn with —
		// see session.go's cs.Append/writeTranscript and turn.go's
		// cs.turnNo). Together with Context.SessionID it's a coordinate
		// pair back into the session transcript — cheap to carry (one
		// int), unlike storing the turn's full text a second time here.
		// learn.go's learnFullTurnText uses it to recover a turn's
		// verbatim messages when the digest's capture-summary line would
		// otherwise truncate past a durable fact.
		Metadata: map[string]any{"verified": turnUsedTools(turnMsgs), "turn": cs.turns},
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
		cost = " | " + humanCost(cs.costUSD)
	}
	header := fmt.Sprintf("%d turns | %s", cs.turns, dur)
	body := fmt.Sprintf("%s in / %s out%s | %d captured | %d memory injections",
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
		Thinking:              thinkingLabel(cs.Request.ChatTemplateKwargs),
		ReasoningTokens:       cs.reasoningTokens,
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
		ClassDir: filepath.Join(cs.ContextDir(), "journal", "eval"),
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
