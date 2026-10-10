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
	"github.com/dereksantos/cortex/internal/redact"
	"github.com/dereksantos/cortex/internal/style"
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
	// Issue #141: a "tests changed: …" line — the harness's own view of
	// what this turn did to the project's test files (testwatch.go). It
	// rides the capture summary so the turn's journal record shows it. The
	// SAME receipt is surfaced on TurnResult (turn.go) so the caller prints
	// it to a human, and handed to the model at finalize (loop.go's
	// FinalizeHook) so its final answer accounts for it — three surfaces,
	// one fact. Best-effort: an empty snapshot (no mutating calls, or
	// nothing test-relevant) adds nothing.
	if receipt := cs.testwatchTestsReceipt(); receipt != "" {
		if outcome == "" {
			outcome = receipt
		} else {
			outcome += " | " + receipt
		}
	}
	// Issue #154: a "leftover debug: …" line — the harness's own view of
	// debug prints the turn added to production files and scratch files it
	// left behind (testwatch.go's ScanDebug). It rides the SAME capture
	// summary as the tests line (a second fact, joined by " | "), is
	// surfaced on TurnResult.DebugReceipt (turn.go), and is folded into the
	// same finalize note (loop.go's FinalizeHook) — three surfaces, one
	// fact. Best-effort: an empty snapshot (no mutating calls, or nothing
	// debug-shaped added) adds nothing.
	if receipt := cs.testwatchDebugReceipt(); receipt != "" {
		if outcome == "" {
			outcome = receipt
		} else {
			outcome += " | " + receipt
		}
	}
	// Issue #129 piece 3: a "lint: …" line — the harness's own view of the
	// turn-end lint pass (turn_lint.go: the project's lint run once over the
	// turn's distinct touched files, "all" mode + trusted only). It rides
	// the SAME capture summary (a third fact, joined by " | "), is
	// surfaced on TurnResult.LintReceipt (turn.go), and reached the model in
	// the same finalize round (turn.go's FinalizeHook) — three surfaces, one
	// fact. Best-effort: a turn with no findings adds nothing.
	if receipt := cs.lintReceipt; receipt != "" {
		if outcome == "" {
			outcome = receipt
		} else {
			outcome += " | " + receipt
		}
	}
	// Issue #219: the measurement-only turn receipt (turn_receipt.go) — the
	// harness's own measurement of what the turn left in the workspace (the
	// git diff --stat block plus the untracked files the turn created, the
	// exit codes of the verification commands the model ran, the files the
	// format hook reported a problem with). It rides the SAME capture
	// summary (a fourth fact, joined by " | "), is surfaced on
	// TurnResult.Receipt (turn.go), and persisted as a kindNote transcript
	// entry (turn.go) — the receipt is harness output and is NEVER appended
	// to the model's reply, which stays verbatim. It is the STORED receipt
	// (cs.receipt, computed by turn.go before captureTurn for a tools-ran
	// turn, or by an earlier turn if this is a captureTurn test that skipped
	// the compute) rendered here. Best-effort: a turn that measured nothing
	// adds nothing.
	if receipt := cs.receipt.render(); receipt != "" {
		if outcome == "" {
			outcome = receipt
		} else {
			outcome += " | " + receipt
		}
	}
	summary := userMsg
	if outcome != "" {
		summary += "\n[" + outcome + "]"
	}
	if answer != "" {
		// Issue #103: redact the answer BEFORE truncating it — a secret cut
		// below its minimum match length by the excerpt cap would otherwise
		// persist partly unmasked. Redact-then-truncate can only ever lose
		// the TAIL of a match (the head is intact, and for a fixed-length
		// key like sk-… that leaves no usable prefix). The truncation
		// marker is kept for untruncated answers only; a long answer that
		// was already cut by the cap does not stack a second ellipsis onto
		// the marker.
		answer, _ = redact.Redact(answer)
		cap := cs.Config.captureExcerptCapChars()
		if len(answer) > cap {
			answer = answer[:cap]
			if !strings.HasSuffix(answer, "…") {
				answer += "…"
			}
		}
		summary += "\n→ " + answer
	}
	// Issue #103: redact the event's two free-text fields — the user prompt
	// (ToolInput.user_prompt) and the whole capture summary (ToolResult, which
	// carries the user prompt + the outcome line + the answer) — right at the
	// capture seam, so a secret the agent read or echoed this turn never
	// reaches the on-disk journal. captureTurn is the single choke point for
	// the loop's captures (loop.run and cortex learn's replay re-read this
	// same journal), and it is a distinct surface from the transcript's
	// per-message redaction (session.go's writeTranscript): the live in-memory
	// turnMsgs are left verbatim, so the model can still use a value this turn
	// — only what is persisted here is masked. The counts are folded into
	// cs.redactions so they ride the turn's reported figure (TurnResult.Redactions
	// and the "redactions" metadata below) — the capture is the journal's OWN
	// masking, on top of the transcript's, so the figure a human sees (the
	// REPL/headless notice) and the one the journal records cover the journal
	// too, as docs/journal.md's invariant says.
	redactedUserPrompt, n := redact.Redact(userMsg)
	cs.redactions += n
	redactedSummary, n2 := redact.Redact(summary)
	cs.redactions += n2
	if err := cs.capturer.CaptureEvent(&events.Event{
		Source:     events.SourceGeneric,
		EventType:  events.EventToolUse,
		Timestamp:  time.Now(),
		ToolName:   "loop",
		ToolInput:  map[string]any{"type": "turn", "user_prompt": redactedUserPrompt},
		ToolResult: redactedSummary,
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
		// otherwise truncate past a durable fact. "redactions" (issue #103)
		// records how many secret patterns were masked for this turn as its
		// messages hit the transcript/journal — the per-turn count that also
		// rides TurnResult.Redactions, so the journal (and the session
		// summary, via cs.redactionsTotal) can account for the redaction a
		// review of this capture's text would otherwise see only as [REDACTED:…].
		Metadata: map[string]any{"verified": turnUsedTools(turnMsgs), "turn": cs.turns, "redactions": cs.redactions},
	}); err == nil {
		cs.captures++
	}
}

func (cs *CortexSession) Close() {
	if cs.transcript != nil {
		cs.transcript.Close()
		cs.transcript = nil
	}
	// Issue #111: session end drops the undo history — the in-memory stack and
	// the session's hidden checkpoint refs — so a closed session's refs never
	// outlive it in the git object store (best-effort, non-fatal).
	cs.clearCheckpoints()
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
	// Issue #103: the session-cumulative redaction total (cs.redactionsTotal,
	// folded in per turn in turn.go) rides the summary — the human-facing
	// "where the session reports the turn" surface — so a session that masked
	// secrets is reported at the same place turns/tokens/captured are. Hidden
	// on a zero count (the common case: no secret ever hit a persisted surface).
	redactions := ""
	if cs.redactionsTotal > 0 {
		redactions = fmt.Sprintf(" | %d secrets redacted", cs.redactionsTotal)
	}
	header := fmt.Sprintf("%d turns | %s", cs.turns, dur)
	body := fmt.Sprintf("%s in / %s out%s | %d captured | %d memory injections%s",
		humanK(cs.tokensIn), humanK(cs.tokensOut), cost,
		cs.captures, cs.injections, redactions)
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
		Notes: fmt.Sprintf("captures=%d injections=%d redactions=%d",
			cs.captures, cs.injections, cs.redactionsTotal),
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

func ctxColor(used, max int) style.Role { return loopui.ContextColor(used, max, compactThreshold) }
