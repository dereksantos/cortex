// discord_risk_test.go — Phase 7 sub-item 3's interactive risk approval:
// approve/deny/timeout, table-driven where the resolution path allows it.
package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRiskApprovalsAskPostsButtonsAndTimesOut proves the un-resolved path:
// no button click, no text reply — the prompt lapses to approved=false,
// timedOut=true, which gateShell (tool_deps.go) uses to reproduce its exact
// headless-Blocked message. Also asserts the pending prompt is cleaned up
// and the buttons get disabled (best-effort UX, not load-bearing).
func TestRiskApprovalsAskPostsButtonsAndTimesOut(t *testing.T) {
	api := newFakeDiscordAPI()
	ra := newRiskApprovals()

	approved, timedOut := ra.ask(context.Background(), api, "chan1", "rm is destructive", "rm -rf build/", 20*time.Millisecond)

	if approved {
		t.Error("approved = true on an unresolved prompt, want false")
	}
	if !timedOut {
		t.Error("timedOut = false after the timeout elapsed, want true")
	}
	if _, ok := ra.pending["chan1"]; ok {
		t.Error("prompt still registered as pending after timeout — ask must clean it up")
	}

	prompts := api.sentComplexTo("chan1")
	if len(prompts) != 1 {
		t.Fatalf("sentComplexTo(chan1) = %d messages, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0].Content, "rm is destructive") {
		t.Errorf("prompt content = %q, missing the risk reason", prompts[0].Content)
	}
	if !strings.Contains(prompts[0].Content, "rm -rf build/") {
		t.Errorf("prompt content = %q, missing the command", prompts[0].Content)
	}
	if len(prompts[0].Components) == 0 {
		t.Error("prompt has no components — expected approve/deny buttons")
	}
	if len(api.editsComplex) != 1 {
		t.Errorf("editsComplex = %d, want 1 (buttons disabled on resolution)", len(api.editsComplex))
	}
}

// TestRiskApprovalsAskCtxCancelAlsoTimesOut proves a /stop or 🛑-reaction
// interrupt (which cancels the turn's ctx — discord.go's runTurn) while an
// approval is pending resolves it the same way a timeout does: no answer
// arrived.
func TestRiskApprovalsAskCtxCancelAlsoTimesOut(t *testing.T) {
	api := newFakeDiscordAPI()
	ra := newRiskApprovals()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	approved, timedOut := ra.ask(ctx, api, "chan1", "reason", "cmd", time.Second)
	if approved || !timedOut {
		t.Errorf("ask() after ctx cancel = (approved=%v, timedOut=%v), want (false, true)", approved, timedOut)
	}
}

// TestRiskApprovalsAskResolvedByButtonOrText drives ask() concurrently and
// resolves it from the other goroutine via the two real resolution paths
// (button click, text reply) — the decision channel is buffered 1 so no
// synchronization beyond a bounded poll for prompt registration is needed.
func TestRiskApprovalsAskResolvedByButtonOrText(t *testing.T) {
	tests := []struct {
		name         string
		resolve      func(ra *riskApprovals, channelID string) bool
		wantApproved bool
	}{
		{"button approve", func(ra *riskApprovals, ch string) bool { return ra.resolveButton(ch, true) }, true},
		{"button deny", func(ra *riskApprovals, ch string) bool { return ra.resolveButton(ch, false) }, false},
		{"text yes", func(ra *riskApprovals, ch string) bool { return ra.resolveText(ch, "yes") }, true},
		{"text y", func(ra *riskApprovals, ch string) bool { return ra.resolveText(ch, "y") }, true},
		{"text no", func(ra *riskApprovals, ch string) bool { return ra.resolveText(ch, "no") }, false},
		{"text approve", func(ra *riskApprovals, ch string) bool { return ra.resolveText(ch, "Approve") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeDiscordAPI()
			ra := newRiskApprovals()
			const channelID = "chan1"

			type result struct{ approved, timedOut bool }
			resCh := make(chan result, 1)
			go func() {
				approved, timedOut := ra.ask(context.Background(), api, channelID, "reason", "cmd", 5*time.Second)
				resCh <- result{approved, timedOut}
			}()

			deadline := time.Now().Add(2 * time.Second)
			for !tt.resolve(ra, channelID) {
				if time.Now().After(deadline) {
					t.Fatal("prompt never became pending — resolve never succeeded")
				}
				time.Sleep(time.Millisecond)
			}

			select {
			case res := <-resCh:
				if res.timedOut {
					t.Error("timedOut = true on an explicitly resolved prompt, want false")
				}
				if res.approved != tt.wantApproved {
					t.Errorf("approved = %v, want %v", res.approved, tt.wantApproved)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("ask() did not return after resolution")
			}
		})
	}
}

// TestRiskApprovalsResolveTextIgnoresOrdinaryMessages proves resolveText
// only consumes a bare yes/no/approve/deny reply — anything else (including
// when no prompt is pending at all) falls through so an ordinary chat
// message is never mistaken for an approval.
func TestRiskApprovalsResolveTextIgnoresOrdinaryMessages(t *testing.T) {
	ra := newRiskApprovals()
	if ra.resolveText("chan1", "yes") {
		t.Error("resolveText consumed a reply with no pending prompt")
	}

	ra.pending["chan1"] = &riskPrompt{resolve: make(chan riskDecision, 1)}
	if ra.resolveText("chan1", "sure, go ahead") {
		t.Error("resolveText consumed free-text that isn't yes/no/approve/deny")
	}
	if _, ok := ra.pending["chan1"]; !ok {
		t.Error("an unmatched reply must leave the prompt pending")
	}
}

// TestRiskApprovalsResolveButtonReportsAlreadyResolved proves resolveButton
// distinguishes "resolved" from "nothing pending" — a slow click after
// timeout must not silently look like a fresh approval.
func TestRiskApprovalsResolveButtonReportsAlreadyResolved(t *testing.T) {
	ra := newRiskApprovals()
	if ra.resolveButton("chan1", true) {
		t.Error("resolveButton reported success with no pending prompt")
	}
}

// TestParseRiskCustomID round-trips riskCustomID for both button kinds and
// rejects anything else (e.g. an application-command interaction's data
// landing in the same handler by mistake).
func TestParseRiskCustomID(t *testing.T) {
	if ch, approve, ok := parseRiskCustomID(riskCustomID("chan42", true)); !ok || ch != "chan42" || !approve {
		t.Errorf("approve id round-trip = (%q, %v, %v), want (chan42, true, true)", ch, approve, ok)
	}
	if ch, approve, ok := parseRiskCustomID(riskCustomID("chan42", false)); !ok || ch != "chan42" || approve {
		t.Errorf("deny id round-trip = (%q, %v, %v), want (chan42, false, true)", ch, approve, ok)
	}
	if _, _, ok := parseRiskCustomID("unrelated-component-id"); ok {
		t.Error("parseRiskCustomID matched an id it shouldn't own")
	}
}
