package main

import (
	"context"
	"errors"
	"strings"

	"github.com/dereksantos/cortex/internal/cache"
)

func (cs *CortexSession) startActivity(label string) {
	if cs.live != nil {
		cs.live.SetActivity(label)
	}
}

func (cs *CortexSession) stopActivity() {
	if cs.live != nil {
		cs.live.SetActivity("")
	}
}

func toolCallSignature(calls []ToolCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Function.Name)
		b.WriteByte(0)
		b.WriteString(c.Function.Arguments)
		b.WriteByte('\n')
	}
	return b.String()
}

type TurnResult struct {
	Reply       string
	Interrupted bool
}

func (cs *CortexSession) Turn(ctx context.Context, input string) (TurnResult, error) {
	// Stamp transcript entries with this turn's ordinal (resume replays them
	// into spans); cleared on exit so seed/compaction writes stay unstamped.
	cs.turnNo = cs.turns + 1
	defer func() { cs.turnNo = 0 }()

	turnStart := len(cs.Request.Messages)
	// Lazy init covers sessions built without NewCortexSession (tests, adapters):
	// the working set engages wherever turn content happens to start.
	if cs.ws == nil {
		cs.ws = cs.newWorkingSet(turnStart)
	}
	// Demote-then-send: if the hydrated tail has outgrown its watermark, move
	// the oldest turns into the outline zone (docs/context-architecture.md).
	// Labels count demoted turns monotonically (folds shrink cs.outline, so
	// its length regresses and cannot number entries).
	batch := cs.ws.DemoteBatch()
	for i, span := range batch {
		ordinal := cs.ws.Demoted() - len(batch) + i + 1
		cs.outline = append(cs.outline, turnOutlineEntry(ordinal, span, cs.Request.Messages[span.Start:span.End], cs.SessionID))
	}
	cs.foldOutlineIfNeeded(ctx)
	if len(cs.outline) > 0 || cs.outlineFolded != "" {
		cs.Request.OutlineBlock = cs.renderOutlineBlock()
	}
	cs.Request.PrefixEnd = cs.ws.Base()
	cs.Request.TailFrom = cs.ws.FrontierMsg()

	// Record the turn's span at exit no matter how the turn ends (error,
	// interrupt, panic): AddTurn enforces contiguity, so every appended
	// message must land in a span.
	defer func() {
		if end := len(cs.Request.Messages); end > turnStart {
			cs.ws.AddTurn(cache.TurnSpan{Start: turnStart, End: end, Tokens: estTurnTokens(cs.Request.Messages[turnStart:end])})
		}
	}()

	cs.Append(Message{Role: RoleUser, Content: input})
	cs.turnIntent = input

	note := cs.memoryIndexNote()
	cs.Request.EphemeralSystem = note
	if note != "" {
		cs.injections++
		cs.injectedChars += len(note)
	}

	maxTok := cs.Request.MaxTokens
	if maxTok <= 0 {
		maxTok = codeMaxOutputTokens
	}
	ts := Toolset{Tools: cs.Request.Tools, Dispatch: cs.coderDispatcher(), BeforeBatch: cs.coderBeforeBatch}
	bounds := Bounds{MaxTokens: maxTok, MaxIter: maxToolIterations}
	
	// Update display during the loop for interactive REPL
	var onStatusUpdate func(int, int)
	var onAfterToolResult func()
	if cs.live != nil {
		onStatusUpdate = func(lastPromptTokens, maxTokens int) {
			// Update the session's token count for display
			cs.LastPromptTokens = lastPromptTokens
			// Force a redraw of the prompt line
			cs.live.SetActivity("")
		}
		// After each tool result is appended, force a prompt redraw
		// to update the context gauge with the current context size
		onAfterToolResult = func() {
			cs.live.SetActivity("")
		}
	}
	ts.AfterToolResult = onAfterToolResult
	
	_, stats, err := runLoop(ctx, cs.coderSender(), cs.Request, ts, bounds, nil, cs.Append, onStatusUpdate)
	cs.Request.EphemeralSystem = ""
	cs.turns++
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.costUSD += stats.Cost
	cs.LastPromptTokens = stats.LastPromptTokens
	cs.LastCachedTokens = stats.LastCachedTokens

	if err != nil {
		return TurnResult{Interrupted: errors.Is(err, context.Canceled)}, err
	}

	turnMsgs := cs.Request.Messages[turnStart:]
	cs.captureTurn(input, turnMsgs)

	return TurnResult{Reply: lastAssistantText(turnMsgs)}, nil
}

func lastAssistantText(turnMsgs []Message) string {
	for i := len(turnMsgs) - 1; i >= 0; i-- {
		m := turnMsgs[i]
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			return m.Content
		}
	}
	return ""
}
