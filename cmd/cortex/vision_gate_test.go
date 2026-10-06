package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/pkg/llm"
)

// TestAgentRequestVisionGate covers #216's gate on cmd/cortex's own
// transport: a message carrying image parts is refused with a
// model-naming error before the request leaves, and accepted (sent as an
// OpenAI parts array) when the binding says the model is vision-capable.
// visionOKResponse is the blocking chat-completions body the gate test
// stand-in backend serves.
const visionOKResponse = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

func TestAgentRequestVisionGate(t *testing.T) {
	img := []llm.ContentPart{
		llm.TextPart("what's in this image?"),
		llm.ImageURLPart("data:image/png;base64,iVBORw0KGgo=", ""),
	}
	tests := []struct {
		name     string
		vision   bool
		wantErr  bool
		wantWire string // substring of the wire body when sent
	}{
		{"vision off: clear error naming the model", false, true, ""},
		{"vision on: parts array on the wire", true, false, `"type":"image_url"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body = string(raw)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(visionOKResponse))
			}))
			defer srv.Close()

			req := CortexArgs{}.Request()
			req.Model = "acme/text-model"
			req.BaseURL = srv.URL
			req.MaxAttempts = 1
			req.Vision = tt.vision
			req.Messages = append(req.Messages[:1], Message{Role: RoleUser, Parts: img})

			_, err := req.Send(context.Background())
			if tt.wantErr {
				if !errors.Is(err, llm.ErrModelNoVision) {
					t.Fatalf("want ErrModelNoVision, got %v", err)
				}
				if !strings.Contains(err.Error(), "acme/text-model") {
					t.Errorf("error must name the model: %v", err)
				}
				if body != "" {
					t.Errorf("nothing should have been sent, got: %s", body)
				}
				return
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if !strings.Contains(body, tt.wantWire) {
				t.Errorf("wire body lacks %q: %s", tt.wantWire, body)
			}
		})
	}

	t.Run("text-only request serialization unchanged", func(t *testing.T) {
		req := CortexArgs{}.Request()
		req.Messages = []Message{{Role: RoleUser, Content: "hi"}}
		b, err := json.Marshal(req.wireMessages())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(b), `{"role":"user","content":"hi"}`) {
			t.Errorf("text-only wire changed: %s", b)
		}
	})
}

// TestResolveBindingVision covers the config side of the gate: explicit
// models.<role>.vision wins; an OpenRouter model without it falls back to
// the capability table's vision tag; everything else defaults to false.
// The live-catalog tier of the same precedence is covered by
// TestApplyCatalogVision and TestPreflightStampsVisionFromCatalog.
func TestResolveBindingVision(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name    string
		backend Backend
		models  map[string]ModelSpec
		want    bool
	}{
		{"explicit true", Backend{Type: "litellm"}, map[string]ModelSpec{roleCode: {Model: "m", Vision: &yes}}, true},
		{"explicit false", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "acme/vl-8b", Vision: &no}}, false},
		{"openrouter vision-suffixed model", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "acme/gpt-4o-vision"}}, true},
		{"openrouter plain model", Backend{Type: "openrouter"}, map[string]ModelSpec{roleCode: {Model: "qwen/qwen3-coder"}}, false},
		{"non-openrouter unset stays false", Backend{Type: "litellm"}, map[string]ModelSpec{roleCode: {Model: "acme/gpt-4o-vision"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Backend: tt.backend, Models: tt.models}
			if got := cfg.resolveBinding(roleCode, nil).VisionEnabled(); got != tt.want {
				t.Errorf("VisionEnabled()=%v want %v", got, tt.want)
			}
		})
	}
}

// TestApplyCatalogVision covers the catalog tier of the #216 precedence:
// the OpenRouter listing's declared input modalities settle a binding whose
// verdict config left unset — including a model whose id carries no vision
// tag at all, which the name heuristic alone would get wrong — while an
// explicit config verdict is never touched and an id the catalog doesn't
// list is left for the heuristic.
func TestApplyCatalogVision(t *testing.T) {
	yes := true
	catalog := []llm.OpenRouterModel{
		{ID: "openai/gpt-4o", AcceptsImages: true}, // no vision tag in the id
		{ID: "qwen/qwen3-coder", AcceptsImages: false},
	}
	specs := []*ModelSpec{
		{Model: "openai/gpt-4o"},
		{Model: "qwen/qwen3-coder"},
		{Model: "acme/vl-8b"},                  // not listed: untouched
		{Model: "openai/gpt-4o", Vision: &yes}, // explicit: untouched
		{Model: ""},                            // empty id: untouched
	}
	applyCatalogVision(catalog, specs...)

	want := []struct {
		settled bool
		enabled bool
	}{
		{true, true},   // catalog says image input, id says nothing
		{true, false},  // catalog says text-only
		{false, false}, // unlisted stays nil for the heuristic
		{true, true},   // explicit true survives a listing that agrees
		{false, false},
	}
	for i, w := range want {
		if got := specs[i].Vision; (got != nil) != w.settled {
			t.Errorf("spec %d settled=%v want %v (Vision=%v)", i, got != nil, w.settled, got)
			continue
		}
		if got := specs[i].VisionEnabled(); got != w.enabled {
			t.Errorf("spec %d VisionEnabled()=%v want %v", i, got, w.enabled)
		}
	}

	t.Run("no catalog is a no-op", func(t *testing.T) {
		spec := ModelSpec{Model: "openai/gpt-4o"}
		applyCatalogVision(nil, &spec)
		if spec.Vision != nil {
			t.Error("a missing catalog must leave the verdict unset, not assert false")
		}
	})
}

// TestPreflightStampsVisionFromCatalog is the end-to-end wiring of the
// catalog tier: the listing the startup preflight fetches settles both
// role bindings, and is handed back so the session can reuse it for
// /model switches. A network failure leaves both verdicts unset (the
// heuristic in resolveBinding then decides), never a asserted false.
func TestPreflightStampsVisionFromCatalog(t *testing.T) {
	served := []llm.OpenRouterModel{
		{ID: "openai/gpt-4o", AcceptsImages: true},
		{ID: "qwen/qwen3-coder", AcceptsImages: false},
	}
	code := ModelSpec{Model: "openai/gpt-4o"}
	study := ModelSpec{Model: "qwen/qwen3-coder"}

	gotCode, gotStudy, catalog := preflightCuratedModels(context.Background(), openrouterCfg(),
		code, study, t.TempDir(), fakeListModels(served, nil))

	if !gotCode.VisionEnabled() {
		t.Error("code bound to a catalog image-input model: vision must be true")
	}
	if gotStudy.VisionEnabled() {
		t.Error("study bound to a catalog text-only model: vision must be false")
	}
	if len(catalog) != len(served) {
		t.Errorf("catalog returned = %d entries, want %d", len(catalog), len(served))
	}

	t.Run("explicit config wins over the catalog", func(t *testing.T) {
		no := false
		cfg := openrouterCfg()
		cfg.Models = map[string]ModelSpec{roleCode: {Model: "openai/gpt-4o", Vision: &no}}
		got, _, _ := preflightCuratedModels(context.Background(), cfg,
			cfg.resolveBinding(roleCode, nil), ModelSpec{}, t.TempDir(), fakeListModels(served, nil))
		if got.VisionEnabled() {
			t.Error("models.code.vision=false must survive a catalog that says image input")
		}
	})

	t.Run("catalog fetch failure leaves the verdict unset", func(t *testing.T) {
		gotCode, gotStudy, catalog := preflightCuratedModels(context.Background(), openrouterCfg(),
			code, study, t.TempDir(), fakeListModels(nil, errors.New("connection refused")))
		if gotCode.Vision != nil || gotStudy.Vision != nil {
			t.Error("a failed catalog fetch must leave vision unset for the name heuristic")
		}
		if catalog != nil {
			t.Error("no listing must be handed back when the fetch failed")
		}
	})
}

// TestClearKeepsVision pins that /clear carries the vision verdict across
// with the rest of the model binding (#216): a cleared conversation on a
// vision-capable model must still accept an image, and one on a text-only
// model must still refuse it — clearing history is not a re-resolution.
func TestClearKeepsVision(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name   string
		vision bool
	}{
		{"vision model keeps it", true},
		{"text-only model keeps its refusal", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &CortexSession{Request: CortexArgs{}.Request()}
			cs.Request.Model = "acme/model"
			cs.Request.Vision = tt.vision
			cs.Clear()
			defer cs.transcript.Close()

			if cs.Request.Vision != tt.vision {
				t.Fatalf("after Clear(): Vision=%v want %v", cs.Request.Vision, tt.vision)
			}
			msg := Message{Role: RoleUser, Parts: []llm.ContentPart{llm.ImageURLPart("https://x.test/a.png", "")}}
			err := cs.Request.checkVision([]Message{msg})
			if tt.vision && err != nil {
				t.Errorf("vision verdict lost across clear: %v", err)
			}
			if !tt.vision && !errors.Is(err, llm.ErrModelNoVision) {
				t.Errorf("want ErrModelNoVision across clear, got %v", err)
			}
		})
	}
}

// TestVisionForModel covers the /model-switch half of the #216 precedence
// (SetModel's helper): the code role's explicit flag for that role's own
// configured model, then the catalog, then the id's capability tags, then
// false — the same ordering resolveBinding settles, so the two paths can't
// disagree.
func TestVisionForModel(t *testing.T) {
	yes, no := true, false
	catalog := []llm.OpenRouterModel{
		{ID: "openai/gpt-4o", AcceptsImages: true},
		{ID: "qwen/qwen3-coder", AcceptsImages: false},
	}
	tests := []struct {
		name    string
		cfg     *Config
		catalog []llm.OpenRouterModel
		model   string
		want    bool
	}{
		{"switch back to the configured vision model", &Config{
			Backend: Backend{Type: "openrouter"},
			Models:  map[string]ModelSpec{roleCode: {Model: "acme/text-flagged", Vision: &yes}},
		}, nil, "acme/text-flagged", true},
		{"explicit false on the configured model wins over a vision-tagged id", &Config{
			Backend: Backend{Type: "openrouter"},
			Models:  map[string]ModelSpec{roleCode: {Model: "acme/model", Vision: &no}},
		}, catalog, "acme/model", false},
		{"catalog vision model whose id has no tag", openrouterCfg(), catalog, "openai/gpt-4o", true},
		// The catalog's entry for an id settles it; an id it doesn't list
		// falls to the tag tier, which is why "acme/llava-7b" below is true
		// while the listed text-only id above stays false.
		{"unlisted openrouter model with a vision tag", openrouterCfg(), catalog, "acme/llava-7b", true},
		{"unlisted openrouter model, no tag", openrouterCfg(), catalog, "acme/plain-model", false},
		{"no catalog at all, vision tag", openrouterCfg(), nil, "acme/qwen-vl-instruct", true},
		{"non-openrouter backend, tag and catalog both ignored", &Config{Backend: Backend{Type: "litellm"}}, catalog, "openai/gpt-4o", false},
		{"unknown model", openrouterCfg(), catalog, "someone/unknown", false},
		{"nil config", nil, catalog, "openai/gpt-4o", false},
		{"empty model id", openrouterCfg(), catalog, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := visionForModel(tt.cfg, tt.catalog, tt.model); got != tt.want {
				t.Errorf("visionForModel(%q)=%v want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestSetModelVision covers the /model switch itself: the verdict is
// re-derived with visionForModel rather than flattened to false, so
// switching back to a vision-declared model restores images and switching
// to a text-only one refuses them.
func TestSetModelVision(t *testing.T) {
	yes := true
	tests := []struct {
		name    string
		cfg     *Config
		catalog []llm.OpenRouterModel
		to      string
		want    bool
	}{
		{"back to the configured vision model", &Config{
			Backend: Backend{Type: "openrouter"},
			Models:  map[string]ModelSpec{roleCode: {Model: "acme/vision-pin", Vision: &yes}},
		}, nil, "acme/vision-pin", true},
		{"to a catalog vision model", openrouterCfg(),
			[]llm.OpenRouterModel{{ID: "openai/gpt-4o", AcceptsImages: true}}, "openai/gpt-4o", true},
		{"to a model nothing knows", openrouterCfg(),
			[]llm.OpenRouterModel{{ID: "openai/gpt-4o", AcceptsImages: true}}, "someone/unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &CortexSession{Config: tt.cfg, catalog: tt.catalog, Request: CortexArgs{}.Request()}
			cs.Request.Vision = false
			cs.SetModel(tt.to)
			if cs.Request.Vision != tt.want {
				t.Errorf("after SetModel(%q): Vision=%v want %v", tt.to, cs.Request.Vision, tt.want)
			}
		})
	}

	t.Run("switching to a text-only model refuses images", func(t *testing.T) {
		cs := &CortexSession{
			Config:  openrouterCfg(),
			Request: CortexArgs{}.Request(),
		}
		cs.Request.Vision = true
		cs.SetModel("acme/text-model")
		if cs.Request.Vision {
			t.Fatal("a switch to a model the catalog can't vouch for must clear vision")
		}
		msg := Message{Role: RoleUser, Parts: []llm.ContentPart{llm.ImageURLPart("https://x.test/a.png", "")}}
		if err := cs.Request.checkVision([]Message{msg}); !errors.Is(err, llm.ErrModelNoVision) {
			t.Errorf("want ErrModelNoVision after the switch, got %v", err)
		}
	})
}
