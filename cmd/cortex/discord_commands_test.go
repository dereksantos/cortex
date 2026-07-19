// discord_commands_test.go — Phase 7 sub-item 2 (native application
// commands): the registration table, registration itself, and each
// command's body, all against the fakeDiscordAPI (no live gateway).
package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// TestDiscordCommandsTableCoversTheReplSlashCommands pins the registration
// table (discord_commands.go's discordCommands) to the REPL slash-command
// set Phase 7 targets (main.go: /compact, /clear, /sessions, /model) plus
// /stop (the interrupt affordance the REPL doesn't need, since Ctrl-C
// already does the job there). A name missing from this table is a name
// that never became discoverable in Discord.
func TestDiscordCommandsTableCoversTheReplSlashCommands(t *testing.T) {
	want := map[string]bool{"sessions": false, "compact": false, "clear": false, "model": false, "stop": false}
	for _, c := range discordCommands {
		if c.Name == "" {
			t.Error("a registered command has an empty name")
		}
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("command %q has no description — undiscoverable in the Discord command picker", c.Name)
		}
		if _, ok := want[c.Name]; !ok {
			t.Errorf("unexpected command %q in the registration table", c.Name)
		}
		want[c.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected command %q missing from discordCommands", name)
		}
	}

	// /model's optional "name" argument is what makes "/model" (show) and
	// "/model name:<x>" (set) both reachable as one command, matching the
	// REPL's "/model [name]" (main.go).
	for _, c := range discordCommands {
		if c.Name != "model" {
			continue
		}
		if len(c.Options) != 1 || c.Options[0].Name != "name" || c.Options[0].Required {
			t.Errorf("/model options = %+v, want one optional \"name\" string option", c.Options)
		}
	}
}

// TestRegisterDiscordCommandsCallsBulkOverwrite proves registration reaches
// the wire call with the right appID/guildID/table, and that an empty appID
// (READY not yet populated — applicationID, discord.go) fails loudly rather
// than silently registering nothing.
func TestRegisterDiscordCommandsCallsBulkOverwrite(t *testing.T) {
	api := newFakeDiscordAPI()
	if err := registerDiscordCommands(api, "app-1", "guild-1"); err != nil {
		t.Fatalf("registerDiscordCommands: %v", err)
	}
	if len(api.bulkOverwrite) != 1 {
		t.Fatalf("bulkOverwrite calls = %d, want 1", len(api.bulkOverwrite))
	}
	got := api.bulkOverwrite[0]
	if got.appID != "app-1" || got.guildID != "guild-1" {
		t.Errorf("bulkOverwrite(appID=%q, guildID=%q), want (app-1, guild-1)", got.appID, got.guildID)
	}
	if len(got.commands) != len(discordCommands) {
		t.Errorf("registered %d commands, want %d", len(got.commands), len(discordCommands))
	}

	if err := registerDiscordCommands(api, "", "guild-1"); err == nil {
		t.Error("registerDiscordCommands with an empty appID should fail, not silently register nothing")
	}
}

// TestApplicationIDPrefersApplicationOverUser proves applicationID
// (discord_commands.go) prefers State.Application.ID once READY populates
// it, falling back to the bot's own user id.
func TestApplicationIDPrefersApplicationOverUser(t *testing.T) {
	if got := applicationID(nil); got != "" {
		t.Errorf("applicationID(nil) = %q, want empty", got)
	}
	userOnly := &discordgo.Session{State: &discordgo.State{Ready: discordgo.Ready{User: &discordgo.User{ID: "user-id"}}}}
	if got := applicationID(userOnly); got != "user-id" {
		t.Errorf("applicationID with only User set = %q, want %q", got, "user-id")
	}
	withApp := &discordgo.Session{State: &discordgo.State{Ready: discordgo.Ready{
		User:        &discordgo.User{ID: "user-id"},
		Application: &discordgo.Application{ID: "app-id"},
	}}}
	if got := applicationID(withApp); got != "app-id" {
		t.Errorf("applicationID with Application set = %q, want %q (Application.ID preferred)", got, "app-id")
	}
}

// TestRunSlashCommandRoutesEachVerb exercises runSlashCommand's dispatch
// (discord_commands.go) — the pure function handleSlashCommand calls after
// acking the interaction — against a hermetic session, one subtest per
// command name.
func TestRunSlashCommandRoutesEachVerb(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")
	const channelID = "chan1"
	if _, err := bot.sessionFor(channelID); err != nil {
		t.Fatalf("sessionFor: %v", err)
	}

	t.Run("sessions", func(t *testing.T) {
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "sessions"})
		if got == "" {
			t.Error("sessions reply is empty")
		}
	})

	t.Run("model show", func(t *testing.T) {
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "model"})
		if !strings.Contains(got, "code:") {
			t.Errorf("model (show) reply = %q, want it to include the code binding", got)
		}
	})

	t.Run("model set", func(t *testing.T) {
		opts := []*discordgo.ApplicationCommandInteractionDataOption{{Name: "name", Type: discordgo.ApplicationCommandOptionString, Value: "some-model"}}
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "model", Options: opts})
		if !strings.Contains(got, "some-model") {
			t.Errorf("model (set) reply = %q, want it to confirm the new model", got)
		}
	})

	t.Run("stop with nothing running", func(t *testing.T) {
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "stop"})
		if !strings.Contains(got, "nothing running") {
			t.Errorf("stop reply = %q, want a nothing-to-stop message", got)
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "bogus"})
		if !strings.Contains(got, "unknown") {
			t.Errorf("unknown-command reply = %q, want it to say so", got)
		}
	})

	t.Run("clear", func(t *testing.T) {
		before, err := bot.sessionFor(channelID)
		if err != nil {
			t.Fatalf("sessionFor: %v", err)
		}
		got := bot.runSlashCommand(channelID, discordgo.ApplicationCommandInteractionData{Name: "clear"})
		if !strings.Contains(got, "cleared") {
			t.Errorf("clear reply = %q, want it to confirm clearing", got)
		}
		after, err := bot.sessionFor(channelID)
		if err != nil {
			t.Fatalf("sessionFor: %v", err)
		}
		if after.ID() == before.ID() {
			t.Error("/clear did not rebind the channel to a new session")
		}
	})
}

// TestHandleComponentResolvesRiskButtons proves the interaction path from a
// button click (handleComponent, discord_commands.go) through to
// riskApprovals.resolveButton, and that the ack is ephemeral so only the
// clicker sees the confirmation.
func TestHandleComponentResolvesRiskButtons(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	bot.risk.pending["chan1"] = &riskPrompt{resolve: make(chan riskDecision, 1)}
	ic := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{CustomID: riskCustomID("chan1", true)},
	}}
	bot.handleComponent(ic)

	if len(api.responds) != 1 {
		t.Fatalf("InteractionRespond calls = %d, want 1", len(api.responds))
	}
	resp := api.responds[0].resp
	if resp.Data == nil || resp.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Error("risk-button ack is not ephemeral — visible to the whole channel")
	}
	if !strings.Contains(strings.ToLower(resp.Data.Content), "approved") {
		t.Errorf("ack content = %q, want it to confirm approval", resp.Data.Content)
	}

	// resolveButton delivers the decision onto the prompt's buffered
	// channel; only ask()'s own select (not exercised by this test) drains
	// it and removes the prompt from ra.pending.
	select {
	case dec := <-bot.risk.pending["chan1"].resolve:
		if dec != riskApproved {
			t.Errorf("decision = %v, want riskApproved", dec)
		}
	default:
		t.Fatal("handleComponent -> resolveButton did not deliver a decision")
	}
}

// TestHandleComponentIgnoresNonRiskCustomIDs proves handleComponent only
// acts on its own risk-approval button ids — any other component id (a
// future feature's button landing in the same handler) is a silent no-op,
// not a crash or a stray InteractionRespond.
func TestHandleComponentIgnoresNonRiskCustomIDs(t *testing.T) {
	mgr := hermeticDiscordManager(t)
	api := newFakeDiscordAPI()
	bot := newDiscordBot(mgr, api, "", "")

	ic := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{CustomID: "something-else"},
	}}
	bot.handleComponent(ic)
	if len(api.responds) != 0 {
		t.Errorf("InteractionRespond calls = %d, want 0 for a non-risk component id", len(api.responds))
	}
}
