package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolateBootstrap gives a test its own CORTEX_HOME/HOME/CWD with no config
// and no $CORTEX_BACKEND, and returns the user config path.
func isolateBootstrap(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CORTEX_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORTEX_BACKEND", "")
	t.Chdir(t.TempDir())
	return filepath.Join(home, "config.json")
}

// mediumTier is the 16 GiB suggestion: a 14b parameter cap, 32k window.
func mediumTier() ModelSuggestion { return suggestModels(16*bytesPerGiB, "arm64") }

// fakeOllama is a scripted Ollama: /api/tags lists tags, /api/show answers
// from shows, /api/create records each request and answers createStatus.
type fakeOllama struct {
	tags         []map[string]any
	shows        map[string]map[string]any
	createStatus int
	delay        time.Duration

	mu      sync.Mutex
	created []map[string]any
}

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-r.Context().Done():
				return
			}
		}
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": f.tags})
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			show, ok := f.shows[body.Model]
			if !ok {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(show)
		case "/api/create":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.created = append(f.created, body)
			f.mu.Unlock()
			status := f.createStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = io.WriteString(w, `{"status":"success"}`)
			} else {
				_, _ = io.WriteString(w, `{"error":"create refused"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ollamaTag(name, size string) map[string]any {
	return map[string]any{"name": name, "details": map[string]any{"parameter_size": size, "family": "x"}}
}

func ollamaShowReply(ctxLen int, params string, caps ...string) map[string]any {
	return map[string]any{
		"model_info":   map[string]any{"general.architecture": "x", "x.context_length": ctxLen},
		"parameters":   params,
		"capabilities": caps,
	}
}

// standardOllama has two tool-capable models (the 14b coder should win on
// a 16 GiB machine), a tool-less one, an embedder and a cloud entry.
func standardOllama() *fakeOllama {
	return &fakeOllama{
		tags: []map[string]any{
			ollamaTag("llama3.2:3b", "3.2B"),
			ollamaTag("qwen2.5-coder:14b", "14.8B"),
			ollamaTag("xlam-1b-fc-r:latest", "1.35B"),
			ollamaTag("nomic-embed-text:latest", "137M"),
			{"name": "gpt-oss:120b-cloud", "remote_host": "https://ollama.com", "details": map[string]any{"parameter_size": "116.8B"}},
		},
		shows: map[string]map[string]any{
			"llama3.2:3b":             ollamaShowReply(131072, "", "completion", "tools"),
			"qwen2.5-coder:14b":       ollamaShowReply(32768, "stop \"<|im_end|>\"", "completion", "tools", "insert"),
			"xlam-1b-fc-r:latest":     ollamaShowReply(16384, "", "completion"),
			"nomic-embed-text:latest": ollamaShowReply(2048, "num_ctx 8192", "embedding"),
		},
	}
}

func specFor(kind localServerKind, url string) localServerSpec {
	name := map[localServerKind]string{kindOllama: "Ollama", kindLMStudio: "LM Studio", kindLlamaCpp: "llama.cpp"}[kind]
	return localServerSpec{Kind: kind, Name: name, Root: url}
}

// closedURL is a URL nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

func TestParseParamsB(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"4.0B", 4},
		{"14.8B", 14.8},
		{"137M", 0.137},
		{"qwen2.5-coder:14b", 14},
		{"qwen2.5-coder:1.5b", 1.5},
		{"llama3.2:3b", 3},
		{"hf.co/unsloth/Qwen3-4B-Instruct-2507-GGUF:Q4_K_M", 4},
		{"qwen3-coder:30b-a3b", 30},
		{"gpt-oss:20b", 20},
		{"mixtral-8x7b", 0},
		{"llama3.2", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parseParamsB(tt.in); got != tt.want {
				t.Errorf("parseParamsB(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDerivedOllamaName(t *testing.T) {
	tests := []struct {
		base   string
		window int
		want   string
	}{
		{"qwen2.5-coder:14b", 32768, "cortex-qwen2.5-coder:14b-32k"},
		{"llama3.2", 8192, "cortex-llama3.2:latest-8k"},
		{"hf.co/unsloth/Qwen3-4B-Instruct-2507-GGUF:Q4_K_M", 32768, "cortex-qwen3-4b-instruct-2507-gguf:q4_k_m-32k"},
		{"localhost:5000/team/model", 65536, "cortex-model:latest-64k"},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := derivedOllamaName(tt.base, tt.window); got != tt.want {
				t.Errorf("derivedOllamaName(%q, %d) = %q, want %q", tt.base, tt.window, got, tt.want)
			}
		})
	}
}

// TestRankLocalModels pins the pick heuristic: drop embedders and tool-less
// models; loaded first; then models that fit the RAM cap by parameter count,
// coder-tuned counted double and known tool-calling families 1.5x; models
// over the cap last, smallest first.
func TestRankLocalModels(t *testing.T) {
	m := func(id string, p float64) localModel { return localModel{ID: id, ParamsB: p} }
	tests := []struct {
		name   string
		models []localModel
		capB   float64
		want   string // the top pick, "" for none
	}{
		{"largest fitting coder wins", []localModel{m("qwen2.5-coder:7b", 7), m("qwen2.5-coder:14b", 14.8), m("qwen2.5-coder:32b", 32)}, 14, "qwen2.5-coder:14b"},
		{"over the cap's slack does not fit", []localModel{m("qwen2.5-coder:7b", 7), m("mistral-small:22b", 22)}, 14, "qwen2.5-coder:7b"},
		{"coder beats a bigger general model", []localModel{m("llama3.1:8b", 8), m("qwen2.5-coder:7b", 7)}, 14, "qwen2.5-coder:7b"},
		{"a 4b instruct beats a 1.5b coder", []localModel{m("qwen2.5-coder:1.5b", 1.5), m("qwen3-4b-2507:latest", 4)}, 14, "qwen3-4b-2507:latest"},
		{"known family beats unknown name of similar size", []localModel{m("vibethinker:latest", 3.1), m("llama3.2:3b", 3.2)}, 14, "llama3.2:3b"},
		{"embedders and tool-less dropped", []localModel{{ID: "nomic-embed-text", ParamsB: 0.1, Embedding: true}, {ID: "xlam:1b", ParamsB: 1, NoTools: true}}, 14, ""},
		{"only oversized: smallest of them", []localModel{m("llama3.3:70b", 70), m("qwen2.5-coder:32b", 32)}, 14, "qwen2.5-coder:32b"},
		{"fitting beats oversized", []localModel{m("qwen2.5-coder:32b", 32), m("llama3.2:3b", 3)}, 14, "llama3.2:3b"},
		{"loaded first", []localModel{m("qwen2.5-coder:14b", 14), {ID: "llama-3.2-3b-instruct", ParamsB: 3, Loaded: true}}, 14, "llama-3.2-3b-instruct"},
		{"unknown size still eligible", []localModel{m("my-model", 0)}, 14, "my-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rankLocalModels(tt.models, tt.capB)
			top := ""
			if len(got) > 0 {
				top = got[0].ID
			}
			if top != tt.want {
				t.Errorf("rankLocalModels top = %q, want %q (ranked %+v)", top, tt.want, got)
			}
		})
	}
}

// TestTierParamCap pins that the RAM fit uses suggestModels' tiers.
func TestTierParamCap(t *testing.T) {
	tests := []struct {
		gib  uint64
		want float64
	}{{4, 3}, {8, 7}, {16, 14}, {32, 32}, {128, 32}}
	for _, tt := range tests {
		if got := tierParamCap(suggestModels(tt.gib*bytesPerGiB, "arm64")); got != tt.want {
			t.Errorf("tierParamCap(%d GiB) = %v, want %v", tt.gib, got, tt.want)
		}
	}
}

// TestDetectLocal drives detectLocal against fake servers of each kind.
func TestDetectLocal(t *testing.T) {
	lmStudio := func(t *testing.T, models []map[string]any) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v0/models" {
				_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	llamaCpp := func(t *testing.T, id string, nParams float64, nCtx int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/models":
				_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
					{"id": id, "owned_by": "llamacpp", "meta": map[string]any{"n_params": nParams, "n_ctx_train": 32768}},
				}})
			case "/props":
				_ = json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]any{"n_ctx": nCtx}})
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}

	type want struct {
		server, base, model string
		window              int
		derive              bool
		noteHas             string
		hintHas             string // when no offer
	}
	tests := []struct {
		name  string
		specs func(t *testing.T) []localServerSpec
		want  want
	}{
		{
			name: "ollama: coder picked, num_ctx unset => derived variant",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindOllama, standardOllama().server(t).URL)}
			},
			want: want{server: "Ollama", base: "qwen2.5-coder:14b", model: "cortex-qwen2.5-coder:14b-32k", window: 32768, derive: true},
		},
		{
			name: "ollama: num_ctx already covers the window => model used as is",
			specs: func(t *testing.T) []localServerSpec {
				f := standardOllama()
				f.shows["qwen2.5-coder:14b"] = ollamaShowReply(32768, "num_ctx                        32768", "completion", "tools")
				return []localServerSpec{specFor(kindOllama, f.server(t).URL)}
			},
			want: want{server: "Ollama", base: "qwen2.5-coder:14b", model: "qwen2.5-coder:14b", window: 32768},
		},
		{
			name: "ollama: window capped by the model's own context length",
			specs: func(t *testing.T) []localServerSpec {
				f := &fakeOllama{
					tags:  []map[string]any{ollamaTag("qwen2.5-coder:7b", "7.6B")},
					shows: map[string]map[string]any{"qwen2.5-coder:7b": ollamaShowReply(16384, "", "completion", "tools")},
				}
				return []localServerSpec{specFor(kindOllama, f.server(t).URL)}
			},
			want: want{server: "Ollama", base: "qwen2.5-coder:7b", model: "cortex-qwen2.5-coder:7b-16k", window: 16384, derive: true},
		},
		{
			name: "ollama: top-ranked model lacks tools => next one",
			specs: func(t *testing.T) []localServerSpec {
				f := standardOllama()
				f.shows["qwen2.5-coder:14b"] = ollamaShowReply(32768, "", "completion")
				return []localServerSpec{specFor(kindOllama, f.server(t).URL)}
			},
			want: want{server: "Ollama", base: "llama3.2:3b", model: "cortex-llama3.2:3b-32k", window: 32768, derive: true},
		},
		{
			name: "ollama: no usable model => pull hint sized to RAM",
			specs: func(t *testing.T) []localServerSpec {
				f := &fakeOllama{
					tags: []map[string]any{ollamaTag("nomic-embed-text:latest", "137M"), ollamaTag("xlam-1b-fc-r:latest", "1.35B")},
					shows: map[string]map[string]any{
						"nomic-embed-text:latest": ollamaShowReply(2048, "", "embedding"),
						"xlam-1b-fc-r:latest":     ollamaShowReply(16384, "", "completion"),
					},
				}
				return []localServerSpec{specFor(kindOllama, f.server(t).URL)}
			},
			want: want{hintHas: "ollama pull qwen2.5-coder:14b"},
		},
		{
			name: "ollama: no models at all => pull hint",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindOllama, (&fakeOllama{}).server(t).URL)}
			},
			want: want{hintHas: "ollama pull qwen2.5-coder:14b"},
		},
		{
			name: "ollama down, LM Studio with a loaded model => LM Studio at its loaded context",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{
					specFor(kindOllama, closedURL(t)),
					specFor(kindLMStudio, lmStudio(t, []map[string]any{
						{"id": "text-embedding-nomic-embed-text-v1.5", "type": "embeddings", "state": "loaded"},
						{"id": "qwen2.5-coder-14b-instruct", "type": "llm", "state": "not-loaded", "max_context_length": 32768},
						{"id": "qwen2.5-coder-7b-instruct", "type": "llm", "state": "loaded", "max_context_length": 32768, "loaded_context_length": 16384},
					})),
				}
			},
			want: want{server: "LM Studio", base: "qwen2.5-coder-7b-instruct", model: "qwen2.5-coder-7b-instruct", window: 16384, noteHas: "lms load qwen2.5-coder-7b-instruct --context-length 32768"},
		},
		{
			name: "LM Studio, nothing loaded => note to load with the window",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindLMStudio, lmStudio(t, []map[string]any{
					{"id": "qwen2.5-coder-14b-instruct", "type": "llm", "state": "not-loaded", "max_context_length": 32768},
				}))}
			},
			want: want{server: "LM Studio", base: "qwen2.5-coder-14b-instruct", model: "qwen2.5-coder-14b-instruct", window: 32768, noteHas: "--context-length 32768"},
		},
		{
			name: "LM Studio with only an embedder => hint",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindLMStudio, lmStudio(t, []map[string]any{
					{"id": "text-embedding-nomic-embed-text-v1.5", "type": "embeddings"},
				}))}
			},
			want: want{hintHas: "LM Studio is running"},
		},
		{
			name: "llama.cpp: window from the server's n_ctx",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindLlamaCpp, llamaCpp(t, "qwen2.5-coder-7b-instruct-q4_k_m.gguf", 7.6e9, 65536))}
			},
			want: want{server: "llama.cpp", base: "qwen2.5-coder-7b-instruct-q4_k_m.gguf", model: "qwen2.5-coder-7b-instruct-q4_k_m.gguf", window: 32768},
		},
		{
			name: "llama.cpp started with a small -c => window shrinks, note says restart",
			specs: func(t *testing.T) []localServerSpec {
				return []localServerSpec{specFor(kindLlamaCpp, llamaCpp(t, "model.gguf", 7.6e9, 4096))}
			},
			want: want{server: "llama.cpp", base: "model.gguf", model: "model.gguf", window: 4096, noteHas: "-c 32768"},
		},
		{
			name:  "nothing reachable => no offer, no hint",
			specs: func(t *testing.T) []localServerSpec { return []localServerSpec{specFor(kindOllama, closedURL(t))} },
			want:  want{},
		},
		{
			name: "a non-server on the port (HTML 404) => not detected",
			specs: func(t *testing.T) []localServerSpec {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, "<html>hello</html>")
				}))
				t.Cleanup(srv.Close)
				return []localServerSpec{specFor(kindLlamaCpp, srv.URL)}
			},
			want: want{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			det := detectLocal(t.Context(), &http.Client{}, tt.specs(t), mediumTier())
			if tt.want.server == "" {
				if det.Offer != nil {
					t.Fatalf("offer = %+v, want none", *det.Offer)
				}
				hints := strings.Join(det.Hints, "\n")
				if tt.want.hintHas == "" && hints != "" {
					t.Errorf("hints = %q, want none", hints)
				}
				if !strings.Contains(hints, tt.want.hintHas) {
					t.Errorf("hints = %q, want to contain %q", hints, tt.want.hintHas)
				}
				return
			}
			o := det.Offer
			if o == nil {
				t.Fatalf("no offer (hints %q), want %s/%s", det.Hints, tt.want.server, tt.want.base)
			}
			if o.Spec.Name != tt.want.server || o.BaseModel != tt.want.base || o.Model != tt.want.model ||
				o.Window != tt.want.window || o.DeriveNumCtx != tt.want.derive {
				t.Errorf("offer = {%s base=%s model=%s window=%d derive=%v}, want {%s base=%s model=%s window=%d derive=%v}",
					o.Spec.Name, o.BaseModel, o.Model, o.Window, o.DeriveNumCtx,
					tt.want.server, tt.want.base, tt.want.model, tt.want.window, tt.want.derive)
			}
			if !strings.Contains(o.Note, tt.want.noteHas) {
				t.Errorf("note = %q, want to contain %q", o.Note, tt.want.noteHas)
			}
		})
	}
}

// TestDetectLocalSlowServerIsBounded pins the startup cost: a server that
// never answers is given up on after the listing deadline, and the probes
// run concurrently (three hung servers cost one deadline, not three).
func TestDetectLocalSlowServerIsBounded(t *testing.T) {
	slow := func() string { return (&fakeOllama{delay: time.Minute}).server(t).URL }
	specs := []localServerSpec{specFor(kindOllama, slow()), specFor(kindLMStudio, slow()), specFor(kindLlamaCpp, slow())}
	start := time.Now()
	det := detectLocal(t.Context(), &http.Client{}, specs, mediumTier())
	elapsed := time.Since(start)
	if det.Offer != nil || len(det.Hints) != 0 {
		t.Errorf("detectLocal = %+v, want nothing from hung servers", det)
	}
	if elapsed > localListTimeout+time.Second {
		t.Errorf("detectLocal took %v, want <= %v", elapsed, localListTimeout+time.Second)
	}
}

// fakeGuided records whether the guided OpenRouter prompt was reached.
type fakeGuided struct{ calls int }

func (f *fakeGuided) Guide() bool { f.calls++; return false }

// readConfig loads the persisted user config.
func readConfig(t *testing.T, path string) Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cfg
}

// TestBootstrapRunLocal drives bootstrapRun.run — the code both REPL and
// `cortex turn` run — through the interactive yes/no paths and the
// non-interactive path against a fake Ollama.
func TestBootstrapRunLocal(t *testing.T) {
	tests := []struct {
		name         string
		interactive  bool
		input        string
		keyFound     bool
		createStatus int
		defaultUp    bool

		wantOutcome bootstrapOutcome
		wantModel   string // persisted code+study model; "" = no config
		wantCreated int
		wantGuided  int
		wantOut     []string
	}{
		{
			name: "interactive, Enter accepts", interactive: true, input: "\n",
			wantOutcome: outcomePersisted, wantModel: "cortex-qwen2.5-coder:14b-32k", wantCreated: 1,
			wantOut: []string{
				"Found Ollama with qwen2.5-coder:14b (32k context). Use it? [Y/n] ",
				"created Ollama model cortex-qwen2.5-coder:14b-32k (qwen2.5-coder:14b with num_ctx 32768)",
				"cortex: using Ollama with cortex-qwen2.5-coder:14b-32k (32k context) — saved to",
			},
		},
		{
			name: "interactive, y accepts", interactive: true, input: "y\n",
			wantOutcome: outcomePersisted, wantModel: "cortex-qwen2.5-coder:14b-32k", wantCreated: 1,
		},
		{
			name: "interactive, n falls through to the guided prompt", interactive: true, input: "n\n",
			wantOutcome: outcomeNone, wantGuided: 1,
			wantOut: []string{"Skipped Ollama."},
		},
		{
			name: "interactive, key wins and the local server is mentioned", interactive: true, input: "", keyFound: true,
			wantOutcome: outcomePersisted, wantModel: curatedTopPick().ID,
			wantOut: []string{"configured a openrouter backend (openrouter-env)", "also found Ollama with qwen2.5-coder:14b (32k context)"},
		},
		{
			name: "interactive, create fails => base model + OLLAMA_CONTEXT_LENGTH warning", interactive: true, input: "\n", createStatus: http.StatusInternalServerError,
			wantOutcome: outcomePersisted, wantModel: "qwen2.5-coder:14b", wantCreated: 1,
			wantOut: []string{"OLLAMA_CONTEXT_LENGTH=32768"},
		},
		{
			name:        "non-interactive uses the local model without prompting",
			wantOutcome: outcomePersisted, wantModel: "cortex-qwen2.5-coder:14b-32k", wantCreated: 1,
			wantOut: []string{"cortex: using Ollama with cortex-qwen2.5-coder:14b-32k (32k context) — saved to"},
		},
		{
			name: "non-interactive leaves a working default endpoint alone", defaultUp: true,
			wantOutcome: outcomeDefaultAnswers,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := isolateBootstrap(t)
			f := standardOllama()
			f.createStatus = tt.createStatus
			ollama := f.server(t)
			defaultRoot := ""
			if tt.defaultUp {
				d := httptest.NewServer(http.NotFoundHandler())
				t.Cleanup(d.Close)
				defaultRoot = d.URL
			}
			var key KeyProbe = noKeyProbe{}
			if tt.keyFound {
				key = &fakeKeyProbe{source: "openrouter-env", ok: true}
			}
			guided := &fakeGuided{}
			var out strings.Builder
			run := bootstrapRun{
				Interactive: tt.interactive,
				In:          strings.NewReader(tt.input),
				Out:         &out,
				Err:         &out,
				ConfigPath:  cfgPath,
				Specs:       []localServerSpec{specFor(kindOllama, ollama.URL)},
				DefaultRoot: defaultRoot,
				Tier:        mediumTier(),
				Key:         key,
				Guided:      guided,
				Client:      &http.Client{},
			}
			_, outcome := run.run()
			if outcome != tt.wantOutcome {
				t.Errorf("outcome = %v, want %v (output %q)", outcome, tt.wantOutcome, out.String())
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output = %q, want to contain %q", out.String(), w)
				}
			}
			if !tt.interactive && strings.Contains(out.String(), "[Y/n]") {
				t.Errorf("non-interactive run prompted: %q", out.String())
			}
			if guided.calls != tt.wantGuided {
				t.Errorf("guided calls = %d, want %d", guided.calls, tt.wantGuided)
			}
			if len(f.created) != tt.wantCreated {
				t.Errorf("/api/create calls = %d, want %d", len(f.created), tt.wantCreated)
			}
			if tt.wantCreated > 0 {
				c := f.created[0]
				params, _ := c["parameters"].(map[string]any)
				if c["from"] != "qwen2.5-coder:14b" || params["num_ctx"] != float64(32768) {
					t.Errorf("create request = %v, want from qwen2.5-coder:14b with num_ctx 32768", c)
				}
			}
			if tt.wantModel == "" {
				if pathExists(cfgPath) {
					t.Errorf("config written at %s, want none", cfgPath)
				}
				return
			}
			cfg := readConfig(t, cfgPath)
			for _, role := range []string{roleCode, roleStudy} {
				if got := cfg.Models[role].Model; got != tt.wantModel {
					t.Errorf("models.%s.model = %q, want %q", role, got, tt.wantModel)
				}
			}
			if tt.keyFound {
				// The OpenRouter seat must carry its endpoint: without
				// backend.endpoint the session sends to localhost:4000.
				if cfg.Backend.Type != "openrouter" || cfg.Backend.Endpoint != openRouterEndpoint {
					t.Errorf("backend = %+v, want type openrouter, endpoint %s", cfg.Backend, openRouterEndpoint)
				}
				return
			}
			if cfg.Backend.Type != "ollama" || cfg.Backend.Endpoint != ollama.URL+"/v1" {
				t.Errorf("backend = %+v, want type ollama, endpoint %s/v1", cfg.Backend, ollama.URL)
			}
			for _, role := range []string{roleCode, roleStudy} {
				if got := cfg.Models[role].Window; got != 32768 {
					t.Errorf("models.%s.window = %d, want 32768", role, got)
				}
			}
		})
	}
}

// TestBootstrapRunNoModelHintThenGuided pins case 5: a server with no usable
// model prints the pull command and the chain goes on to the guided prompt.
func TestBootstrapRunNoModelHintThenGuided(t *testing.T) {
	cfgPath := isolateBootstrap(t)
	ollama := (&fakeOllama{}).server(t)
	guided := &fakeGuided{}
	var out strings.Builder
	_, outcome := bootstrapRun{
		Interactive: true, In: strings.NewReader(""), Out: &out, Err: &out, ConfigPath: cfgPath,
		Specs: []localServerSpec{specFor(kindOllama, ollama.URL)}, Tier: mediumTier(),
		Key: noKeyProbe{}, Guided: guided, Client: &http.Client{},
	}.run()
	if outcome != outcomeNone || guided.calls != 1 {
		t.Errorf("outcome = %v, guided calls = %d; want outcomeNone after one guided prompt", outcome, guided.calls)
	}
	want := "cortex: Ollama is running, but it has no model Cortex can use (Cortex needs a chat model that can call tools). To add one, run: ollama pull qwen2.5-coder:14b"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// captureStd swaps os.Stdin (fed with input), os.Stdout and os.Stderr for
// pipes while fn runs, and returns what fn wrote to stdout and stderr.
func captureStd(t *testing.T, input string, fn func()) (stdout, stderr string) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := io.WriteString(inW, input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	inW.Close()
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	var wg sync.WaitGroup
	var ob, eb []byte
	wg.Add(2)
	go func() { defer wg.Done(); ob, _ = io.ReadAll(outR) }()
	go func() { defer wg.Done(); eb, _ = io.ReadAll(errR) }()
	fn()
	os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr
	outW.Close()
	errW.Close()
	wg.Wait()
	inR.Close()
	return string(ob), string(eb)
}

// TestRunGuidedBootstrapLocalOllama drives the real interactive entry point
// main() calls (runGuidedBootstrap: os.Stdin/os.Stdout) against a fake
// Ollama: Enter accepts and the config is saved; "n" falls through to the
// OpenRouter key prompt, whose empty answer skips setup.
func TestRunGuidedBootstrapLocalOllama(t *testing.T) {
	tests := []struct {
		name, input string
		wantSaved   bool
		wantOut     string
	}{
		{"enter accepts", "\n", true, "Found Ollama with qwen2.5-coder:14b (32k context). Use it? [Y/n] "},
		{"n declines, then the key prompt is skipped", "n\n\n", false, "OpenRouter API key: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := isolateBootstrap(t)
			f := standardOllama()
			orig := bootstrapLocalSpecs
			bootstrapLocalSpecs = []localServerSpec{specFor(kindOllama, f.server(t).URL)}
			defer func() { bootstrapLocalSpecs = orig }()

			stdout, _ := captureStd(t, tt.input, runGuidedBootstrap)
			if !strings.Contains(stdout, tt.wantOut) {
				t.Errorf("stdout = %q, want to contain %q", stdout, tt.wantOut)
			}
			if pathExists(cfgPath) != tt.wantSaved {
				t.Errorf("config saved = %v, want %v (stdout %q)", pathExists(cfgPath), tt.wantSaved, stdout)
			}
		})
	}
}

// TestHeadlessBootstrapWritesConfigOnStderr drives `cortex turn`'s entry
// point (maybeRunHeadlessBootstrap): with a fake Ollama it saves the config
// and says so on stderr, writing nothing to stdout; with nothing local it
// prints the hint and saves nothing.
func TestHeadlessBootstrapWritesConfigOnStderr(t *testing.T) {
	tests := []struct {
		name      string
		withLocal bool
		wantErr   string
		wantSaved bool
	}{
		{"ollama found", true, "cortex: using Ollama with cortex-qwen2.5-coder:14b-32k (32k context) — saved to", true},
		{"nothing local", false, "no local model server with a usable model was found", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := isolateBootstrap(t)
			orig := bootstrapLocalSpecs
			defer func() { bootstrapLocalSpecs = orig }()
			bootstrapLocalSpecs = []localServerSpec{specFor(kindOllama, closedURL(t))}
			if tt.withLocal {
				bootstrapLocalSpecs = []localServerSpec{specFor(kindOllama, standardOllama().server(t).URL)}
			}
			stdout, stderr := captureStd(t, "", maybeRunHeadlessBootstrap)
			if stdout != "" {
				t.Errorf("stdout = %q, want empty (stdout is the answer only)", stdout)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr = %q, want to contain %q", stderr, tt.wantErr)
			}
			if pathExists(cfgPath) != tt.wantSaved {
				t.Errorf("config saved = %v, want %v", pathExists(cfgPath), tt.wantSaved)
			}
			if tt.wantSaved {
				// A second run sees the config and does nothing.
				_, stderr2 := captureStd(t, "", maybeRunHeadlessBootstrap)
				if stderr2 != "" {
					t.Errorf("second run stderr = %q, want nothing (config exists)", stderr2)
				}
			}
		})
	}
}

// TestOllamaContextWarning pins the session-start check for a configured
// Ollama backend.
func TestOllamaContextWarning(t *testing.T) {
	f := &fakeOllama{shows: map[string]map[string]any{
		"big":   ollamaShowReply(131072, "num_ctx 32768", "completion", "tools"),
		"small": ollamaShowReply(131072, "num_ctx 8192", "completion", "tools"),
		"unset": ollamaShowReply(131072, "", "completion", "tools"),
	}}
	srv := f.server(t)
	tests := []struct {
		name, backend, model string
		window               int
		endpoint             string
		wantHas              string // "" = no warning
	}{
		{"num_ctx covers the window", "ollama", "big", 32768, srv.URL + "/v1", ""},
		{"num_ctx below the window", "ollama", "small", 32768, srv.URL + "/v1", "num_ctx 8192, below models.code.window (32768)"},
		{"num_ctx unset", "Ollama", "unset", 32768, srv.URL + "/v1/", "OLLAMA_CONTEXT_LENGTH=32768"},
		{"not an ollama backend", "litellm", "small", 32768, srv.URL + "/v1", ""},
		{"no window pinned", "ollama", "small", 0, srv.URL + "/v1", ""},
		{"server unreachable", "ollama", "small", 32768, closedURL(t) + "/v1", ""},
		{"model unknown to the server", "ollama", "missing", 32768, srv.URL + "/v1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ollamaContextWarning(t.Context(), &http.Client{}, tt.backend, ModelSpec{Model: tt.model, Window: tt.window, Endpoint: tt.endpoint})
			if tt.wantHas == "" {
				if got != "" {
					t.Errorf("warning = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantHas) {
				t.Errorf("warning = %q, want to contain %q", got, tt.wantHas)
			}
		})
	}
}

// TestSmallModelHint pins which picks get the "small model" hint: under
// smallModelParamsB (7B) yes; 7B and larger, or an unknown size, no.
func TestSmallModelHint(t *testing.T) {
	tests := []struct {
		name    string
		paramsB float64
		want    bool
	}{
		{"unknown size", 0, false},
		{"1.5B", 1.5, true},
		{"3.2B", 3.2, true},
		{"7B", 7, false},
		{"14.8B", 14.8, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, interactive := range []bool{true, false} {
				got := smallModelHint(localOffer{ParamsB: tt.paramsB}, interactive) != ""
				if got != tt.want {
					t.Errorf("smallModelHint(%v, interactive=%v) shown = %v, want %v", tt.paramsB, interactive, got, tt.want)
				}
			}
		})
	}
}

// smallOnlyOllama is a fake Ollama whose only usable chat model is 3.2B.
func smallOnlyOllama() *fakeOllama {
	return &fakeOllama{
		tags: []map[string]any{ollamaTag("llama3.2:3b", "3.2B")},
		shows: map[string]map[string]any{
			"llama3.2:3b": ollamaShowReply(131072, "", "completion", "tools"),
		},
	}
}

// TestSmallModelHintEntryPoints drives the real first-run entry points: the
// interactive prompt (runGuidedBootstrap) shows the hint before "Use it?"
// only for a small pick, and the headless path (maybeRunHeadlessBootstrap)
// notes it on stderr.
func TestSmallModelHintEntryPoints(t *testing.T) {
	tests := []struct {
		name     string
		fake     func() *fakeOllama
		headless bool
		want     string
		wantHint bool
	}{
		{"interactive small", smallOnlyOllama, false, "Press n to use a free hosted model instead.\nUse it? [Y/n] ", true},
		{"interactive large", standardOllama, false, "Found Ollama with qwen2.5-coder:14b (32k context). Use it? [Y/n] ", false},
		{"headless small", smallOnlyOllama, true, "cortex: note: this is a small model", true},
		{"headless large", standardOllama, true, "cortex: using Ollama with", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateBootstrap(t)
			orig := bootstrapLocalSpecs
			defer func() { bootstrapLocalSpecs = orig }()
			bootstrapLocalSpecs = []localServerSpec{specFor(kindOllama, tt.fake().server(t).URL)}

			var out string
			if tt.headless {
				_, out = captureStd(t, "", maybeRunHeadlessBootstrap)
			} else {
				out, _ = captureStd(t, "\n", runGuidedBootstrap)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output = %q, want to contain %q", out, tt.want)
			}
			if got := strings.Contains(out, "small models often fail") || strings.Contains(out, "Small models often fail"); got != tt.wantHint {
				t.Errorf("hint shown = %v, want %v (output %q)", got, tt.wantHint, out)
			}
		})
	}
}
