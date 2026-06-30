package main

import (
	"context"
	"errors"
	"strings"
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
	turnStart := len(cs.Request.Messages)
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
	_, stats, err := runLoop(ctx, cs.coderSender(), cs.Request, ts, bounds, nil, cs.Append)
	cs.Request.EphemeralSystem = ""
	cs.turns++
	cs.tokensIn += stats.InputTokens
	cs.tokensOut += stats.OutputTokens
	cs.costUSD += stats.Cost
	cs.LastPromptTokens = stats.LastPromptTokens

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
