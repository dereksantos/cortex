package llm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Content parts on the wire (issue #216, part 1 of image input #134).
//
// ChatMessage carries either a plain string Content (the default, and
// what every existing caller sets) or a list of structured Parts —
// OpenAI-style content parts: {"type":"text","text":...} and
// {"type":"image_url","image_url":{"url":...}}, where the URL is an
// http(s) URL or a base64 `data:` URI. When Parts is non-empty it is
// what goes on the wire; a text-only message therefore still
// serializes as `"content":"..."`, byte for byte as before.
//
// The same Parts slice is translated to Anthropic's native image block
// (AnthropicImageContent) for the Anthropic Messages path, where an
// image is {"type":"image","source":{"type":"base64"|
// "url",...}} instead of image_url.

// ContentTypeText is the text content part's discriminator.
const ContentTypeText = "text"

// ContentTypeImageURL is the OpenAI-style image content part's
// discriminator (the Anthropic translation is derived from it, see
// AnthropicImageContent).
const ContentTypeImageURL = "image_url"

// ImageDetail is OpenAI's optional fidelity hint on an image part.
// Empty (omitted) lets the backend decide; "auto", "low", and "high"
// are the OpenAI values other OpenAI-compatible servers mirror.
type ImageDetail string

const (
	ImageDetailAuto ImageDetail = "auto"
	ImageDetailLow  ImageDetail = "low"
	ImageDetailHigh ImageDetail = "high"
)

// imageURLRef is the nested payload of an image content part: exactly
// OpenAI's wire shape (one object, `url` plus the optional `detail`
// hint) — nothing non-standard rides here. It stays unexported because
// callers only ever touch ImageURLPart.
type imageURLRef struct {
	URL    string      `json:"url"`
	Detail ImageDetail `json:"detail,omitempty"`
}

// ContentPart is one part of a message's structured content. Text is set
// on a text part; ImageURL on an image part — always the canonical
// OpenAI form, an http(s) URL or a base64 `data:` URI (Anthropic's
// native image block decodes back into it, see UnmarshalJSON). Detail is
// the optional fidelity hint for images; MediaType is the image's MIME
// type when the part was decoded from a shape that states it separately
// (Anthropic's source block) — the OpenAI shape carries it inside the
// data URI instead.
//
// MarshalJSON decides the emitted shape; UnmarshalJSON accepts both the
// OpenAI and Anthropic request shapes, so a marshaled message round-trips
// (which is what lets tests and the debug echoes inspect a wire body).
type ContentPart struct {
	Type      string
	Text      string
	ImageURL  string
	MediaType string
	Detail    ImageDetail
}

// TextPart builds a text content part.
func TextPart(text string) ContentPart {
	return ContentPart{Type: ContentTypeText, Text: text}
}

// ImageURLPart builds an image content part from an http(s) URL or a
// base64 `data:` URI (the form base64-encoded image bytes take on the
// wire: "data:image/png;base64,iVBORw0...").
func ImageURLPart(url string, detail ImageDetail) ContentPart {
	return ContentPart{Type: ContentTypeImageURL, ImageURL: url, Detail: detail}
}

// HasImage reports whether the part is an image part.
func (p ContentPart) HasImage() bool { return p.Type == ContentTypeImageURL }

// MarshalJSON emits the OpenAI content-part shape.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	switch p.Type {
	case ContentTypeImageURL:
		return json.Marshal(struct {
			Type     string      `json:"type"`
			ImageURL imageURLRef `json:"image_url"`
		}{
			Type:     ContentTypeImageURL,
			ImageURL: imageURLRef{URL: p.ImageURL, Detail: p.Detail},
		})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: ContentTypeText, Text: p.Text})
	}
}

// UnmarshalJSON accepts the OpenAI part shape and Anthropic's text/image
// block shapes, so a request body can be decoded back into ContentParts.
func (p *ContentPart) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ImageURL  json.RawMessage `json:"image_url"`
		Source    json.RawMessage `json:"source"`
		MediaType string          `json:"media_type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("content part: %w", err)
	}
	p.Type = raw.Type
	p.Text = raw.Text
	p.MediaType = raw.MediaType
	switch {
	case len(raw.ImageURL) > 0:
		p.Type = ContentTypeImageURL
		// OpenAI accepts both a bare string and the {"url":...} object.
		var s string
		if json.Unmarshal(raw.ImageURL, &s) == nil {
			p.ImageURL = s
			return nil
		}
		var ref imageURLRef
		if err := json.Unmarshal(raw.ImageURL, &ref); err != nil {
			return fmt.Errorf("content part: image_url: %w", err)
		}
		p.ImageURL = ref.URL
		p.Detail = ref.Detail
	case len(raw.Source) > 0:
		// Anthropic's native block: normalize to the canonical OpenAI
		// shape so one representation flows through the gate.
		p.Type = ContentTypeImageURL
		var src struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		}
		if err := json.Unmarshal(raw.Source, &src); err != nil {
			return fmt.Errorf("content part: source: %w", err)
		}
		if src.MediaType != "" {
			p.MediaType = src.MediaType
		}
		switch src.Type {
		case "base64":
			// Rebuild the canonical data URI, so one representation flows
			// through the gate and the OpenAI translation alike. A base64
			// source with no media_type can't be rebuilt into one, so it
			// is rejected here rather than carried as a malformed part.
			if src.MediaType == "" {
				return fmt.Errorf("content part: anthropic base64 image source has no media_type")
			}
			p.ImageURL = "data:" + src.MediaType + ";base64," + src.Data
		default:
			p.ImageURL = src.URL
		}
	}
	return nil
}

// ValidateContentParts checks that every part is one a vision-capable
// backend will accept: a text part, or an image part carrying an http(s)
// URL or a base64 `data:` URI. It is the single shape gate every
// image-bearing request goes through, so a malformed part surfaces as a
// local error instead of a provider 400.
func ValidateContentParts(parts []ContentPart) error {
	for _, p := range parts {
		switch p.Type {
		case ContentTypeText:
			// Any text, including empty (a leading image part rides
			// alongside an empty text instruction).
		case ContentTypeImageURL:
			if err := validateImageURL(p.ImageURL); err != nil {
				return err
			}
		default:
			return fmt.Errorf("content part: unsupported type %q (want %q or %q)", p.Type, ContentTypeText, ContentTypeImageURL)
		}
	}
	return nil
}

// validateImageURL accepts http(s) URLs and base64 data URIs, rejecting
// everything else — a local file path or a javascript:/file: URI must
// never reach a provider. A data URI must state its media type: a bare
// "data:;base64,..." is not something any provider accepts, and taking
// it would mean AnthropicImageContent had nowhere to get media_type
// from — i.e. a silent drop waiting to happen.
func validateImageURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("image content part: empty url")
	}
	if strings.HasPrefix(raw, "data:") {
		if !strings.Contains(raw, ";base64,") {
			return fmt.Errorf("image content part: data uri must be base64-encoded: %s", summarizeURL(raw))
		}
		if strings.HasPrefix(raw, "data:;") {
			return fmt.Errorf("image content part: data uri has no media type (want data:image/<fmt>;base64,...)")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("image content part: parse url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("image content part: url scheme %q unsupported (want http, https, or a base64 data: uri)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("image content part: url has no host: %s", summarizeURL(raw))
	}
	return nil
}

// summarizeURL shortens a URL for an error message; a base64 data URI is
// replaced by its media type so the error doesn't carry megabytes.
func summarizeURL(raw string) string {
	if strings.HasPrefix(raw, "data:") {
		media := strings.TrimPrefix(raw, "data:")
		if i := strings.IndexByte(media, ';'); i >= 0 {
			media = media[:i]
		}
		return "data:" + media + ";base64,<...>"
	}
	if len(raw) > 80 {
		return raw[:80] + "…"
	}
	return raw
}

// AnthropicImageContent translates an image content part into Anthropic's
// native image block ({"type":"image","source":{...}}), the shape the
// Anthropic Messages API takes in place of OpenAI's image_url part.
// Non-image parts return false so callers keep emitting their own text
// blocks. Only the Anthropic-native path (pkg/llm's AnthropicClient)
// needs this; every OpenAI-compatible backend takes the part as-is.
func AnthropicImageContent(p ContentPart) (map[string]any, bool) {
	if !p.HasImage() {
		return nil, false
	}
	raw := p.ImageURL
	if strings.HasPrefix(raw, "data:") {
		rest := strings.TrimPrefix(raw, "data:")
		media, data, ok := strings.Cut(rest, ",")
		if !ok {
			return nil, false
		}
		mediaType := p.MediaType
		if i := strings.IndexByte(media, ';'); i >= 0 {
			mediaType = media[:i]
		}
		if mediaType == "" {
			return nil, false
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       strings.TrimPrefix(data, "base64,"),
			},
		}, true
	}
	if raw == "" {
		return nil, false
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "url",
			"url":  raw,
		},
	}, true
}

// ErrModelNoVision is the sentinel the vision gate returns when a request
// carries image parts but the target model isn't known to accept them.
// Callers match it with errors.Is; the wrapped message names the model.
var ErrModelNoVision = fmt.Errorf("model does not accept image input")

// NewVisionUnsupportedError builds the gate's error naming the model. It
// wraps ErrModelNoVision with %w, so errors.Is(err, ErrModelNoVision)
// holds while Error() reads as the one model-specific sentence.
func NewVisionUnsupportedError(model string) error {
	return fmt.Errorf("model %q cannot accept image content parts — vision is disabled for it: set models.<role>.vision to true or route the turn to a vision-capable model: %w", model, ErrModelNoVision)
}

// HasImageParts reports whether any part is an image part — the cheap
// predicate a caller uses to decide whether the gate must run at all
// (text-only requests never touch it).
func HasImageParts(parts []ContentPart) bool {
	for _, p := range parts {
		if p.HasImage() {
			return true
		}
	}
	return false
}

// GateImages is the vision capability gate: it accepts a request's image
// parts when the model is known to take images, and returns an error
// naming the model when it isn't. It never drops parts silently — a
// text-only model gets ErrModelNoVision and the caller must surface it.
//
// allow is the caller's resolved verdict for the target model: pkg/llm
// stays agnostic about where that comes from — the OpenRouter catalog's
// declared input modalities, a fleet advert, or the per-role
// `models.<role>.vision` config flag. In cmd/cortex that precedence is
// resolved once in resolveBinding and carried as AgentRequest.Vision.
func GateImages(parts []ContentPart, model string, allow bool) error {
	if !HasImageParts(parts) {
		return nil
	}
	if err := ValidateContentParts(parts); err != nil {
		return err
	}
	if !allow {
		return NewVisionUnsupportedError(model)
	}
	return nil
}

// modelAcceptsImages reads an OpenRouter catalog entry's declared input
// modalities (architecture.input_modalities). Absent or empty means the
// catalog has nothing to say — reported as false, so the caller falls
// back to the per-role config flag rather than assuming vision.
func modelAcceptsImages(modalities []string) bool {
	for _, m := range modalities {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true
		}
	}
	return false
}
