package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Discord adapter (docs/cortex-web.md Phase 7, decision D14). Cortex knows
// Discord and nothing else: discord*.go is the only place that imports
// discordgo (discord_client.go's discordAPI interface is the boundary
// everything else in this package sees), and there is no awareness of any
// orchestration layer above it — such a layer wraps the whole `cortex
// discord` process externally without cortex referencing it.
//
// `cortex discord` runs the bot inside the Cortex binary and drives sessions
// through the same SessionManager `cortex serve` uses (serve_session.go,
// Phase 4) instead of the pre-Phase-7 bespoke sync.Mutex + wholesale
// session-swap: one live *CortexSession per Discord channel
// (discordBot.sessions), one turn at a time per session
// (managedSession.mu — a non-blocking TryLock here, unlike serve's blocking
// mutex, so a message that arrives mid-turn gets an immediate "still
// working" reply instead of silently queuing behind it), different
// channels' turns run concurrently.
//
// Session lifecycle is decided, not hardcoded: at ingress a small-model
// classifier (classifyRoute, on the channel's own session reasoner) routes
// each message to either CONTINUE the current change or START a new one.
// Biased to continue — a reset is cheap because per-turn capture already
// persisted durable facts to .cortex/, so retrieval carries the relevant
// context into a fresh session. !new / !continue are manual overrides, and
// /sessions, /compact, /clear, /model, /stop are native application
// commands (discord_commands.go, decision D12) mirroring the REPL's slash
// commands. A shellrisk Risky command gets an interactive approve/deny
// prompt (discord_risk.go) instead of headless-Blocked — the one place
// Discord gains something the REPL already had: a human in the loop.

// discordMaxMessage is Discord's hard per-message character limit; replies
// are chunked below it. Kept a hair under 2000 for safety. Not
// config-overridable — see CLAUDE.md's "explicitly not exposed" list.
const discordMaxMessage = 1990

// defaultTypingRefresh is the immutable historical default — see
// fleetDiscoveryTimeout's doc comment (config.go) for why
// Config.discordTypingRefresh falls back to this const, never the mutable
// typingRefresh var below.
const defaultTypingRefresh = 8 * time.Second

// typingRefresh re-triggers the typing indicator, which Discord clears after
// ~10s, so a long agent turn keeps showing "Cortex is typing…". A var (not a
// const): runDiscordCLI sets it once at startup from
// discord.typing_refresh_sec; unset, it stays defaultTypingRefresh.
var typingRefresh = defaultTypingRefresh

// routeConfidenceThreshold is the bar a new_change decision must clear to
// reset the session. Below it, continue — the bias-to-continue gate that
// keeps a misread from resetting live work. Kept as the resolver default
// (Config.discordRouteConfidenceThreshold) — maybeRouteNewChange reads the
// per-session config value directly rather than this constant, since
// discord.route_confidence_threshold's 0-vs-unset distinction needs the
// *float64 pointer, which a plain var can't carry as cleanly.
const routeConfidenceThreshold = 0.8

// discordBot holds the bot's mutable state. Every map is keyed by Discord
// channel id — one live session, goal, active change, in-flight-turn
// cancel, and status message per channel — guarded by mu.
type discordBot struct {
	mgr       *SessionManager
	api       discordAPI
	channelID string // legacy env scoping — restricts which channel/DMs respond (shouldRespond)
	project   string // resolved project name; "" = CWD-implicit default (serve_session.go's Create/Resume)
	risk      *riskApprovals

	mu        sync.Mutex
	sessions  map[string]string             // channelID -> live session id
	goals     map[string]string             // channelID -> active-task goal (bias-to-continue router)
	changes   map[string]string             // channelID -> active change branch
	cancels   map[string]context.CancelFunc // channelID -> cancel for its in-flight turn
	statusMsg map[string]string             // channelID -> id of its live progress status message
}

func newDiscordBot(mgr *SessionManager, api discordAPI, channelID, project string) *discordBot {
	return &discordBot{
		mgr:       mgr,
		api:       api,
		channelID: channelID,
		project:   project,
		risk:      newRiskApprovals(),
		sessions:  make(map[string]string),
		goals:     make(map[string]string),
		changes:   make(map[string]string),
		cancels:   make(map[string]context.CancelFunc),
		statusMsg: make(map[string]string),
	}
}

// newDiscordSessionFactory layers Discord's own default (EnableMemory —
// every discord.go session has had memory enabled since before this
// SessionManager rebase) on top of newProductionSession's shared
// construction (serve_session.go) — the same base `cortex serve` uses.
// newProductionSession itself now also calls EnableMemory (the
// docs/cross-source-learning.md serve-capture-gap fix), so this second call
// is a harmless no-op re-wire; kept explicit so Discord's own memory-on
// intent stays legible here even if serve's default ever changes.
func newDiscordSessionFactory() sessionFactory {
	return func() *CortexSession {
		cs := newProductionSession()
		cs.EnableMemory()
		return cs
	}
}

// runDiscordCLI implements `cortex discord`: connect to Discord and drive
// sessions through the shared SessionManager. Token comes from
// DISCORD_BOT_TOKEN (env, like the OpenRouter key); an optional
// DISCORD_CHANNEL_ID restricts the bot to one channel, DISCORD_PROJECT
// targets a registered project (--project's env equivalent; CWD-implicit
// when unset), and DISCORD_SESSION_ID resumes a specific prior session into
// DISCORD_CHANNEL_ID (it needs a channel to bind to under the per-channel
// session model — set both or neither).
// applyDiscordConfig sets the discord*.go package vars (typingRefresh,
// riskApprovalTimeout, progressEditInterval) from cfg.Discord, once, before
// any bot activity — the maybeRouteNewChange call sites read
// routeMaxOutputTokens/routeConfidenceThreshold straight off the live
// session's Config instead (per-session, not process-wide), since those two
// are consulted through a *CortexSession that's already available there.
func applyDiscordConfig(cfg *Config) {
	typingRefresh = cfg.discordTypingRefresh()
	riskApprovalTimeout = cfg.discordRiskApprovalTimeout()
	progressEditInterval = cfg.discordProgressEditInterval()
}

func runDiscordCLI() error {
	applyDiscordConfig(LoadConfig())
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN is not set — create a bot at https://discord.com/developers, enable the Message Content intent, and export its token")
	}
	channelID := strings.TrimSpace(os.Getenv("DISCORD_CHANNEL_ID"))
	project := strings.TrimSpace(os.Getenv("DISCORD_PROJECT"))

	reg, err := registry.New()
	if err != nil {
		return fmt.Errorf("failed to open project registry: %w", err)
	}
	if project != "" {
		if _, err := reg.Lookup(project); err != nil {
			return fmt.Errorf("DISCORD_PROJECT %q: %w", project, err)
		}
	}

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return fmt.Errorf("discord session: %w", err)
	}
	mgr := NewSessionManager(reg, newDiscordSessionFactory())
	bot := newDiscordBot(mgr, dg, channelID, project)
	defer bot.closeSessions()

	// Message events (not exclusively slash commands) so a turn can take
	// minutes without hitting the 3s interaction-ack deadline, plus
	// reaction events for the 🛑-interrupt affordance (discord_progress.go)
	// and interaction events for application commands + risk-approval
	// buttons (discord_commands.go). Message Content must be enabled in the
	// developer portal for message content to arrive.
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent |
		discordgo.IntentGuildMessageReactions | discordgo.IntentDirectMessageReactions
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) { bot.handle(s, m) })
	dg.AddHandler(func(s *discordgo.Session, ic *discordgo.InteractionCreate) { bot.handleInteraction(ic) })
	dg.AddHandler(func(s *discordgo.Session, r *discordgo.MessageReactionAdd) { bot.handleReactionAdd(s, r) })
	// Per-guild command registration (D12: "one startup call per guild")
	// fires as each guild syncs in, making the commands available in that
	// guild instantly rather than waiting out Discord's slow global
	// propagation window.
	dg.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		if err := registerDiscordCommands(bot.api, applicationID(s), g.ID); err != nil {
			log.Printf("discord: %v", err)
		}
	})

	if err := dg.Open(); err != nil {
		return fmt.Errorf("discord connect: %w", err)
	}
	defer dg.Close()

	// Global registration (guildID "") is what makes the commands available
	// in DMs; per-guild registration above is the instant path for guilds.
	if err := registerDiscordCommands(bot.api, applicationID(dg), ""); err != nil {
		log.Printf("discord: %v", err)
	}

	if sid := strings.TrimSpace(os.Getenv("DISCORD_SESSION_ID")); sid != "" {
		if channelID == "" {
			log.Printf("discord: DISCORD_SESSION_ID set without DISCORD_CHANNEL_ID — ignoring (no channel to bind the resumed session to under the per-channel session model)")
		} else if ms, err := mgr.GetOrResume(project, sid); err != nil {
			log.Printf("discord: resume %s: %v — starting fresh per channel", sid, err)
		} else {
			bot.bindSession(channelID, ms.ID())
			log.Printf("discord: resumed session %s for channel %s", ms.ID(), channelID)
		}
	}

	log.Printf("discord: connected as %s%s", botLabel(dg), channelScope(channelID))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("discord: shutting down")
	return nil
}

// closeSessions closes the transcript file handle on every session the
// manager still holds live, at shutdown.
func (b *discordBot) closeSessions() {
	for _, id := range b.mgr.List() {
		if ms, ok := b.mgr.Get(id); ok {
			ms.cs.Close()
		}
	}
}

// handle processes one Discord message: a pending risk-approval reply, a
// manual override command, or an ordinary turn.
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

	// A pending risky-command approval in this channel takes a bare
	// yes/no reply as its resolution instead of an ordinary message — the
	// fallback for clients without button support (discord_risk.go).
	if b.risk.resolveText(m.ChannelID, content) {
		return
	}

	cmd, arg := parseBotCommand(content)
	switch cmd {
	case "status":
		b.send(m.ChannelID, b.statusLine(m.ChannelID))
		return
	case "continue":
		b.send(m.ChannelID, "continuing "+b.changeLabel(m.ChannelID))
		return
	case "new":
		name := arg
		if name == "" {
			name = "change"
		}
		if b.startNewChange(m.ChannelID, name) != nil {
			b.send(m.ChannelID, "started "+b.changeLabel(m.ChannelID))
		} else {
			b.send(m.ChannelID, "failed to start a new change — see the server log")
		}
		return
	case "stop":
		b.send(m.ChannelID, b.interrupt(m.ChannelID))
		return
	}

	b.runTurn(m.ChannelID, content)
}

// sessionFor resolves channelID's live session, creating one if the channel
// has none yet, and (re-)wires its risk-approval hook to this channel —
// idempotent, so it's safe to call on every resolution rather than only on
// creation.
func (b *discordBot) sessionFor(channelID string) (*managedSession, error) {
	b.mu.Lock()
	id := b.sessions[channelID]
	b.mu.Unlock()

	var ms *managedSession
	var err error
	if id != "" {
		ms, err = b.mgr.GetOrResume(b.project, id)
	}
	if id == "" || err != nil {
		if ms, err = b.mgr.Create(b.project); err != nil {
			return nil, err
		}
		b.bindSession(channelID, ms.ID())
	}
	b.wireApproval(channelID, ms)
	return ms, nil
}

// newSession starts and binds a brand-new session for channelID,
// unconditionally (the /clear and !new/routed-reset path — sessionFor's
// creation path is "no session yet"; this one is "discard the current
// one").
func (b *discordBot) newSession(channelID string) (*managedSession, error) {
	ms, err := b.mgr.Create(b.project)
	if err != nil {
		return nil, err
	}
	b.bindSession(channelID, ms.ID())
	b.wireApproval(channelID, ms)
	return ms, nil
}

func (b *discordBot) bindSession(channelID, sessionID string) {
	b.mu.Lock()
	b.sessions[channelID] = sessionID
	b.mu.Unlock()
}

// wireApproval installs channelID's interactive risk-approval hook
// (session_core.go's approveRisky, tool_deps.go's gateShell) on ms —
// Discord's Phase 7 alternative to headless-Blocked for a Risky shellrisk
// verdict.
func (b *discordBot) wireApproval(channelID string, ms *managedSession) {
	ms.cs.approveRisky = func(ctx context.Context, reason, command string) (approved, timedOut bool) {
		return b.risk.ask(ctx, b.api, channelID, reason, command, riskApprovalTimeout)
	}
}

// runTurn resolves channelID's session, enforces "one turn at a time" via a
// non-blocking TryLock (busy → an immediate reply instead of queuing —
// Phase 7 sub-item 1), routes for a possible change reset, then runs the
// turn with progress-as-edits and an interrupt hook wired to the turn's
// ctx-cancel.
func (b *discordBot) runTurn(channelID, input string) {
	if strings.TrimSpace(input) == "" {
		return
	}
	ms, err := b.sessionFor(channelID)
	if err != nil {
		b.send(channelID, "⚠️ failed to open a session: "+err.Error())
		return
	}
	if !ms.mu.TryLock() {
		b.send(channelID, "still working on the previous message — hang tight.")
		return
	}

	// Route before the turn: a confident new_change resets to a fresh
	// session first, same bias-to-continue policy as before the rebase.
	// Switching sessions means releasing the old one's lock and acquiring
	// the new session's instead.
	if switched := b.maybeRouteNewChange(context.Background(), channelID, input, ms); switched != nil {
		ms.mu.Unlock()
		ms = switched
		if !ms.mu.TryLock() {
			// Unreachable in practice (a session this call just created),
			// handled for completeness rather than assumed away.
			b.send(channelID, "still working on the previous message — hang tight.")
			return
		}
	}
	defer ms.mu.Unlock()

	b.mgr.Touch(ms.ID())
	b.mu.Lock()
	if strings.TrimSpace(b.goals[channelID]) == "" {
		b.goals[channelID] = input
	}
	b.mu.Unlock()

	stopTyping := keepTyping(b.api, channelID)
	statusID := b.startProgress(channelID)

	ctx, cancel := context.WithCancel(context.Background())
	b.setCancel(channelID, cancel)

	res, turnErr := ms.cs.TurnWithProgress(ctx, input, b.progressSink(channelID, statusID))

	cancel()
	b.clearCancel(channelID)
	stopTyping()
	b.finishProgress(channelID, statusID)

	reply := res.Reply
	switch {
	case errors.Is(turnErr, context.Canceled):
		reply = "🛑 interrupted"
	case turnErr != nil:
		log.Printf("discord: turn error: %v", turnErr)
		if reply == "" {
			reply = "⚠️ turn error: " + turnErr.Error()
		}
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(no reply)"
	}
	b.send(channelID, reply)

	// Reply sent — bound the session without making the user wait. Still
	// holding ms.mu (deferred above), so a second message on this channel
	// waits for compaction rather than racing it.
	boundSession(ms.cs, turnErr)
}

// setCancel/clearCancel/interrupt implement Phase 7 sub-item 4's interrupt
// affordance: a /stop command (discord_commands.go) or a 🛑 reaction on the
// live status message (discord_progress.go) cancels the same context
// TurnWithProgress is running on — the identical mechanism main.go's
// Ctrl-C paths use (editor.Interruptible / signal.NotifyContext canceling
// the ctx passed to Turn), just triggered by a Discord affordance instead
// of a terminal keystroke.
func (b *discordBot) setCancel(channelID string, cancel context.CancelFunc) {
	b.mu.Lock()
	b.cancels[channelID] = cancel
	b.mu.Unlock()
}

func (b *discordBot) clearCancel(channelID string) {
	b.mu.Lock()
	delete(b.cancels, channelID)
	b.mu.Unlock()
}

func (b *discordBot) interrupt(channelID string) string {
	b.mu.Lock()
	cancel := b.cancels[channelID]
	b.mu.Unlock()
	if cancel == nil {
		return "nothing running to stop."
	}
	cancel()
	return "🛑 stopping…"
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

// maybeRouteNewChange classifies input against channelID's active goal and,
// only on a confident new_change, starts a fresh session/branch and returns
// it (nil means "keep going" — an empty goal, an unavailable provider, any
// LLM/parse error, "continue", or sub-threshold confidence all leave the
// current session untouched, so a misread degrades to "keep going", never a
// surprise reset). The classifier runs on ms's own reasoner (the same
// reasoner the shell gate uses).
func (b *discordBot) maybeRouteNewChange(ctx context.Context, channelID, input string, ms *managedSession) *managedSession {
	b.mu.Lock()
	goal := b.goals[channelID]
	b.mu.Unlock()
	if strings.TrimSpace(goal) == "" {
		return nil // no active task to diverge from
	}
	r := ms.cs.reasoner()
	r.SetMaxTokens(ms.cs.Config.routeMaxOutputTokens()) // fresh client per call; bounding it here can't affect other reasoner() users
	dec, ok := classifyRoute(ctx, r, input, goal)
	if !ok || dec.Decision != routeNewChange || dec.Confidence < ms.cs.Config.discordRouteConfidenceThreshold() {
		return nil
	}
	name := strings.TrimSpace(dec.Name)
	if name == "" {
		name = slugifyChange(input)
	}
	log.Printf("discord: route → new change %q (conf %.2f: %s)", name, dec.Confidence, dec.Why)
	return b.startNewChange(channelID, name)
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

// startNewChange resets channelID to a fresh session and cuts a new change
// branch. Durable facts already live in .cortex/ via per-turn capture, so
// the fresh session's retrieval carries the relevant context forward — the
// reset is cheap. Git is best-effort: a dirty tree is checkpointed first
// (only when already on a change branch) so the new branch starts clean; if
// branching fails the session still resets. Returns nil only when the new
// session itself fails to start (the caller reports that as a failure; a
// failed git checkpoint/branch is logged and otherwise ignored, same as
// before the rebase).
func (b *discordBot) startNewChange(channelID, name string) *managedSession {
	if clean, _ := gitClean(); !clean {
		if head, err := commitChange("checkpoint: " + b.goalOrWIP(channelID)); err == nil {
			log.Printf("discord: checkpointed WIP %s", head)
		}
	}
	var change string
	if branch, err := startChange(name); err != nil {
		log.Printf("discord: change start %q: %v (resetting session only)", name, err)
	} else {
		change = branch
		log.Printf("discord: started change %s", branch)
	}

	ms, err := b.newSession(channelID)
	if err != nil {
		log.Printf("discord: failed to start a new session for change %q: %v", name, err)
		return nil
	}
	b.mu.Lock()
	b.goals[channelID] = ""
	b.changes[channelID] = change
	b.mu.Unlock()
	return ms
}

// send delivers text to a channel, chunked under Discord's per-message limit.
func (b *discordBot) send(channelID, text string) {
	for _, chunk := range chunkMessage(text, discordMaxMessage) {
		if _, err := b.api.ChannelMessageSend(channelID, chunk); err != nil {
			log.Printf("discord: send failed: %v", err)
			return
		}
	}
}

func (b *discordBot) changeLabel(channelID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.changes[channelID]; c != "" {
		return "change " + c
	}
	if id := b.sessions[channelID]; id != "" {
		return "session " + id
	}
	return "session (none yet)"
}

func (b *discordBot) goalOrWIP(channelID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if g := strings.TrimSpace(b.goals[channelID]); g != "" {
		return g
	}
	return "work in progress"
}

func (b *discordBot) statusLine(channelID string) string {
	b.mu.Lock()
	goal, id := b.goals[channelID], b.sessions[channelID]
	b.mu.Unlock()
	if strings.TrimSpace(goal) == "" {
		goal = "(none yet)"
	}
	ratio := 0.0
	if id != "" {
		if ms, ok := b.mgr.Get(id); ok {
			ratio = ms.cs.contextRatio()
		}
	}
	return fmt.Sprintf("%s · context %.0f%%\ngoal: %s", b.changeLabel(channelID), 100*ratio, goal)
}

// sessionsText, compactText, clearText, and modelText are the native
// application commands' bodies (discord_commands.go's runSlashCommand) —
// Discord equivalents of the REPL's /sessions, /compact, /clear, /model
// (main.go).
func (b *discordBot) sessionsText(channelID string) string {
	ms, err := b.sessionFor(channelID)
	if err != nil {
		return "⚠️ " + err.Error()
	}
	infos, err := listSessions(ms.cs.SessionsDir(), 15)
	if err != nil || len(infos) == 0 {
		return "no sessions found"
	}
	var out strings.Builder
	for _, s := range infos {
		marker := "  "
		if s.ID == ms.cs.SessionID {
			marker = "* "
		}
		preview := s.First
		if preview == "" {
			preview = "(no prompt)"
		}
		if r := []rune(preview); len(r) > 60 {
			preview = string(r[:60]) + "…"
		}
		fmt.Fprintf(&out, "%s%s  %2d msgs  %s\n", marker, s.ID, s.Messages, preview)
	}
	return out.String()
}

func (b *discordBot) compactText(channelID string) string {
	ms, err := b.sessionFor(channelID)
	if err != nil {
		return "⚠️ " + err.Error()
	}
	if !ms.mu.TryLock() {
		return "still working on the previous message — try again shortly."
	}
	defer ms.mu.Unlock()
	pct := 100 * ms.cs.contextRatio()
	if err := ms.cs.Compact(context.Background()); err != nil {
		return fmt.Sprintf("compact failed: %v", err)
	}
	return fmt.Sprintf("compacted (was %.0f%%) → session %s", pct, ms.cs.SessionID)
}

func (b *discordBot) clearText(channelID string) string {
	ms, err := b.newSession(channelID)
	if err != nil {
		return "⚠️ failed to start a fresh session: " + err.Error()
	}
	b.mu.Lock()
	b.goals[channelID] = ""
	b.mu.Unlock()
	return "cleared → session " + ms.ID()
}

func (b *discordBot) modelText(channelID, name string) string {
	ms, err := b.sessionFor(channelID)
	if err != nil {
		return "⚠️ " + err.Error()
	}
	if name == "" {
		return fmt.Sprintf("code:  %s @ %s\nstudy: %s @ %s",
			ms.cs.Request.Model, ms.cs.Request.BaseURL, ms.cs.Study.Model, ms.cs.Study.Endpoint)
	}
	ms.cs.SetModel(name)
	return "code model → " + name
}

// parseBotCommand recognizes the manual overrides. The first whitespace
// token decides: "!status", "!continue", "!new <name>", or "!stop";
// anything else is an ordinary message, returned with kind "".
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
	case "!stop":
		return "stop", ""
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
func keepTyping(api discordAPI, channelID string) (stop func()) {
	done := make(chan struct{})
	_ = api.ChannelTyping(channelID)
	go func() {
		t := time.NewTicker(typingRefresh)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = api.ChannelTyping(channelID)
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
			session.learnWindow(real)
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
