// discord_fake_test.go — the fake discordAPI (discord_client.go) every
// Phase 7 discord*_test.go file shares: records every call it receives and
// hands back canned Discord objects, so no test in this package ever
// touches a live gateway or the discordgo library's HTTP client.
package main

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// fakeDiscordAPI implements discordAPI by recording calls in send order and
// synthesizing message ids. Safe for concurrent use (discordBot's methods
// may call it from multiple goroutines under test, same as production).
type fakeDiscordAPI struct {
	mu sync.Mutex

	sent          []fakeSend
	sentComplex   []fakeSendComplex
	edits         []fakeEdit
	editsComplex  []fakeEditComplex
	typing        []string
	reactions     []fakeReaction
	responds      []fakeRespond
	respondEdits  []fakeRespondEdit
	bulkOverwrite []fakeBulkOverwrite

	nextID int
}

type fakeSend struct {
	channelID, content string
}
type fakeSendComplex struct {
	channelID string
	data      *discordgo.MessageSend
}
type fakeEdit struct {
	channelID, messageID, content string
}
type fakeEditComplex struct {
	edit *discordgo.MessageEdit
}
type fakeReaction struct {
	channelID, messageID, emojiID string
}
type fakeRespond struct {
	interaction *discordgo.Interaction
	resp        *discordgo.InteractionResponse
}
type fakeRespondEdit struct {
	interaction *discordgo.Interaction
	edit        *discordgo.WebhookEdit
}
type fakeBulkOverwrite struct {
	appID, guildID string
	commands       []*discordgo.ApplicationCommand
}

func newFakeDiscordAPI() *fakeDiscordAPI {
	return &fakeDiscordAPI{}
}

func (f *fakeDiscordAPI) newMessageID() string {
	f.nextID++
	return "msg-" + strconv.Itoa(f.nextID)
}

func (f *fakeDiscordAPI) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fakeSend{channelID, content})
	return &discordgo.Message{ID: f.newMessageID(), ChannelID: channelID, Content: content}, nil
}

func (f *fakeDiscordAPI) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentComplex = append(f.sentComplex, fakeSendComplex{channelID, data})
	return &discordgo.Message{ID: f.newMessageID(), ChannelID: channelID, Content: data.Content}, nil
}

func (f *fakeDiscordAPI) ChannelMessageEdit(channelID, messageID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, fakeEdit{channelID, messageID, content})
	return &discordgo.Message{ID: messageID, ChannelID: channelID, Content: content}, nil
}

func (f *fakeDiscordAPI) ChannelMessageEditComplex(m *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editsComplex = append(f.editsComplex, fakeEditComplex{m})
	return &discordgo.Message{ID: m.ID, ChannelID: m.Channel}, nil
}

func (f *fakeDiscordAPI) ChannelTyping(channelID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, channelID)
	return nil
}

func (f *fakeDiscordAPI) MessageReactionAdd(channelID, messageID, emojiID string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, fakeReaction{channelID, messageID, emojiID})
	return nil
}

func (f *fakeDiscordAPI) InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responds = append(f.responds, fakeRespond{interaction, resp})
	return nil
}

func (f *fakeDiscordAPI) InteractionResponseEdit(interaction *discordgo.Interaction, newresp *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respondEdits = append(f.respondEdits, fakeRespondEdit{interaction, newresp})
	return &discordgo.Message{}, nil
}

func (f *fakeDiscordAPI) ApplicationCommandBulkOverwrite(appID string, guildID string, commands []*discordgo.ApplicationCommand, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if appID == "" {
		return nil, fmt.Errorf("fakeDiscordAPI: empty appID")
	}
	f.bulkOverwrite = append(f.bulkOverwrite, fakeBulkOverwrite{appID, guildID, commands})
	return commands, nil
}

// sentTo returns every plain-text ChannelMessageSend content sent to
// channelID, in call order.
func (f *fakeDiscordAPI) sentTo(channelID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		if s.channelID == channelID {
			out = append(out, s.content)
		}
	}
	return out
}

// sentComplexTo returns every ChannelMessageSendComplex payload sent to
// channelID, in call order.
func (f *fakeDiscordAPI) sentComplexTo(channelID string) []*discordgo.MessageSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*discordgo.MessageSend
	for _, s := range f.sentComplex {
		if s.channelID == channelID {
			out = append(out, s.data)
		}
	}
	return out
}

var _ discordAPI = (*fakeDiscordAPI)(nil)
