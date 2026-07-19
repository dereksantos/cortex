// discord_progress.go — Phase 7 sub-item 4: progress streamed as edits to a
// single status message (the same Progress seam serve_stream.go's SSE
// handler fans into "progress" events — cmd/cortex/loop.go's runLoop), plus
// the 🛑-reaction interrupt affordance wired to the same ctx-cancel path a
// /stop command (discord_commands.go) or Ctrl-C (main.go's
// editor.Interruptible / signal.NotifyContext) uses: canceling the context
// TurnWithProgress runs on.
package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// progressEditInterval throttles status-message edits so a chatty turn
// doesn't trip Discord's per-channel edit rate limit; the final state is
// always posted regardless of throttling (finishProgress bypasses it).
const progressEditInterval = 1500 * time.Millisecond

// statusStopEmoji is the reaction a user adds to the live status message to
// interrupt the turn — the message-based analog of a slash /stop for
// clients that find tapping a reaction faster than typing a command.
const statusStopEmoji = "🛑"

// progressEditor throttles a single status message's edits to at most one
// per interval, always emitting the latest line eventually via finish. Not
// safe for concurrent update calls from multiple goroutines beyond the one
// runTurn drives — Progress, like the rest of a turn, is single-goroutine.
type progressEditor struct {
	api       discordAPI
	channelID string
	messageID string
	interval  time.Duration
	now       func() time.Time
	mu        sync.Mutex
	last      time.Time
}

func (p *progressEditor) update(line string) {
	if p.messageID == "" || line == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.last.IsZero() && now.Sub(p.last) < p.interval {
		return
	}
	p.last = now
	if _, err := p.api.ChannelMessageEdit(p.channelID, p.messageID, renderProgressLine(line)); err != nil {
		log.Printf("discord: progress edit failed: %v", err)
	}
}

func (p *progressEditor) finish(line string) {
	if p.messageID == "" {
		return
	}
	if _, err := p.api.ChannelMessageEdit(p.channelID, p.messageID, line); err != nil {
		log.Printf("discord: final progress edit failed: %v", err)
	}
}

func renderProgressLine(line string) string {
	return fmt.Sprintf("⏳ %s", line)
}

// startProgress posts the initial status message and arms it with the
// stop-reaction affordance, returning its message id ("" on send failure —
// callers degrade to no progress display rather than failing the turn).
func (b *discordBot) startProgress(channelID string) string {
	msg, err := b.api.ChannelMessageSend(channelID, "⏳ working…")
	if err != nil {
		log.Printf("discord: failed to post progress status message: %v", err)
		return ""
	}
	if err := b.api.MessageReactionAdd(channelID, msg.ID, statusStopEmoji); err != nil {
		log.Printf("discord: failed to arm the stop reaction: %v", err)
	}
	b.mu.Lock()
	b.statusMsg[channelID] = msg.ID
	b.mu.Unlock()
	return msg.ID
}

// progressSink adapts a channel's status message into the Progress seam
// (cmd/cortex/loop.go's runLoop) TurnWithProgress runs.
func (b *discordBot) progressSink(channelID, statusID string) Progress {
	ed := &progressEditor{api: b.api, channelID: channelID, messageID: statusID, interval: progressEditInterval, now: time.Now}
	return func(line string) { ed.update(line) }
}

// finishProgress replaces the status message's content with a final marker
// and forgets it as the channel's live stop-reaction target — a reaction
// added after this point has nothing in flight to cancel.
func (b *discordBot) finishProgress(channelID, statusID string) {
	if statusID == "" {
		return
	}
	(&progressEditor{api: b.api, channelID: channelID, messageID: statusID}).finish("✓ done")
	b.mu.Lock()
	if b.statusMsg[channelID] == statusID {
		delete(b.statusMsg, channelID)
	}
	b.mu.Unlock()
}

// handleReactionAdd interrupts the channel's in-flight turn when someone
// (not the bot itself) adds statusStopEmoji to that channel's live status
// message — the same ctx-cancel path a /stop command (discord_commands.go)
// or Ctrl-C (main.go) uses.
func (b *discordBot) handleReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if r.Emoji.Name != statusStopEmoji {
		return
	}
	if s.State != nil && s.State.User != nil && r.UserID == s.State.User.ID {
		return // the bot's own reaction, added by startProgress
	}
	b.mu.Lock()
	live := b.statusMsg[r.ChannelID] == r.MessageID
	b.mu.Unlock()
	if !live {
		return
	}
	b.send(r.ChannelID, b.interrupt(r.ChannelID))
}
