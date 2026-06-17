package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Discord adapter. Cortex knows Discord and nothing else: this file is the only
// place that imports discordgo, and there is no awareness of any orchestration
// layer above it — such a layer wraps the whole `loop discord` process
// externally without cortex referencing it.
//
// `loop discord` runs the bot inside the loop binary, so it holds ONE persistent
// in-memory CortexSession and calls session.Turn directly — no subprocess, no
// per-message cold start. A mutex serializes turns, which is what enforces "one
// session / one change at a time" and bounds cost. The session's transcript +
// shared .cortex/ give continuity and cross-session learning.

const (
	// discordMaxMessage is Discord's hard per-message character limit; replies
	// are chunked below it. Kept a hair under 2000 for safety.
	discordMaxMessage = 1990
	// typingRefresh re-triggers the typing indicator, which Discord clears after
	// ~10s, so a long agent turn keeps showing "Cortex is typing…".
	typingRefresh = 8 * time.Second
)

// runDiscordCLI implements `loop discord`: connect to Discord and drive one
// persistent session. Token comes from DISCORD_BOT_TOKEN (env, like the
// OpenRouter key); an optional DISCORD_CHANNEL_ID restricts the bot to one
// channel, and DISCORD_SESSION_ID resumes a specific prior session.
func runDiscordCLI() error {
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set — create a bot at https://discord.com/developers, enable the Message Content intent, and export its token")
	}
	channelID := strings.TrimSpace(os.Getenv("DISCORD_CHANNEL_ID"))

	// One persistent session for the whole bot. quiet mutes terminal emission —
	// the reply comes back via TurnResult and goes to Discord, while stderr stays
	// a clean server log.
	session := NewCortexSession()
	session.quiet = true
	if sid := strings.TrimSpace(os.Getenv("DISCORD_SESSION_ID")); sid != "" {
		if err := session.ResumeTranscript(sid); err != nil {
			log.Printf("resume %s: %v — starting fresh", sid, err)
			session.StartTranscript()
		}
	} else {
		session.StartTranscript()
	}
	session.EnableRetrieval()
	defer session.Close()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("discord session: %w", err)
	}
	// Message events (not slash commands) so a turn can take minutes without
	// hitting the 3s interaction-ack deadline. Message Content must be enabled in
	// the developer portal for the content to arrive.
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	var mu sync.Mutex // serializes Turn: one session at a time
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handleMessage(s, m, session, &mu, channelID)
	})

	if err := dg.Open(); err != nil {
		return fmt.Errorf("discord connect: %w", err)
	}
	defer dg.Close()
	log.Printf("discord: connected as %s — session %s%s", botLabel(dg), session.SessionID, channelScope(channelID))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("discord: shutting down")
	return nil
}

// handleMessage runs one Discord message through the persistent session and
// posts the reply. It ignores its own and other bots' messages, applies the
// respond policy, serializes the turn, and keeps the typing indicator alive.
func handleMessage(s *discordgo.Session, m *discordgo.MessageCreate, session *CortexSession, mu *sync.Mutex, channelID string) {
	if m.Author == nil || m.Author.Bot || (s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID) {
		return
	}
	botID := ""
	if s.State != nil && s.State.User != nil {
		botID = s.State.User.ID
	}
	if !shouldRespond(m, botID, channelID) {
		return
	}
	input := strings.TrimSpace(stripMention(m.Content, botID))
	if input == "" {
		return
	}

	// One at a time: a turn in flight blocks the next message until it finishes,
	// which is the cost guard, not just a correctness one.
	mu.Lock()
	defer mu.Unlock()

	stopTyping := keepTyping(s, m.ChannelID)
	res, turnErr := session.Turn(context.Background(), input)
	stopTyping()

	reply := res.Reply
	if turnErr != nil {
		log.Printf("discord: turn error: %v", turnErr)
		if reply == "" {
			reply = "⚠️ turn error: " + turnErr.Error()
		}
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(no reply)"
	}
	for _, chunk := range chunkMessage(reply, discordMaxMessage) {
		if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
			log.Printf("discord: send failed: %v", err)
			break
		}
	}

	// Reply is already sent, so bound the session without making the user wait:
	// a persistent session accumulates history across every message, so without
	// this it would fill the window and overflow. Still inside the mutex, so the
	// next message waits for compaction rather than racing it.
	boundSession(session, turnErr)
}

// boundSession keeps the persistent session within its context window at the
// turn boundary — the only safe point, since mid-turn compaction would orphan a
// tool-call sequence. On a clean turn it compacts (distills history via study)
// once context crosses the threshold; on an overflow error it learns the real
// window from the message, then compacts so the next turn fits. This is what
// makes "one persistent session" sustainable instead of a slow march to
// overflow.
func boundSession(session *CortexSession, turnErr error) {
	if turnErr != nil {
		if real := parseCtxSize(turnErr.Error()); real > 0 {
			session.Window = real
			if err := session.Compact(context.Background()); err != nil {
				log.Printf("discord: compact after overflow failed: %v", err)
			} else {
				log.Printf("discord: recovered from overflow → window %d, session %s", real, session.SessionID)
			}
		}
		return
	}
	if session.contextRatio() >= compactThreshold {
		pct := 100 * session.contextRatio()
		if err := session.Compact(context.Background()); err != nil {
			log.Printf("discord: compact failed: %v", err)
		} else {
			log.Printf("discord: compacted at %.0f%% → session %s", pct, session.SessionID)
		}
	}
}

// shouldRespond is the gate for which messages the bot acts on: every DM, any
// message that mentions the bot, and — when DISCORD_CHANNEL_ID is set — every
// message in that one channel. With no channel configured the bot stays quiet in
// servers unless directly mentioned, so it never replies to unrelated chatter.
func shouldRespond(m *discordgo.MessageCreate, botID, channelID string) bool {
	if m.GuildID == "" { // direct message
		return true
	}
	if channelID != "" && m.ChannelID == channelID {
		return true
	}
	for _, u := range m.Mentions {
		if u.ID == botID {
			return true
		}
	}
	return false
}

// stripMention removes a leading/inline bot mention from the message so the
// model sees the request, not the "<@id>" plumbing. Both the plain and
// nickname (<@!id>) mention forms are handled.
func stripMention(content, botID string) string {
	if botID == "" {
		return content
	}
	content = strings.ReplaceAll(content, "<@"+botID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botID+">", "")
	return content
}

// chunkMessage splits a reply into pieces no longer than max, preferring to
// break on a newline, then a space, and only hard-cutting an unbroken run as a
// last resort. The separator a break lands on is consumed, so chunks rejoin
// cleanly without doubled blank lines.
func chunkMessage(s string, max int) []string {
	if max <= 0 {
		max = discordMaxMessage
	}
	var out []string
	for len(s) > max {
		cut, drop := strings.LastIndexByte(s[:max], '\n'), 1
		if cut <= 0 {
			cut = strings.LastIndexByte(s[:max], ' ')
		}
		if cut <= 0 {
			cut, drop = max, 0 // no separator in range — hard cut, lose nothing
		}
		out = append(out, s[:cut])
		s = s[cut+drop:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// keepTyping shows the typing indicator immediately and refreshes it until the
// returned stop func is called, so a multi-minute turn keeps the channel warm.
func keepTyping(s *discordgo.Session, channelID string) (stop func()) {
	done := make(chan struct{})
	_ = s.ChannelTyping(channelID)
	go func() {
		t := time.NewTicker(typingRefresh)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = s.ChannelTyping(channelID)
			}
		}
	}()
	return func() { close(done) }
}

func botLabel(dg *discordgo.Session) string {
	if dg.State != nil && dg.State.User != nil {
		return dg.State.User.Username
	}
	return "bot"
}

func channelScope(channelID string) string {
	if channelID == "" {
		return " (DMs + mentions)"
	}
	return " (channel " + channelID + ")"
}
