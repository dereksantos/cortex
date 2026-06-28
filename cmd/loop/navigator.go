package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/projectindex"
	"github.com/dereksantos/cortex/internal/tools"
)

// navigator.go is the study tool (study-navigator direction doc): a bounded
// read-only subagent that leads with a free structural map of the target, then
// reads ONLY the goal-relevant regions in tiny ranges — and can spawn a bounded
// sub-study to delegate a neighbor file/subdir. It replaced the byte-grid
// sampling engine (now removed). Compaction and shell-output study use the
// free-text summarizer instead — they have no structural map to navigate.

// navMaxIterations bounds the navigator's read loop — how many rounds of
// tool-calling it gets before it must answer from what it has read. Small on
// purpose: the whole point is a few targeted reads, not exhaustive coverage.
const navMaxIterations = 10

// navReadBudgetBytes caps the total tool output the navigator accumulates before
// it must answer. Without it, a bulk-reading study model (e.g. a coder model
// pointed at the study role) pulls a large file 800 lines at a time, re-sending
// the whole grown history each round — context balloons with no ceiling. ~96 KB
// (~24K tokens) is ample to ground an answer while keeping the call cheap and
// fast regardless of the study model's window. The iteration cap and this byte
// cap are independent ceilings; whichever trips first forces the digest.
const navReadBudgetBytes = 96000

// navReadLines caps how many lines one navigator read_file pulls. The map hands
// the navigator exact line-spans, so it should read tight regions, not 800-line
// swaths (the coder's read_file cap); a smaller cap spreads the read budget
// across more targeted reads instead of spending it on one giant block.
const navReadLines = 200

// navMaxDepth bounds study-spawns-study recursion: a navigator may delegate a
// sub-study (study(path, goal)) to cover a neighbor file or subdirectory, but
// only this many levels deep, so a fan-out can't run away.
const navMaxDepth = 2

// navMapBudget caps the seed map so orientation never dominates the navigator's
// own context. Sized to hold a large single file's full declaration skeleton
// (~one compact line per decl) — truncating it hides the file's back half and
// forces sequential scanning instead of targeted reads (measured on
// cmd/loop/main.go, whose 7.3KB skeleton was clipped at 6KB). A huge directory
// tree still truncates, with a note to project_index a subdirectory to drill in.
const navMapBudget = 16000

// navTools is the navigator's surface: orient with project_index (map), pull
// exact ranges with read_file, and delegate a whole neighbor file/subdir to a
// sub-study with study. No write/edit/bash/remove — navigation reads, it does
// not mutate.
var navTools = []Tool{tools.ReadFile, tools.ProjectIndexTool, tools.StudyTool}

// navAllowed is the execution allowlist matching navTools — a defensive guard
// so a model that hallucinates a call to a tool it wasn't offered (write_file,
// bash, …) gets a refusal observation instead of an actual mutation.
var navAllowed = map[string]bool{
	tools.FunctionReadFile:     true,
	tools.FunctionProjectIndex: true,
	tools.FunctionStudy:        true,
}

const navigatorSystem = `You are a code navigator. You are given a structural MAP of a path and a GOAL. Read the parts relevant to the goal, then answer it.

- The map lists each declaration or section with its line span, e.g. "L40-92". Read one with read_file(path, 40, 92). Use project_index(path) to map a file or subdirectory you haven't seen yet.
- To understand a whole NEIGHBOR file or subdirectory at once, call study(path, goal) — it returns a digest of that path so you don't read it all yourself. Use it for breadth (a package, a big related file); read ranges yourself for the path you were given.
- Read narrowly — pull the few parts the goal needs, not whole files or irrelevant regions. You have a limited read budget; spend it on what matters, then stop.

When you've seen enough, stop calling tools and answer the GOAL: explain clearly and concretely how the relevant code works and how the pieces fit together. Mention file:line where it helps the reader find the code. Base your answer only on what you actually read.`

// runNavigator runs the bounded read loop and returns the final digest. It
// builds its own request against the STUDY model (small, long-context) with the
// narrow nav tool set — separate from the main conversation's request, so the
// navigation never pollutes the coder's history. depth is the study-spawns-study
// nesting level (0 at the top); it bounds recursion via navMaxDepth.
func (cs *CortexSession) runNavigator(ctx context.Context, path, goal string, depth int) (string, error) {
	if strings.TrimSpace(goal) == "" {
		goal = "Summarize what this code does and how its parts fit together."
	}
	if !cs.quiet {
		fmt.Println(withColor(fmt.Sprintf("  ▸ navigate(%s) via %s", path, cs.Study.Model), green))
	}

	req := &AgentRequest{
		Model:              cs.Study.Model,
		BaseURL:            cs.Study.Endpoint,
		APIKey:             resolveKey(cs.Study),
		ChatTemplateKwargs: cs.Study.TemplateKwargs(),
		Temperature:        0,
		MaxTokens:          cs.Study.maxOut(studyMaxOutputTokens), // bound output; never unbounded
		Tools:              navTools,
		Messages: []Message{
			{Role: RoleSystem, Content: navigatorSystem},
			{Role: RoleUser, Content: navSeed(path, goal)},
		},
	}

	var lastSig string
	var repeats int
	var readBytes int // total tool output accumulated; bounds context growth
	for i := 0; i < navMaxIterations; i++ {
		res, err := req.Send(ctx)
		if err != nil {
			return "", fmt.Errorf("navigator send: %w", err)
		}
		if len(res.Choices) == 0 {
			return "", fmt.Errorf("navigator: no choices in response")
		}
		cs.tokensIn += res.Usage.PromptTokens
		cs.tokensOut += res.Usage.CompletionTokens
		cs.costUSD += res.Usage.Cost

		msg := res.Choices[0].Message
		// Recover Qwen-native XML tool calls the proxy didn't normalize, same as
		// the main loop — otherwise a tool call leaks as prose and reads as a
		// (premature) final answer.
		if len(msg.ToolCalls) == 0 {
			if calls := tools.ParseXMLToolCalls(msg.Content); len(calls) > 0 {
				msg.ToolCalls = calls
				msg.Content = tools.StripToolMarkup(msg.Content)
			}
		}
		req.Messages = append(req.Messages, msg)

		// No tool calls → the model answered. That prose IS the digest.
		if len(msg.ToolCalls) == 0 {
			return strings.TrimSpace(msg.Content), nil
		}

		// No-progress guard: a weak model can re-issue the identical read
		// forever. Once it repeats past the cap, force a synthesis.
		if sig := toolCallSignature(msg.ToolCalls); sig == lastSig {
			repeats++
		} else {
			lastSig, repeats = sig, 1
		}
		if repeats >= maxRepeatedToolCalls {
			break
		}

		for _, call := range msg.ToolCalls {
			out := cs.runNavTool(ctx, call, depth)
			readBytes += len(out)
			req.Messages = append(req.Messages, Message{
				Role:       RoleTool,
				ToolCallID: call.ID,
				Content:    out,
			})
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Context budget spent: stop reading and answer from what's gathered, so a
		// bulk-reading model can't keep growing the context past the ceiling.
		if readBytes >= navReadBudgetBytes {
			break
		}
	}

	// Read budget exhausted (iteration cap, byte cap, or stuck repeating) while
	// still calling tools: ask for the digest now, with tools withheld so it must
	// answer from what it has.
	return cs.navFinalize(ctx, req)
}

// runNavTool executes one navigator tool call, refusing anything outside the
// allowlist (defense against a hallucinated write/bash call). A study call is a
// bounded recursive spawn — a sub-navigator over the named path — capped at
// navMaxDepth so a fan-out can't run away. Everything else dispatches normally.
func (cs *CortexSession) runNavTool(ctx context.Context, call ToolCall, depth int) string {
	name := call.Function.Name
	if !navAllowed[name] {
		return fmt.Sprintf("Error: %q is not available to the navigator — use read_file, project_index, or study.", name)
	}
	if name == tools.FunctionStudy {
		path, err := call.StringArg("path")
		if err != nil {
			return "Error: " + err.Error()
		}
		if depth >= navMaxDepth {
			return fmt.Sprintf("Error: max study depth (%d) reached — read %s directly with read_file instead of spawning another study.", navMaxDepth, path)
		}
		goal, _ := call.StringArg("goal")
		digest, err := cs.runNavigator(ctx, path, goal, depth+1)
		if err != nil {
			return "Error: " + err.Error()
		}
		return digest
	}
	out, err := tools.Execute(ctx, clampNavRead(call), cs)
	if err != nil {
		return "Error: " + err.Error()
	}
	return out
}

// clampNavRead shrinks an over-wide read_file range so one navigator read can't
// pull a huge swath of a file and blow the read budget on a single call. Only
// ranged reads are clamped (to navReadLines); a whole-file read passes through
// and still hits read_file's size gate (→ a declaration skeleton, not the file).
// Non-read_file calls are returned unchanged.
func clampNavRead(call ToolCall) ToolCall {
	if call.Function.Name != tools.FunctionReadFile {
		return call
	}
	start, ok := call.IntArg("start")
	if !ok || start <= 0 {
		return call // whole-file read: the size gate handles it
	}
	end, hasEnd := call.IntArg("end")
	if !hasEnd || end < start || end-start+1 > navReadLines {
		end = start + navReadLines - 1
	}
	path, _ := call.StringArg("path")
	args, err := json.Marshal(map[string]any{"path": path, "start": start, "end": end})
	if err != nil {
		return call
	}
	call.Function.Arguments = string(args)
	return call
}

// navFinalize requests a final digest with the tool set withheld, so a model
// that ran out of read budget still produces an answer grounded in what it read
// rather than returning nothing.
func (cs *CortexSession) navFinalize(ctx context.Context, req *AgentRequest) (string, error) {
	req.Tools = nil
	req.Messages = append(req.Messages, Message{
		Role:    RoleUser,
		Content: "You've reached the read budget. Stop reading and write the digest now from what you've seen, citing @path:start-end for every claim.",
	})
	res, err := req.Send(ctx)
	if err != nil {
		return "", fmt.Errorf("navigator finalize: %w", err)
	}
	if len(res.Choices) == 0 {
		return "", fmt.Errorf("navigator: no choices in finalize response")
	}
	cs.tokensIn += res.Usage.PromptTokens
	cs.tokensOut += res.Usage.CompletionTokens
	cs.costUSD += res.Usage.Cost
	return strings.TrimSpace(res.Choices[0].Message.Content), nil
}

// navSeed builds the navigator's opening user message: the goal, the path, and
// the structural map (bounded) the navigation starts from.
func navSeed(path, goal string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL: %s\nPATH: %s\n\n", goal, path)
	if m := navMap(path); m != "" {
		b.WriteString("MAP (each entry's line span is what you read with read_file):\n\n")
		b.WriteString(m)
		b.WriteString("\n")
	} else {
		b.WriteString("No structural map was available for this path; use read_file(path, start, end) to read regions and project_index to orient.\n")
	}
	return b.String()
}

// navMap renders the bounded structural map seed for path, or "" when it can't
// be built (a missing path — the navigator can still read_file by name).
func navMap(path string) string {
	ix, err := projectindex.Build(path)
	if err != nil {
		return ""
	}
	m := ix.Render()
	if len(m) > navMapBudget {
		m = m[:navMapBudget] + "\n… (map truncated; project_index a subdirectory to drill in)"
	}
	return m
}

// Navigate is the study tool: the map-first navigator over a path. (Compaction
// and shell-output study use the free-text summarizer instead — they have no
// structural map to navigate.)
func (cs *CortexSession) Navigate(ctx context.Context, path, goal string) (string, error) {
	return cs.runNavigator(ctx, path, goal, 0)
}
