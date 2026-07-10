package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/dereksantos/cortex/pkg/llm"
)

// Discord adapter. Cortex knows Discord and nothing else: this file is the only
// place that imports discordgo, and there is no awareness of any orchestration
// layer above it — such a layer wraps the whole `cortex discord` process
// externally without cortex referencing it.
//
// `cortex discord` runs the bot inside the Cortex binary, so it holds an in-memory
// CortexSession and calls session.Turn directly — no subprocess, no per-message
// cold start. A mutex serializes turns, which is what enforces "one session /
// one change at a time" and bounds cost.
//
// Session lifecycle is decided, not hardcoded: at ingress a small-model
// classifier (classifyRoute, on the Cortex process's own reasoner) routes each message to
// either CONTINUE the current change or START a new one. Biased to continue — a reset
// is cheap because per-turn capture already persisted durable facts to .cortex/,
// so retrieval carries the relevant context into a fresh session. !new / !continue
// are manual overrides.

const (
	// discordMaxMessage is Discord's hard per-message character limit; replies
	// are chunked below it. Kept a hair under 2000 for safety.
	discordMaxMessage = 1990
	// typingRefresh re-triggers the typing indicator, which Discord clears after
	// ~10s, so a long agent turn keeps showing "Cortex is typing…".
	typingRefresh = 8 * time.Second
	// routeConfidenceThreshold is the bar a new_change decision must clear to
	// reset the session. Below it, continue — the bias-to-continue gate that
	// keeps a misread from resetting live work.
	routeConfidenceThreshold = 0.8
)

// discordBot holds the bot's mutable state. session is swapped wholesale on a
// new change; the mutex serializes the entire handle path (route + turn +
// compaction) so messages are processed one at a time.
type discordBot struct {
	mu        sync.Mutex
	session   *CortexSession
	channelID string
	goal      string // one-line description of the active task (first msg of the session)
	change    string // active change branch, "" when none has been cut
}

// runDiscordCLI implements `cortex discord`: connect to Discord and drive the
// session. Token comes from DISCORD_BOT_TOKEN (env, like the OpenRouter key); an
// optional DISCORD_CHANNEL_ID restricts the bot to one channel, and
// DISCORD_SESSION_ID resumes a specific prior session.
func runDiscordCLI() error {
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set — create a bot at https://discord.com/developers, enable the Message Content intent, and export its token")
	}
	channelID := strings.TrimSpace(os.Getenv("DISCORD_CHANNEL_ID"))

	// quiet mutes terminal emission — the reply comes back via TurnResult and
	// goes to Discord, while stderr stays a clean server log.
	session := NewCortexSession()
	session.quiet = true
	if sid := strings.TrimSpace(os.Getenv("DISCORD_SESSION_ID")); sid != "" {
		if err := session.ResumeTranscript(sid); err != nil {
			log.Printf("resume %s: %v — starting fresh", sid, err)
			session.StartTranscript()
		} else {
			session.showLoadedContext(session.SessionID)
		}
	} else {
		session.StartTranscript()
	}
	session.EnableMemory()
	bot := &discordBot{session: session, channelID: channelID}
	defer bot.session.Close()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("discord session: %w", err)
	}
	// Message events (not slash commands) so a turn can take minutes without
	// hitting the 3s interaction-ack deadline. Message Content must be enabled in
	// the developer portal for the content to arrive.
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) { bot.handle(s, m) })

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

// handle processes one Discord message: filters, applies a manual command if
// present, otherwise routes (continue vs new change) and runs the turn.
func (b *discordBot) handle(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || (s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID) {
		return
	}
	botID := ""
	if s.State != nil && s.State.User != nil {
		botID = s.State.User.ID
	}
	if !shouldRespond(m, botID, b.channelID) {
		return
	}
	content := strings.TrimSpace(stripMention(m.Content, botID))
	cmd, arg := parseBotCommand(content)

	// Serialize everything: a turn (or compaction) in flight blocks the next
	// message until it finishes. This is the cost guard, not just correctness.
	b.mu.Lock()
	defer b.mu.Unlock()

	switch cmd {
	case "status":
		b.reply(s, m.ChannelID, b.statusLine())
		return
	case "continue":
		b.reply(s, m.ChannelID, "continuing "+b.changeLabel())
		return
	case "new":
		name := arg
		if name == "" {
			name = "change"
		}
		b.startNewChange(name)
		b.reply(s, m.ChannelID, "started "+b.changeLabel())
		return
	}

	input := content
	if input == "" {
		return
	}

	// Route before the turn: a confident new_change resets to a fresh session +
	// branch first, so the work lands in the right place.
	b.maybeRouteNewChange(context.Background(), input)

	stopTyping := keepTyping(s, m.ChannelID)
	res, turnErr := b.session.Turn(context.Background(), input)
	stopTyping()

	// First substantive message of a session establishes its goal — the
	// reference the router compares later messages against.
	if strings.TrimSpace(b.goal) == "" {
		b.goal = input
	}

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
	b.reply(s, m.ChannelID, reply)

	// Reply sent — bound the session without making the user wait. Still inside
	// the mutex, so the next message waits for compaction rather than racing it.
	boundSession(b.session, turnErr)
}

// Route decision labels. routeNewChange is the only one that triggers a reset;
// any other value (including the empty-string fail-safe) means continue.
const (
	routeContinue  = "continue"
	routeNewChange = "new_change"
)

// routeMessagePrompt classifies an incoming message against the active task:
// continue the current change, or start a new, distinct one? Biased hard toward
// continue — resetting throws away the working context, so it only fires on an
// unmistakable shift. Single small-LLM call, fixed-shape JSON output. The two
// %s are the goal then the message.
const routeMessagePrompt = `You route messages for a coding assistant that works on ONE change at a time. Decide whether the new message belongs to the CURRENT task or starts a NEW, distinct task.

Current task:
%s

New message:
%s

Rules:
- "continue" — a follow-up, refinement, correction, question, or clarification about the current task. Also when the message is small talk or you are at all unsure.
- "new_change" — ONLY when the message clearly asks for a different, unrelated piece of work that does not build on the current task.
- Bias strongly toward "continue". Choosing "new_change" resets the working context, so only do it when the shift is unmistakable. When in doubt, continue.

For "new_change", set "name" to a short kebab-case slug for the new task (e.g. "add-rate-limiting"). For "continue", leave "name" empty.

Output ONLY a JSON object:
{"decision":"continue|new_change","name":"<slug-or-empty>","confidence":0.0-1.0,"why":"<=8 words"}`

// routeMaxOutputTokens bounds the classifier's reply — it emits one fixed-shape
// JSON object, never prose. Restores the cap the original prompt template
// carried (max_output_tokens: 50), nudged up to avoid clipping a valid object
// with a longer slug. A reply truncated below the closing brace simply fails to
// parse and falls back to "continue" — the safe direction.
const routeMaxOutputTokens = 80

// routeDecision is the classifier's parsed output.
type routeDecision struct {
	Decision   string  `json:"decision"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

// maybeRouteNewChange classifies the message against the active goal and, only on
// a confident new_change, resets to a fresh session/branch. Fails safe: an empty
// goal, an unavailable provider, any LLM/parse error, "continue", or
// sub-threshold confidence all leave the current session untouched — a misread
// degrades to "keep going", never a surprise reset. The classifier runs on the
// loop's own small-model client (the same reasoner the shell gate uses).
func (b *discordBot) maybeRouteNewChange(ctx context.Context, message string) {
	if strings.TrimSpace(b.goal) == "" {
		return // no active task to diverge from
	}
	r := b.session.reasoner()
	r.SetMaxTokens(routeMaxOutputTokens) // fresh client per call; bounding it here can't affect other reasoner() users
	dec, ok := classifyRoute(ctx, r, message, b.goal)
	if !ok {
		return // fail-safe: any error keeps the current session
	}
	if dec.Decision != routeNewChange || dec.Confidence < routeConfidenceThreshold {
		return
	}
	name := strings.TrimSpace(dec.Name)
	if name == "" {
		name = slugifyChange(message)
	}
	log.Printf("discord: route → new change %q (conf %.2f: %s)", name, dec.Confidence, dec.Why)
	b.startNewChange(name)
}

// classifyRoute runs the route-message classification. ok is false on any
// failure path (provider unavailable, LLM error, no parseable JSON), so the
// caller treats it as "continue". A non-new_change or out-of-range result is
// returned as-is for the caller to threshold.
func classifyRoute(ctx context.Context, p llm.Provider, message, goal string) (routeDecision, bool) {
	if p == nil || !p.IsAvailable() {
		return routeDecision{}, false
	}
	resp, err := p.Generate(ctx, fmt.Sprintf(routeMessagePrompt, goal, message))
	if err != nil {
		return routeDecision{}, false
	}
	return parseRouteDecision(resp)
}

// parseRouteDecision lifts the JSON object out of the model's reply (tolerating
// surrounding prose), validates the decision label, and clamps confidence to
// [0,1]. An unrecognized label normalizes to "continue" with zero confidence.
func parseRouteDecision(resp string) (routeDecision, bool) {
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start < 0 || end <= start {
		return routeDecision{}, false
	}
	var dec routeDecision
	if err := json.Unmarshal([]byte(resp[start:end+1]), &dec); err != nil {
		return routeDecision{}, false
	}
	dec.Decision = strings.ToLower(strings.TrimSpace(dec.Decision))
	if dec.Decision != routeNewChange && dec.Decision != routeContinue {
		dec.Decision = routeContinue
		dec.Confidence = 0
	}
	if dec.Confidence < 0 {
		dec.Confidence = 0
	}
	if dec.Confidence > 1 {
		dec.Confidence = 1
	}
	return dec, true
}

// startNewChange resets to a fresh session and cuts a new change branch. Durable
// facts already live in .cortex/ via per-turn capture, so the fresh session's
// retrieval carries the relevant context forward — the reset is cheap. Git is
// best-effort: a dirty tree is checkpointed first (only when already on a change
// branch) so the new branch starts clean; if branching fails the session still
// resets.
func (b *discordBot) startNewChange(name string) {
	if clean, _ := gitClean(); !clean {
		if head, err := commitChange("checkpoint: " + b.goalOrWIP()); err == nil {
			log.Printf("discord: checkpointed WIP %s", head)
		}
	}
	if branch, err := startChange(name); err != nil {
		log.Printf("discord: change start %q: %v (resetting session only)", name, err)
		b.change = ""
	} else {
		b.change = branch
		log.Printf("discord: started change %s", branch)
	}

	old := b.session
	ns := NewCortexSession()
	ns.quiet = true
	ns.StartTranscript()
	ns.EnableMemory()
	b.session = ns
	b.goal = ""
	old.Close()
}

// reply sends text to a channel, chunked under Discord's per-message limit.
func (b *discordBot) reply(s *discordgo.Session, channelID, text string) {
	for _, chunk := range chunkMessage(text, discordMaxMessage) {
		if _, err := s.ChannelMessageSend(channelID, chunk); err != nil {
			log.Printf("discord: send failed: %v", err)
			return
		}
	}
}

func (b *discordBot) changeLabel() string {
	if b.change != "" {
		return "change " + b.change
	}
	return "session " + b.session.SessionID
}

func (b *discordBot) goalOrWIP() string {
	if strings.TrimSpace(b.goal) != "" {
		return b.goal
	}
	return "work in progress"
}

func (b *discordBot) statusLine() string {
	goal := b.goal
	if strings.TrimSpace(goal) == "" {
		goal = "(none yet)"
	}
	return fmt.Sprintf("%s · context %.0f%%\ngoal: %s", b.changeLabel(), 100*b.session.contextRatio(), goal)
}

// parseBotCommand recognizes the manual overrides. The first whitespace token
// decides: "!status", "!continue", or "!new <name>"; anything else is an
// ordinary message, returned with kind "".
func parseBotCommand(content string) (kind, arg string) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", ""
	}
	switch fields[0] {
	case "!status":
		return "status", ""
	case "!continue":
		return "continue", ""
	case "!new":
		return "new", strings.TrimSpace(strings.TrimPrefix(content, fields[0]))
	default:
		return "", content
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

// boundSession keeps the persistent session within its context window at the
// turn boundary — the only safe point, since mid-turn compaction would orphan a
// tool-call sequence. On a clean turn it compacts (distills history via study)
// once context crosses the threshold; on an overflow error it learns the real
// window from the message, then compacts so the next turn fits. This is what
// makes a long-lived session sustainable instead of a slow march to overflow.
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
