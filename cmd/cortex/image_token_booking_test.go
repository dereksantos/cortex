package main

// image_token_booking_test.go — issue #218 step 4: what an image costs the
// window.
//
// Three shapes, three answers:
//   - a data-URI part (the form both attachment paths use today) books the
//     decoded bytes ÷ 3, the documented base64 billing rule;
//   - an http(s) URL part books the documented upper-bound ONCE — its bytes
//     are not local, so its size is unknown;
//   - an image recorded as an attachment (a `[image: …]` marker naming the
//     same reference) is booked once, not twice.
//
// The marker-only resumed shape is pinned unchanged: after resume no image
// goes on the wire, so only its ~30-token marker text counts, through
// estTurnTokens' len(Content) term.

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

func dataURIPart(b64payload string) llm.ContentPart {
	return llm.ImageURLPart("data:image/png;base64,"+b64payload, llm.ImageDetailAuto)
}

func urlPart(u string) llm.ContentPart {
	return llm.ImageURLPart(u, llm.ImageDetailAuto)
}

// TestImageTokensOfDataURIAttachment is the data-URI case: the decoded byte
// count, once.
func TestImageTokensOfDataURIAttachment(t *testing.T) {
	raw := attachTestPNG(2, 900) // 900 raw bytes -> 1200 base64 chars
	uri := attachTestDataURI(raw)
	msgs := []Message{{Role: RoleUser, Content: "look @a.png",
		Parts: []llm.ContentPart{llm.TextPart("look @a.png"), dataURIPart(uri[len("data:image/png;base64,"):])}}}

	if got, want := imageTokensOf(msgs), tools.ImageTokensOf(len(raw)); got != want {
		t.Errorf("imageTokensOf = %d, want %d (decoded bytes / 3)", got, want)
	}
	// The no-decode sizing helper must agree with the real decode, or every
	// data-URI booking is off.
	if got := imageDataURIRawBytes(uri); got != len(raw) {
		t.Errorf("imageDataURIRawBytes = %d, want %d", got, len(raw))
	}
}

// TestImageTokensOfURLAttachmentIsBookedOnce is the URL case: the documented
// upper bound, and exactly one copy of it however long the URL string is.
func TestImageTokensOfURLAttachmentIsBookedOnce(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"plain png url", "https://x.test/a.png"},
		{"long url with a query", "https://cdn.example.com/issues/screenshot.png?width=1200&height=900&sig=" + strings.Repeat("a", 4000)},
		{"http scheme", "http://x.test/b.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []Message{{Role: RoleUser, Parts: []llm.ContentPart{
				llm.TextPart("see it"), urlPart(tt.url)}}}
			want := tools.ImageTokensOf(defaultImageTokenBookingBytes)
			if got := imageTokensOf(msgs); got != want {
				t.Errorf("imageTokensOf = %d, want the single upper bound %d", got, want)
			}
			// The booking must not scale with the URL STRING: a 4000-char URL
			// is still one image of unknown size.
			if got := imageTokensOf([]Message{{Role: RoleUser, Content: "see it",
				Parts: []llm.ContentPart{llm.TextPart("see it"), urlPart("https://x.test/a.png")}}}); got != want {
				t.Errorf("a short URL booked %d, want the same bound %d", got, want)
			}
		})
	}
}

// TestImageTokensOfAttachmentNotDoubleBooked is the heart of the step: an
// image that is BOTH a wire part and a recorded attachment (its `[image: …]`
// marker naming the same reference) is priced once.
func TestImageTokensOfAttachmentNotDoubleBooked(t *testing.T) {
	part := urlPart("https://x.test/a.png")

	// The same image as a part AND as the attachment marker naming it.
	marker := "[image: https://x.test/a.png image/png unknown size attached as an image part]"
	both := []Message{{Role: RoleUser, Content: marker, Parts: []llm.ContentPart{llm.TextPart(marker), part}}}

	one := tools.ImageTokensOf(defaultImageTokenBookingBytes)
	if got := imageTokensOf(both); got != one {
		t.Errorf("an attachment recorded twice on one message booked %d, want it once (%d)", got, one)
	}

	t.Run("two DISTINCT images on one message book twice", func(t *testing.T) {
		// The dedupe key is identity, not presence: a turn with two different
		// screenshots really does cost two images, and collapsing them would
		// under-book the window (the failure that lets context grow past it).
		mark := "[image: https://x.test/a.png …] [image: https://x.test/b.png …]"
		msgs := []Message{{Role: RoleUser, Content: mark, Parts: []llm.ContentPart{
			llm.TextPart(mark),
			urlPart("https://x.test/a.png"),
			urlPart("https://x.test/b.png"),
		}}}
		if got, want := imageTokensOf(msgs), 2*one; got != want {
			t.Errorf("two distinct URL images booked %d, want %d", got, want)
		}
	})

	t.Run("the marker's own text is not the image's price", func(t *testing.T) {
		// The deduped shape must not ALSO pay for the marker string as if the
		// marker were the image: estTurnTokens adds len(Content) for the text,
		// which is ~30 tokens, not the image's ~500k. The image share is what
		// must stay at one booking.
		got := imageTokensOf(both)
		if got != one {
			t.Errorf("image share = %d, want the single bound %d", got, one)
		}
	})
}

// TestImageTokensOfResumedMarkerUnchanged pins the #217 shape: content is the
// marker, Parts is empty, so there is no image in the prompt and the image
// share is zero — booking it at the cap was the bug that inflated a resumed
// turn past a whole window.
func TestImageTokensOfResumedMarkerUnchanged(t *testing.T) {
	marker := "[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]"
	msgs := []Message{{Role: "tool", ToolCallID: "c1", Content: marker}}

	if got := imageTokensOf(msgs); got != 0 {
		t.Errorf("a resumed marker-only result books %d image tokens, want 0", got)
	}
	wantTotal := cache.TokensOf(len(marker)+len("c1")) + imageTokensOf(msgs)
	if got := estTurnTokens(msgs); got != wantTotal {
		t.Errorf("estTurnTokens = %d, want %d (the marker text only)", got, wantTotal)
	}
	if hasImageContent(msgs[0]) {
		t.Error("a marker-only message must not claim image content")
	}
}

// TestImageTokensOfMixedWindow covers the realistic window: a data-URI turn,
// a URL turn, a resumed marker, and plain text — each priced once, nothing
// else charged.
func TestImageTokensOfMixedWindow(t *testing.T) {
	raw := attachTestPNG(6, 300)
	textOnly := Message{Role: RoleUser, Content: "no images here"}
	resumed := Message{Role: "tool", ToolCallID: "c9",
		Content: "[image:old.png image/png 500 bytes ≈166 tokens attached as an image part]"}
	dataMsg := Message{Role: RoleUser, Content: "one",
		Parts: []llm.ContentPart{llm.TextPart("one"), urlPart(attachTestDataURI(raw))}}
	urlMsg := Message{Role: RoleUser, Content: "two",
		Parts: []llm.ContentPart{llm.TextPart("two"), urlPart("https://x.test/c.webp")}}

	msgs := []Message{textOnly, resumed, dataMsg, urlMsg}
	want := tools.ImageTokensOf(len(raw)) + tools.ImageTokensOf(defaultImageTokenBookingBytes)
	if got := imageTokensOf(msgs); got != want {
		t.Errorf("imageTokensOf over the window = %d, want %d", got, want)
	}
	// And the whole-turn estimate still agrees with the two terms:
	// cache.TokensOf over every message's Content (the text half — which for
	// the resumed marker is its ONLY cost) plus the image share.
	if got, wantTotal := estTurnTokens(msgs), cache.TokensOf(sumContents(msgs))+want; got != wantTotal {
		t.Errorf("estTurnTokens = %d, want %d", got, wantTotal)
	}
}

// sumContents mirrors estTurnTokens' text term: len(Content) over every
// message (plus tool-call name/args, unused here).
// TestImageTokensOfURLPartReachesTheWindowSurfaces pins the URL booking where
// it is consumed: the /context legend, the turn estimate that drives
// demotion, and the in-turn demotion estimator. A URL part is not something
// Cortex builds today (an @mentioned image URL is downloaded and sent as a
// data URI — see TestMentionImageURLIsDownloadedNotForwarded), so these
// exercise the backstop the way a resumed or forwarded part would reach it.
func TestImageTokensOfURLPartReachesTheWindowSurfaces(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Request.Vision = true
	cs.StartTranscript()
	t.Cleanup(func() { cs.transcript.Close() })

	cs.Append(Message{Role: RoleSystem, Content: "sys"})
	cs.Append(Message{Role: RoleUser, Content: "the screenshot",
		Parts: []llm.ContentPart{llm.TextPart("the screenshot"), urlPart("https://x.test/a.png")}})
	cs.ws = cs.newWorkingSet(1)

	want := tools.ImageTokensOf(defaultImageTokenBookingBytes)
	if got := cs.hydratedImageTokens(); got != want {
		t.Errorf("hydratedImageTokens = %d, want the single upper bound %d", got, want)
	}
	n := 0
	for _, m := range cs.Request.Messages {
		if hasImageContent(m) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d messages claim image content, want 1", n)
	}

	t.Run("a URL image forces demotion the way a huge one should", func(t *testing.T) {
		// The point of an UPPER bound: a turn carrying an unknown-size image
		// must look expensive enough to demote, or the window overflows.
		turnMsgs := cs.Request.Messages[1:]
		if got := estTurnTokens(turnMsgs); got < want {
			t.Errorf("estTurnTokens = %d, want at least the bound %d", got, want)
		}
		if got := inTurnEstimator(inTurnMsg(turnMsgs[0])); got >= want {
			t.Errorf("inTurnEstimator = %d, want the text-only share (under the image bound %d) — the image half is booked separately", got, want)
		}
	})
}

// TestMentionImageURLIsDownloadedNotForwarded records WHY the URL branch is a
// backstop rather than the main path: an @mentioned image URL is fetched and
// attached as a data URI, so the parts Cortex sends carry real, priceable
// bytes. If this ever flips to forwarding the URL, the upper-bound booking
// stops being a backstop and starts being the norm — worth knowing then.
func TestMentionImageURLIsDownloadedNotForwarded(t *testing.T) {
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	fetched := false
	fetchMentionImage = func(_ context.Context, u string) ([]byte, error) {
		fetched = true
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("fetched %q, want the https address as typed", u)
		}
		return pngFixtureBytes(240), nil
	}

	_, _, images, refused := mentions(setupMentionWorkspace(t),
		"look @https://cdn.example.com/issues/screenshot.png", mentionTestVision())
	if len(refused) > 0 {
		t.Fatalf("a reachable URL image must attach, refused: %v", refused)
	}
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if !fetched {
		t.Error("the URL was not fetched — a forwarded URL is what makes the upper-bound booking the norm")
	}
	if !strings.HasPrefix(images[0].Part.DataURI, "data:image/png;base64,") {
		t.Errorf("attachment = %.40q, want the fetched bytes as a data URI", images[0].Part.DataURI)
	}
	// And the human's reference survives on the attachment for the report line.
	if !strings.Contains(images[0].Ref, "screenshot.png") {
		t.Errorf("Ref = %q, want the typed URL", images[0].Ref)
	}

	t.Run("its booking is the real byte size, not the URL bound", func(t *testing.T) {
		msg := Message{Role: RoleUser, Content: "look", Parts: []llm.ContentPart{
			llm.TextPart("look"), urlPart(images[0].Part.DataURI)}}
		want := tools.ImageTokensOf(len(pngFixtureBytes(240)))
		if got := imageTokensOf([]Message{msg}); got != want {
			t.Errorf("imageTokensOf = %d, want the fetched size's estimate %d (not the %d-token bound)",
				got, want, tools.ImageTokensOf(defaultImageTokenBookingBytes))
		}
		if got := imageTokensOf([]Message{msg}); got >= tools.ImageTokensOf(defaultImageTokenBookingBytes) {
			t.Error("a downloaded image must not be booked at the unknown-size upper bound")
		}
	})
}

func sumContents(msgs []Message) int {
	sum := 0
	for _, m := range msgs {
		sum += len(m.Content)
	}
	return sum
}
