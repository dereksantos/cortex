// discord_client.go — the wire boundary between this package's Discord logic
// and the discordgo library (docs/cortex-web.md Phase 7's test requirement:
// "the discord API boundary wrapped in an interface so no test touches
// discord itself"). discordAPI names exactly the *discordgo.Session methods
// the bot calls; *discordgo.Session satisfies it structurally (Go interface
// satisfaction needs no adapter type), so production code passes dg directly
// while tests substitute a fake that records calls and returns canned
// responses — no live gateway, no network, in either case.
package main

import "github.com/bwmarrin/discordgo"

// discordAPI is deliberately narrow: only the calls discord*.go actually
// makes. Extend it (and the *discordgo.Session compile-time check below)
// rather than reaching for the concrete *discordgo.Session type anywhere
// else in this package.
type discordAPI interface {
	ChannelMessageSend(channelID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEdit(channelID, messageID, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(m *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelTyping(channelID string, options ...discordgo.RequestOption) error
	MessageReactionAdd(channelID, messageID, emojiID string, options ...discordgo.RequestOption) error
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	InteractionResponseEdit(interaction *discordgo.Interaction, newresp *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ApplicationCommandBulkOverwrite(appID string, guildID string, commands []*discordgo.ApplicationCommand, options ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
}

// var _ discordAPI = (*discordgo.Session)(nil) proves the production type
// satisfies the interface at compile time — if discordgo ever changes one of
// these signatures, the build breaks here instead of at a call site.
var _ discordAPI = (*discordgo.Session)(nil)
