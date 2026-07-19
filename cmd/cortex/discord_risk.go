// discord_risk.go — Phase 7's interactive risk approval (docs/cortex-web.md
// Phase 7, decision D3 sub-item 3): a shellrisk Risky command on the coder's
// top-level bash call posts an approve/deny prompt (buttons, since discordgo
// v0.29 supports message components — decision D12) instead of the
// headless-Blocked default (tool_deps.go's gateShell), scoped to the
// requesting channel and time-bounded. A bare "yes"/"no" reply is accepted
// as a fallback for clients without button support. Denial and timeout both
// refuse the command; only a timeout reproduces gateShell's exact
// no-approver-available message text (session_core.go's approveRisky
// docstring) — see riskApprovals.ask.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// riskApprovalTimeout is the default window a risky-command prompt stays
// open before it lapses to the headless-Blocked behavior (D3 sub-item 3:
// "default 120s").
const riskApprovalTimeout = 120 * time.Second

// riskDecision is the resolution of one pending approval.
type riskDecision int

const (
	riskApproved riskDecision = iota
	riskDenied
)

// riskPrompt is one pending approval. resolve is buffered 1 so whichever
// resolution path (button, text reply) wins a race with the other just
// drops its send rather than blocking.
type riskPrompt struct {
	resolve chan riskDecision
}

// riskApprovals tracks at most one pending risky-command approval per
// channel — a second Risky command in the same channel while one is already
// pending would need its own prompt/customID scoping to avoid ambiguity,
// which today's one-turn-at-a-time-per-session serialization (discord.go's
// managedSession.mu) already prevents: a channel's live session runs one
// turn at a time, so it can have at most one bash call, hence at most one
// pending approval, in flight.
type riskApprovals struct {
	mu      sync.Mutex
	pending map[string]*riskPrompt // channelID -> pending prompt
}

func newRiskApprovals() *riskApprovals {
	return &riskApprovals{pending: make(map[string]*riskPrompt)}
}

// riskCustomID builds the button custom_id for channelID's pending prompt;
// approve selects the approve-button id, else the deny-button id. Scoping
// the id to the channel (rather than a random token) is enough because only
// one prompt is ever pending per channel (see riskApprovals doc).
func riskCustomID(channelID string, approve bool) string {
	if approve {
		return "risk-approve:" + channelID
	}
	return "risk-deny:" + channelID
}

// parseRiskCustomID reports whether id is a risk-approval button id, the
// channel it targets, and whether it's the approve (vs deny) button.
func parseRiskCustomID(id string) (channelID string, approve bool, ok bool) {
	switch {
	case strings.HasPrefix(id, "risk-approve:"):
		return strings.TrimPrefix(id, "risk-approve:"), true, true
	case strings.HasPrefix(id, "risk-deny:"):
		return strings.TrimPrefix(id, "risk-deny:"), false, true
	default:
		return "", false, false
	}
}

// ask posts the approval prompt to channelID and blocks until a button
// click, a "yes"/"no" text reply (resolveText), the timeout elapses, or ctx
// is canceled (a /stop or 🛑-reaction interrupt during the wait — treated
// the same as a timeout: no answer arrived). approved=false, timedOut=true
// is the signal gateShell (tool_deps.go) uses to reproduce its exact
// headless-Blocked message; approved=false, timedOut=false is an explicit
// decline.
func (ra *riskApprovals) ask(ctx context.Context, api discordAPI, channelID, reason, command string, timeout time.Duration) (approved, timedOut bool) {
	if timeout <= 0 {
		timeout = riskApprovalTimeout
	}
	p := &riskPrompt{resolve: make(chan riskDecision, 1)}
	ra.mu.Lock()
	ra.pending[channelID] = p
	ra.mu.Unlock()
	defer func() {
		ra.mu.Lock()
		if ra.pending[channelID] == p {
			delete(ra.pending, channelID)
		}
		ra.mu.Unlock()
	}()

	msg, err := api.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("⚠ risky command — %s\n```\n%s\n```\nApprove within %s? (buttons, or reply yes/no)", reason, command, timeout.Round(time.Second)),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Approve", Style: discordgo.SuccessButton, CustomID: riskCustomID(channelID, true)},
				discordgo.Button{Label: "Deny", Style: discordgo.DangerButton, CustomID: riskCustomID(channelID, false)},
			}},
		},
	})
	if err != nil {
		log.Printf("discord: failed to post risk approval prompt: %v", err)
	}
	var msgID string
	if msg != nil {
		msgID = msg.ID
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case dec := <-p.resolve:
		ra.disableButtons(api, channelID, msgID)
		return dec == riskApproved, false
	case <-timer.C:
		ra.disableButtons(api, channelID, msgID)
		return false, true
	case <-ctx.Done():
		ra.disableButtons(api, channelID, msgID)
		return false, true
	}
}

// disableButtons strips the approve/deny buttons from the prompt message
// once it's resolved, so a stale click can't re-fire it. Best-effort: a
// failed edit leaves inert buttons on an already-resolved prompt, which is
// harmless (resolveButton below is a no-op once ra.pending no longer holds
// the prompt).
func (ra *riskApprovals) disableButtons(api discordAPI, channelID, messageID string) {
	if messageID == "" {
		return
	}
	empty := []discordgo.MessageComponent{}
	if _, err := api.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    channelID,
		ID:         messageID,
		Components: &empty,
	}); err != nil {
		log.Printf("discord: failed to clear risk-approval buttons: %v", err)
	}
}

// resolveText resolves channelID's pending approval from a plain-text
// "yes"/"no" reply — the fallback for clients without button support
// (D3 sub-item 3). Reports whether content was consumed as a resolution (a
// pending prompt existed and content matched); the caller should treat a
// true result as "not an ordinary chat message" and stop further handling.
func (ra *riskApprovals) resolveText(channelID, content string) bool {
	ra.mu.Lock()
	p, ok := ra.pending[channelID]
	ra.mu.Unlock()
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "yes", "y", "approve":
		trySend(p.resolve, riskApproved)
		return true
	case "no", "n", "deny":
		trySend(p.resolve, riskDenied)
		return true
	default:
		return false
	}
}

// resolveButton resolves channelID's pending approval from a button click.
// Reports whether a pending prompt existed to resolve (false means the
// prompt already lapsed or was already resolved — e.g. a slow click after
// timeout).
func (ra *riskApprovals) resolveButton(channelID string, approve bool) bool {
	ra.mu.Lock()
	p, ok := ra.pending[channelID]
	ra.mu.Unlock()
	if !ok {
		return false
	}
	dec := riskDenied
	if approve {
		dec = riskApproved
	}
	trySend(p.resolve, dec)
	return true
}

// trySend delivers dec without blocking — the channel is buffered 1, so
// this only ever drops a send when the prompt was already resolved by the
// other path in the same instant, which is fine: the first resolution wins.
func trySend(ch chan riskDecision, dec riskDecision) {
	select {
	case ch <- dec:
	default:
	}
}
