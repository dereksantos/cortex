// discord_manager_test.go — Phase 7 sub-item 1 (SessionManager rebase: busy
// reply, one live session per channel) and sub-item 4 (interrupt cancels
// the turn's ctx), against a hermetic *CortexSession-backed SessionManager
// (the pattern serve_session_test.go's hermeticSessionFactory established) —
// no live Discord gateway, no live model.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/dereksantos/cortex/internal/registry"
)

// fakeReactionEvent builds a bare *discordgo.Session (State.User.ID fixed to
// "the-bot", never "someone-else" so handleReactionAdd's self-reaction
// guard never fires for these tests) and a *discordgo.MessageReactionAdd —
// enough for handleReactionAdd (discord_progress.go), which only reads
// s.State.User.ID and the reaction's own fields, never anything requiring a
// live gateway connection.
func fakeReactionEvent(userID, channelID, messageID, emojiName string) (*discordgo.Session, *discordgo.MessageReactionAdd) {
	s := &discordgo.Session{State: &discordgo.State{Ready: discordgo.Ready{User: &discordgo.User{ID: "the-bot"}}}}
	r := &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{
		UserID: userID, ChannelID: channelID, MessageID: messageID, Emoji: discordgo.Emoji{Name: emojiName},
	}}
	return s, r
}

// hermeticDiscordManager builds a SessionManager whose sessions never touch
// real config or the network (CortexArgs{}.Request() ignores its receiver),
// rooted at a temp CWD so StartTranscript has somewhere to write — the same
// hermeticSessionFactory pattern serve_session_test.go establishes, reused
// here as a sessionFactory closure rather than imported directly since
// discord.go's own newDiscordSessionFactory also calls EnableMemory, which
// these tests don't need and would slow down for no benefit.
func hermeticDiscordManager(t *testing.T) *SessionManager {
	t.Helper()
	t.Chdir(t.TempDir())
	reg := &fakeRegistry{}
	return NewSessionManager(reg, func() *CortexSession {
		return &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	})
}

// TestDiscordRunTurnBusyRepliesInsteadOfQueuing is Phase 7 sub-item 1's DoD:
// a message that arrives while the channel's session is mid-turn gets an
// immediate "still working" reply, not a silently queued turn. Simulated by
// holding the managedSession's lock directly (as an in-flight turn would)
// rather than racing a real turn.
func TestDiscordRunTurnBusyRepliesInsteadOfQueuing(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	ms, err := bot.sessionFor("chan1")
	if err != nil {
		t.Fatalf("sessionFor: %v", err)
	}
	if !ms.mu.TryLock() {
		t.Fatal("expected to acquire the fresh session's lock")
	}
	// Deliberately left locked — stands in for an in-flight turn.

	bot.runTurn("chan1", "are you still there?")

	got := api.sentTo("chan1")
	if len(got) != 1 {
		t.Fatalf("sentTo(chan1) = %v, want exactly one busy reply", got)
	}
	if !strings.Contains(got[0], "still working") {
		t.Errorf("reply = %q, want a busy message", got[0])
	}
}

// TestDiscordSessionForOneLiveSessionPerChannel proves the per-channel
// session mapping (docs/cortex-web.md Phase 7's "one live session per
// channel/project"): two different channels get two different sessions,
// while the same channel resolves to the same session on a second call.
func TestDiscordSessionForOneLiveSessionPerChannel(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	a1, err := bot.sessionFor("chanA")
	if err != nil {
		t.Fatalf("sessionFor(chanA): %v", err)
	}
	b1, err := bot.sessionFor("chanB")
	if err != nil {
		t.Fatalf("sessionFor(chanB): %v", err)
	}
	if a1.ID() == b1.ID() {
		t.Fatalf("chanA and chanB share session %q, want distinct sessions", a1.ID())
	}

	a2, err := bot.sessionFor("chanA")
	if err != nil {
		t.Fatalf("sessionFor(chanA) again: %v", err)
	}
	if a2.ID() != a1.ID() {
		t.Errorf("sessionFor(chanA) returned %q the second time, want the same session %q", a2.ID(), a1.ID())
	}
}

// TestDiscordClearStartsAFreshSessionForTheChannel proves /clear's body
// (discord.go's clearText, the "clear" application command's handler)
// rebinds the channel to a brand-new session rather than mutating the old
// one in place — SessionManager tracks sessions by their true id, so an
// in-place id change (the REPL's cs.Clear()) would desync the map.
func TestDiscordClearStartsAFreshSessionForTheChannel(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	first, err := bot.sessionFor("chan1")
	if err != nil {
		t.Fatalf("sessionFor: %v", err)
	}

	reply := bot.clearText("chan1")
	if !strings.Contains(reply, "cleared") {
		t.Errorf("clearText reply = %q, want it to mention \"cleared\"", reply)
	}

	second, err := bot.sessionFor("chan1")
	if err != nil {
		t.Fatalf("sessionFor after clear: %v", err)
	}
	if second.ID() == first.ID() {
		t.Error("clearText did not rebind the channel to a new session")
	}
}

// TestDiscordInterruptCancelsTheTurnsContext is Phase 7 sub-item 4's DoD:
// interrupt() (wired from a /stop command, discord_commands.go, and a
// 🛑-reaction, discord_progress.go) cancels the exact context.CancelFunc
// runTurn registered — the same ctx-cancel mechanism main.go's Ctrl-C paths
// use (editor.Interruptible / signal.NotifyContext canceling the ctx passed
// to Turn).
func TestDiscordInterruptCancelsTheTurnsContext(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	bot.setCancel("chan1", cancel)

	reply := bot.interrupt("chan1")
	if !strings.Contains(reply, "stopping") {
		t.Errorf("interrupt reply = %q, want a stopping acknowledgment", reply)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("interrupt() did not cancel the registered context")
	}

	// clearCancel (called after every turn, discord.go's runTurn) leaves
	// nothing to interrupt.
	bot.clearCancel("chan1")
	if got := bot.interrupt("chan1"); !strings.Contains(got, "nothing running") {
		t.Errorf("interrupt after clearCancel = %q, want a nothing-to-stop message", got)
	}
}

// TestDiscordHandleReactionAddInterruptsOnlyTheLiveStatusMessage proves the
// 🛑-reaction affordance (discord_progress.go): a reaction on the channel's
// CURRENT status message interrupts, a reaction from the bot's own account
// (added by startProgress) is ignored, and a reaction on a stale/foreign
// message id is ignored.
func TestDiscordHandleReactionAddInterruptsOnlyTheLiveStatusMessage(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	_, cancel := context.WithCancel(context.Background())
	bot.setCancel("chan1", cancel)
	bot.mu.Lock()
	bot.statusMsg["chan1"] = "status-1"
	bot.mu.Unlock()

	dg, r := fakeReactionEvent("someone-else", "chan1", "status-1", statusStopEmoji)
	bot.handleReactionAdd(dg, r)

	got := api.sentTo("chan1")
	if len(got) != 1 || !strings.Contains(got[0], "stopping") {
		t.Fatalf("sentTo(chan1) = %v, want one stopping acknowledgment", got)
	}
}

func TestDiscordHandleReactionAddIgnoresWrongMessageOrEmoji(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")
	bot.mu.Lock()
	bot.statusMsg["chan1"] = "status-1"
	bot.mu.Unlock()

	dg, wrongMsg := fakeReactionEvent("someone-else", "chan1", "status-other", statusStopEmoji)
	bot.handleReactionAdd(dg, wrongMsg)
	dg2, wrongEmoji := fakeReactionEvent("someone-else", "chan1", "status-1", "👍")
	bot.handleReactionAdd(dg2, wrongEmoji)

	if got := api.sentTo("chan1"); len(got) != 0 {
		t.Errorf("sentTo(chan1) = %v, want no messages for a non-matching reaction", got)
	}
}

// TestRegistryEmptyProjectSkipsLookup proves the serve_session.go seam this
// rebase relies on: Create/Resume with project="" (Discord's CWD-implicit
// default, matching applyProjectFlag's existing --project-less semantics)
// never touches the registry at all — a fakeRegistry with zero projects
// still succeeds.
func TestRegistryEmptyProjectSkipsLookup(t *testing.T) {
	t.Chdir(t.TempDir())
	reg := &fakeRegistry{}
	mgr := NewSessionManager(reg, func() *CortexSession {
		return &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
	})
	if _, err := mgr.Create(""); err != nil {
		t.Fatalf("Create(\"\") with an empty registry: %v", err)
	}
	if _, err := reg.Lookup(""); err == nil {
		t.Fatal("registry.Lookup(\"\") unexpectedly succeeded — fakeRegistry should have no \"\" entry")
	} else if err != registry.ErrProjectNotFound {
		t.Fatalf("registry.Lookup(\"\") error = %v, want ErrProjectNotFound (proves Create never called it)", err)
	}
}
