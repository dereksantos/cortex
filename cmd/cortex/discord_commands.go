// discord_commands.go — Phase 7's command parity (docs/cortex-web.md Phase
// 7, decision D12): the REPL slash commands (/compact, /clear, /sessions,
// /model) as native Discord application commands. discordgo v0.29 fully
// supports application commands (ApplicationCommandBulkOverwrite) and
// message components (interactions.go/components.go — confirmed by reading
// the vendored library, not assumed), so this is the primary surface; the
// legacy "!status"/"!continue"/"!new" message-prefix commands (discord.go)
// stay for backward compatibility rather than being ported to slash form —
// they predate command parity and aren't in the REPL's slash-command set
// this phase targets.
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// discordCommands is the application-command registration table — the
// "command registration table" the Phase 7 test plan calls for. Each name
// mirrors its REPL slash-command counterpart (main.go's dispatch) so the
// discoverable Discord form and the REPL form never drift apart in name.
var discordCommands = []*discordgo.ApplicationCommand{
	{Name: "sessions", Description: "List recent Cortex sessions for this channel's project"},
	{Name: "compact", Description: "Compact the current session's context via study"},
	{Name: "clear", Description: "Start a fresh session, clearing context"},
	{
		Name:        "model",
		Description: "Show or set the coding model",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "name",
				Description: "Model to switch to (omit to show the current bindings)",
				Required:    false,
			},
		},
	},
	{Name: "stop", Description: "Interrupt the in-flight turn in this channel"},
}

// registerDiscordCommands overwrites appID's command set for guildID (""
// registers globally — required for DM availability, but subject to
// Discord's slow global-propagation window; a non-empty guildID registers
// instantly for that guild, per D12's "one startup call per guild").
func registerDiscordCommands(api discordAPI, appID, guildID string) error {
	if strings.TrimSpace(appID) == "" {
		return fmt.Errorf("discord application id unavailable — cannot register application commands")
	}
	if _, err := api.ApplicationCommandBulkOverwrite(appID, guildID, discordCommands); err != nil {
		return fmt.Errorf("failed to register discord application commands (guild %q): %w", guildID, err)
	}
	return nil
}

// applicationID extracts the bot's application id from a connected session's
// state: State.Application.ID once READY has populated it, falling back to
// the bot's own user id (identical to the application id for every ordinary
// bot-only Discord app).
func applicationID(dg *discordgo.Session) string {
	if dg == nil || dg.State == nil {
		return ""
	}
	if dg.State.Application != nil && dg.State.Application.ID != "" {
		return dg.State.Application.ID
	}
	if dg.State.User != nil {
		return dg.State.User.ID
	}
	return ""
}

// handleInteraction routes a Discord interaction event to the application-
// command or message-component (button) handler.
func (b *discordBot) handleInteraction(ic *discordgo.InteractionCreate) {
	switch ic.Type {
	case discordgo.InteractionApplicationCommand:
		b.handleSlashCommand(ic)
	case discordgo.InteractionMessageComponent:
		b.handleComponent(ic)
	}
}

// handleSlashCommand acks immediately (deferred response — a turn-adjacent
// command like /compact can take longer than Discord's 3s interaction
// deadline) then edits in the real result once runSlashCommand returns.
func (b *discordBot) handleSlashCommand(ic *discordgo.InteractionCreate) {
	if err := b.api.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("discord: failed to ack interaction: %v", err)
		return
	}
	reply := b.runSlashCommand(ic.ChannelID, ic.ApplicationCommandData())
	if _, err := b.api.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{Content: &reply}); err != nil {
		log.Printf("discord: failed to deliver interaction reply: %v", err)
	}
}

// runSlashCommand executes one application command's verb and returns the
// text to show. Pure enough to unit-test without an interaction round-trip:
// callers only need channelID and the parsed command data.
func (b *discordBot) runSlashCommand(channelID string, data discordgo.ApplicationCommandInteractionData) string {
	switch data.Name {
	case "sessions":
		return b.sessionsText(channelID)
	case "compact":
		return b.compactText(channelID)
	case "clear":
		return b.clearText(channelID)
	case "model":
		name := ""
		if opt := data.GetOption("name"); opt != nil {
			name = strings.TrimSpace(opt.StringValue())
		}
		return b.modelText(channelID, name)
	case "stop":
		return b.interrupt(channelID)
	default:
		return "unknown command: /" + data.Name
	}
}

// handleComponent resolves a risk-approval button click (discord_risk.go)
// and acks with an ephemeral confirmation only the clicker sees.
func (b *discordBot) handleComponent(ic *discordgo.InteractionCreate) {
	data := ic.MessageComponentData()
	channelID, approve, ok := parseRiskCustomID(data.CustomID)
	if !ok {
		return
	}
	resolved := b.risk.resolveButton(channelID, approve)
	content := "risky command denied."
	if approve {
		content = "risky command approved."
	}
	if !resolved {
		content = "that approval has already been resolved or expired."
	}
	if err := b.api.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("discord: failed to ack risk-approval button: %v", err)
	}
}
