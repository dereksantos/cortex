// bootstrap_local.go — the local-server stage of the first-run backend chain
// (bootstrap.go stage 3): find a model server already running on this
// machine, pick a model it has installed, and seat it with a context window
// the server will actually honor.
//
// Three servers are probed, concurrently, on their default ports:
//
//   - Ollama     http://localhost:11434  (/api/tags, /api/show)
//   - LM Studio  http://localhost:1234   (/api/v0/models, else /v1/models)
//   - llama.cpp  http://localhost:8080   (/v1/models, /props)
//
// The first server (in that order) with a usable model wins. A server that
// is up but has no usable model contributes a one-line hint naming the
// command that fixes it; nothing is ever downloaded.
//
// The Ollama context trap: Ollama's OpenAI-compatible /v1 endpoint — the
// only request path cmd/cortex has — has no per-request way to set the
// context size (it maps temperature/top_p/stop/seed/max_tokens into the
// runner's options, never num_ctx). A model without a num_ctx parameter is
// served at the server default (OLLAMA_CONTEXT_LENGTH; 4096 on Ollama 0.9),
// and a longer prompt is cut off by the server without an error. So when
// the picked model does not already carry a num_ctx at least as large as
// the window, the stage creates a derived model (POST /api/create with
// "from" + "parameters": {"num_ctx": N}) — metadata only, no download — and
// seats that name instead.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/pkg/llm"
)

// localServerKind names a supported local server. The value is also what
// the stage persists as backend.type ("ollama" turns on the session-start
// num_ctx check; the other two are treated as generic OpenAI-compatible
// backends, docs/configuration.md "The backend block").
type localServerKind string

const (
	kindOllama   localServerKind = "ollama"
	kindLMStudio localServerKind = "lmstudio"
	kindLlamaCpp localServerKind = "llamacpp"
)

// localServerSpec is one server to probe: its kind, display name, and root
// URL (no /v1 suffix).
type localServerSpec struct {
	Kind localServerKind
	Name string
	Root string
}

// defaultLocalServerSpecs are the default ports, in precedence order.
var defaultLocalServerSpecs = []localServerSpec{
	{Kind: kindOllama, Name: "Ollama", Root: "http://localhost:11434"},
	{Kind: kindLMStudio, Name: "LM Studio", Root: "http://localhost:1234"},
	{Kind: kindLlamaCpp, Name: "llama.cpp", Root: "http://localhost:8080"},
}

const (
	// localListTimeout bounds the concurrent listing phase as a whole, so a
	// machine with nothing running (or a port that swallows packets) adds at
	// most this much to startup.
	localListTimeout = defaultOllamaProbeTimeout
	// localShowTimeout bounds each Ollama /api/show call; they only run once
	// a server has answered its listing.
	localShowTimeout = 2 * time.Second
	// localCreateTimeout bounds the Ollama /api/create call that derives a
	// num_ctx variant. It copies no weights, so it normally takes well under
	// a second.
	localCreateTimeout = 60 * time.Second
	// maxOllamaShows caps how many ranked candidates are inspected with
	// /api/show before giving up on finding a tool-capable model.
	maxOllamaShows = 8
	// minLocalWindow is the window below which the stage warns: Cortex's
	// system prompt plus tool declarations take several thousand tokens, so
	// a smaller window leaves almost nothing for the conversation.
	minLocalWindow = 16384
)

// localModel is one model a local server reports, normalized across the
// three servers.
type localModel struct {
	ID string
	// ParamsB is the parameter count in billions; 0 when unknown.
	ParamsB float64
	// ContextLength is the model's maximum context as the server reports it
	// (Ollama model_info *.context_length, LM Studio max_context_length,
	// llama.cpp n_ctx_train); 0 when unknown.
	ContextLength int
	// ServedContext is the context the server will actually run the model
	// with, when the server says so: Ollama's num_ctx parameter, LM Studio's
	// loaded_context_length, llama.cpp's /props n_ctx. 0 when unknown.
	ServedContext int
	// Loaded is true when LM Studio reports the model as loaded.
	Loaded bool
	// Embedding marks an embedding-only model (never a chat candidate).
	Embedding bool
	// NoTools is true when the server states the model cannot call tools
	// (Ollama capabilities without "tools", or without "completion").
	NoTools bool
}

// localServer is a server that answered its listing request.
type localServer struct {
	Spec   localServerSpec
	Models []localModel
}

// localOffer is the stage's proposal: which server and model to seat, with
// what window.
type localOffer struct {
	Spec localServerSpec
	// BaseModel is the model id as the server lists it (what the user knows).
	BaseModel string
	// Model is the id to persist. It differs from BaseModel only for an
	// Ollama derived num_ctx variant (DeriveNumCtx).
	Model  string
	Window int
	// DeriveNumCtx asks the stage to create Model from BaseModel on the
	// Ollama server with num_ctx = Window before seating it.
	DeriveNumCtx bool
	// Note is a warning to print with the offer (context limits the stage
	// could not fix itself), or empty.
	Note string
}

// Source is the ResolvedBackend.Source for this offer, e.g. "ollama-local".
func (o localOffer) Source() string { return string(o.Spec.Kind) + "-local" }

// Endpoint is the OpenAI-compatible base URL persisted as backend.endpoint.
func (o localOffer) Endpoint() string { return strings.TrimRight(o.Spec.Root, "/") + "/v1" }

// localDetection is the outcome of probing every local server.
type localDetection struct {
	// Offer is the first usable server+model, or nil.
	Offer *localOffer
	// Hints holds one fix-it line per server that answered but had no
	// usable model, in precedence order.
	Hints []string
}

// detectLocal probes specs concurrently, then walks the servers that
// answered in spec order and returns the first usable offer. tier is the
// RAM-tier suggestion (suggestModels) that sets the parameter-count cap and
// the window ceiling.
func detectLocal(ctx context.Context, client *http.Client, specs []localServerSpec, tier ModelSuggestion) localDetection {
	servers := listLocalServers(ctx, client, specs)
	var det localDetection
	for _, s := range servers {
		offer, ok := offerFor(ctx, client, s, tier)
		if ok {
			det.Offer = &offer
			det.Hints = nil
			return det
		}
		det.Hints = append(det.Hints, noModelHint(s.Spec, tier))
	}
	return det
}

// listLocalServers runs every spec's listing concurrently under one
// localListTimeout deadline and returns the servers that answered, in spec
// order.
func listLocalServers(ctx context.Context, client *http.Client, specs []localServerSpec) []localServer {
	ctx, cancel := context.WithTimeout(ctx, localListTimeout)
	defer cancel()
	results := make([]*localServer, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Add(1)
		go func(i int, spec localServerSpec) {
			defer wg.Done()
			var (
				models []localModel
				err    error
			)
			switch spec.Kind {
			case kindOllama:
				models, err = listOllamaModels(ctx, client, spec.Root)
			case kindLMStudio:
				models, err = listLMStudioModels(ctx, client, spec.Root)
			case kindLlamaCpp:
				models, err = listLlamaCppModels(ctx, client, spec.Root)
			default:
				return
			}
			if err != nil {
				return
			}
			results[i] = &localServer{Spec: spec, Models: models}
		}(i, spec)
	}
	wg.Wait()
	var out []localServer
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// offerFor picks a model on one server and sizes its window. ok is false
// when the server has no usable model.
func offerFor(ctx context.Context, client *http.Client, s localServer, tier ModelSuggestion) (localOffer, bool) {
	ranked := rankLocalModels(s.Models, tierParamCap(tier))
	if len(ranked) == 0 {
		return localOffer{}, false
	}
	pick := ranked[0]
	if s.Spec.Kind == kindOllama {
		found := false
		for i, m := range ranked {
			if i >= maxOllamaShows {
				break
			}
			if err := ollamaShowInto(ctx, client, s.Spec.Root, &m); err != nil {
				continue
			}
			if m.NoTools || m.Embedding {
				continue
			}
			pick, found = m, true
			break
		}
		if !found {
			return localOffer{}, false
		}
	}

	desired := tier.Code.Window
	if pick.ContextLength > 0 && pick.ContextLength < desired {
		desired = pick.ContextLength
	}
	offer := localOffer{Spec: s.Spec, BaseModel: pick.ID, Model: pick.ID, Window: desired}
	switch s.Spec.Kind {
	case kindOllama:
		if pick.ServedContext < desired {
			offer.DeriveNumCtx = true
			offer.Model = derivedOllamaName(pick.ID, desired)
		}
	case kindLMStudio:
		switch {
		case pick.Loaded && pick.ServedContext > 0:
			if pick.ServedContext < desired {
				offer.Window = pick.ServedContext
				offer.Note = fmt.Sprintf("LM Studio loaded %s with a %d-token context. For longer sessions, reload it with more: lms load %s --context-length %d",
					pick.ID, pick.ServedContext, pick.ID, desired)
			}
		default:
			offer.Note = fmt.Sprintf("LM Studio loads %s with its own context-length setting, which can be smaller than %d tokens. Load it first so the window is real: lms load %s --context-length %d",
				pick.ID, desired, pick.ID, desired)
		}
	case kindLlamaCpp:
		if pick.ServedContext > 0 && pick.ServedContext < desired {
			offer.Window = pick.ServedContext
			offer.Note = fmt.Sprintf("llama-server runs with a %d-token context. For longer sessions, restart it with -c %d (and --jinja for tool calls).",
				pick.ServedContext, desired)
		}
	}
	if offer.Window < minLocalWindow {
		small := fmt.Sprintf("A %d-token window is small for Cortex's prompt and tools; expect early compaction or failed turns.", offer.Window)
		if offer.Note == "" {
			offer.Note = small
		} else {
			offer.Note += " " + small
		}
	}
	return offer, true
}

// coderTokens mark a coding-tuned model; such a model's parameter count is
// counted double when ranking. familyTokens mark a general instruct family
// that is known to call tools; counted at 1.5x. Everything else counts at
// face value. Matched as lowercase substrings of the model id.
var (
	coderTokens  = []string{"coder", "devstral", "codestral"}
	familyTokens = []string{"qwen", "gpt-oss", "llama3", "llama-3", "mistral", "granite", "glm"}
)

// modelWeight is the ranking score: the parameter count, scaled by the name
// preference above. Unknown size scores 0 (ranked last among fits).
func modelWeight(m localModel) float64 {
	id := strings.ToLower(m.ID)
	for _, t := range coderTokens {
		if strings.Contains(id, t) {
			return m.ParamsB * 2
		}
	}
	for _, t := range familyTokens {
		if strings.Contains(id, t) {
			return m.ParamsB * 1.5
		}
	}
	return m.ParamsB
}

// rankLocalModels drops embedding and tool-less models and orders the rest:
// loaded models first (LM Studio is already serving them), then models
// whose size fits under capB (with paramCapSlack, or unknown) by modelWeight descending,
// then models too large for this machine by size ascending — so a server
// holding only oversized models still yields its smallest one. Ties break
// on id for a stable order.
func rankLocalModels(models []localModel, capB float64) []localModel {
	var out []localModel
	for _, m := range models {
		if m.Embedding || m.NoTools {
			continue
		}
		out = append(out, m)
	}
	fits := func(m localModel) bool { return m.ParamsB == 0 || capB <= 0 || m.ParamsB <= capB*paramCapSlack }
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Loaded != b.Loaded {
			return a.Loaded
		}
		fa, fb := fits(a), fits(b)
		if fa != fb {
			return fa
		}
		if fa {
			wa, wb := modelWeight(a), modelWeight(b)
			if wa != wb {
				return wa > wb
			}
		} else if a.ParamsB != b.ParamsB {
			return a.ParamsB < b.ParamsB
		}
		return a.ID < b.ID
	})
	return out
}

// paramCapSlack lets a model through whose reported size runs a little over
// its nominal one: the tier cap comes from a name ("qwen2.5-coder:14b" ->
// 14), while servers report exact counts (Ollama lists that model as
// 14.8B).
const paramCapSlack = 1.15

// tierParamCap is the parameter-count ceiling (billions) for this machine:
// the size of the RAM tier's suggested code model (suggestModels).
func tierParamCap(tier ModelSuggestion) float64 {
	return parseParamsB(tier.Code.Model)
}

// paramsInName matches a "<number>b" size token bounded by non-alphanumerics,
// e.g. ":14b", "-1.5b", "-4B-" (case-folded before matching).
var paramsInName = regexp.MustCompile(`(?:^|[^a-z0-9.])(\d+(?:\.\d+)?)b(?:$|[^a-z0-9])`)

// parseParamsB reads a parameter count in billions from a size string
// ("4.0B", "137M") or a model id ("qwen2.5-coder:14b"). 0 when none.
func parseParamsB(s string) float64 {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(strings.TrimSuffix(l, "b"), 64); err == nil && strings.HasSuffix(l, "b") {
		return n
	}
	if n, err := strconv.ParseFloat(strings.TrimSuffix(l, "m"), 64); err == nil && strings.HasSuffix(l, "m") {
		return n / 1000
	}
	if m := paramsInName.FindStringSubmatch(l); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		return n
	}
	return 0
}

// derivedOllamaName is the name of the num_ctx variant the stage creates:
// "cortex-" + the model's last path segment, its tag (default "latest"),
// and the window in k — e.g. "qwen2.5-coder:14b" at 32768 becomes
// "cortex-qwen2.5-coder:14b-32k". Lowercased, since Ollama names are.
func derivedOllamaName(base string, window int) string {
	name, tag := base, "latest"
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		name, tag = base[:i], base[i+1:]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(fmt.Sprintf("cortex-%s:%s-%dk", name, tag, window/1024))
}

// noModelHint is the fix-it line for a server that answered but has no
// usable model, sized to this machine's RAM tier.
func noModelHint(spec localServerSpec, tier ModelSuggestion) string {
	size := strings.ToUpper(strconv.FormatFloat(tierParamCap(tier), 'f', -1, 64)) + "B"
	switch spec.Kind {
	case kindOllama:
		return fmt.Sprintf("Ollama is running, but it has no model Cortex can use (Cortex needs a chat model that can call tools). To add one, run: ollama pull %s", tier.Code.Model)
	case kindLMStudio:
		return fmt.Sprintf("LM Studio is running, but it has no model Cortex can use (Cortex needs a chat model that can call tools). Download one in LM Studio, for example Qwen2.5 Coder %s Instruct.", size)
	default:
		return fmt.Sprintf("%s is running at %s, but it serves no model Cortex can use. Restart it with a coding model, for example: llama-server --jinja -c %d -hf Qwen/Qwen2.5-Coder-%s-Instruct-GGUF",
			spec.Name, spec.Root, tier.Code.Window, size)
	}
}

// getJSON GETs root+path and decodes a 200 JSON body into v.
func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// postJSON POSTs body as JSON to url and decodes a 200 JSON reply into v
// (v may be nil).
func postJSON(ctx context.Context, client *http.Client, url string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// listOllamaModels reads Ollama's /api/tags. Cloud-hosted entries (a
// remote_host, or a "cloud" tag) are skipped: they do not run locally.
func listOllamaModels(ctx context.Context, client *http.Client, root string) ([]localModel, error) {
	var tags struct {
		Models []struct {
			Name       string `json:"name"`
			RemoteHost string `json:"remote_host"`
			Details    struct {
				Family        string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := getJSON(ctx, client, root+"/api/tags", &tags); err != nil {
		return nil, err
	}
	out := make([]localModel, 0, len(tags.Models))
	for _, t := range tags.Models {
		lower := strings.ToLower(t.Name)
		if t.RemoteHost != "" || strings.HasSuffix(lower, ":cloud") || strings.HasSuffix(lower, "-cloud") {
			continue
		}
		params := parseParamsB(t.Details.ParameterSize)
		if params == 0 {
			params = parseParamsB(t.Name)
		}
		out = append(out, localModel{
			ID:        t.Name,
			ParamsB:   params,
			Embedding: strings.Contains(lower, "embed") || strings.Contains(strings.ToLower(t.Details.Family), "bert"),
		})
	}
	return out, nil
}

// ollamaShowInfo is what /api/show tells the stage about one model.
type ollamaShowInfo struct {
	// ContextLength is model_info's "<arch>.context_length"; 0 if absent.
	ContextLength int
	// NumCtx is the model's num_ctx parameter; 0 if the model sets none
	// (the server default then applies).
	NumCtx int
	// Capabilities is nil when the server predates the field.
	Capabilities []string
}

// ollamaShow calls POST /api/show for model.
func ollamaShow(ctx context.Context, client *http.Client, root, model string) (ollamaShowInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, localShowTimeout)
	defer cancel()
	var show struct {
		ModelInfo    map[string]any `json:"model_info"`
		Parameters   string         `json:"parameters"`
		Capabilities []string       `json:"capabilities"`
	}
	if err := postJSON(ctx, client, root+"/api/show", map[string]string{"model": model}, &show); err != nil {
		return ollamaShowInfo{}, err
	}
	info := ollamaShowInfo{Capabilities: show.Capabilities}
	for k, v := range show.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				info.ContextLength = int(f)
			}
		}
	}
	for _, line := range strings.Split(show.Parameters, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "num_ctx" {
			if n, err := strconv.Atoi(f[1]); err == nil {
				info.NumCtx = n
			}
		}
	}
	return info, nil
}

// ollamaShowInto fills m's context fields and tool verdict from /api/show.
func ollamaShowInto(ctx context.Context, client *http.Client, root string, m *localModel) error {
	info, err := ollamaShow(ctx, client, root, m.ID)
	if err != nil {
		return err
	}
	m.ContextLength = info.ContextLength
	m.ServedContext = info.NumCtx
	if info.Capabilities != nil {
		has := func(c string) bool {
			for _, x := range info.Capabilities {
				if x == c {
					return true
				}
			}
			return false
		}
		m.NoTools = !has("tools") || !has("completion")
		m.Embedding = m.Embedding || (has("embedding") && !has("completion"))
	}
	return nil
}

// createOllamaVariant creates model on the Ollama server at root from base
// with num_ctx set — POST /api/create {"model","from","parameters"}. It
// reuses base's weights; nothing is downloaded.
func createOllamaVariant(ctx context.Context, client *http.Client, root, model, base string, numCtx int) error {
	ctx, cancel := context.WithTimeout(ctx, localCreateTimeout)
	defer cancel()
	body := map[string]any{
		"model":      model,
		"from":       base,
		"parameters": map[string]any{"num_ctx": numCtx},
		"stream":     false,
	}
	var reply struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := postJSON(ctx, client, root+"/api/create", body, &reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return fmt.Errorf("%s", reply.Error)
	}
	return nil
}

// listLMStudioModels reads LM Studio's REST API (/api/v0/models: type,
// load state, context lengths), falling back to the plain OpenAI listing
// (/v1/models: ids only) on servers without it.
func listLMStudioModels(ctx context.Context, client *http.Client, root string) ([]localModel, error) {
	var rich struct {
		Data []struct {
			ID                  string `json:"id"`
			Type                string `json:"type"`
			State               string `json:"state"`
			MaxContextLength    int    `json:"max_context_length"`
			LoadedContextLength int    `json:"loaded_context_length"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, root+"/api/v0/models", &rich); err == nil && rich.Data != nil {
		out := make([]localModel, 0, len(rich.Data))
		for _, d := range rich.Data {
			out = append(out, localModel{
				ID:            d.ID,
				ParamsB:       parseParamsB(d.ID),
				ContextLength: d.MaxContextLength,
				ServedContext: d.LoadedContextLength,
				Loaded:        d.State == "loaded",
				Embedding:     d.Type == "embeddings" || strings.Contains(strings.ToLower(d.ID), "embed"),
			})
		}
		return out, nil
	}
	return listOpenAIModels(ctx, client, root)
}

// listOpenAIModels reads a plain OpenAI-style /v1/models listing.
func listOpenAIModels(ctx context.Context, client *http.Client, root string) ([]localModel, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, root+"/v1/models", &list); err != nil {
		return nil, err
	}
	if list.Data == nil {
		return nil, fmt.Errorf("%s/v1/models: no data list", root)
	}
	out := make([]localModel, 0, len(list.Data))
	for _, d := range list.Data {
		out = append(out, localModel{ID: d.ID, ParamsB: parseParamsB(d.ID), Embedding: strings.Contains(strings.ToLower(d.ID), "embed")})
	}
	return out, nil
}

// listLlamaCppModels reads llama-server's /v1/models (with its "meta"
// extension: n_params, n_ctx_train) and /props (the context the server was
// started with, default_generation_settings.n_ctx).
func listLlamaCppModels(ctx context.Context, client *http.Client, root string) ([]localModel, error) {
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				NParams   float64 `json:"n_params"`
				NCtxTrain int     `json:"n_ctx_train"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, root+"/v1/models", &list); err != nil {
		return nil, err
	}
	if list.Data == nil {
		return nil, fmt.Errorf("%s/v1/models: no data list", root)
	}
	var props struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	_ = getJSON(ctx, client, root+"/props", &props) // optional: older servers lack it
	out := make([]localModel, 0, len(list.Data))
	for _, d := range list.Data {
		params := d.Meta.NParams / 1e9
		if params == 0 {
			params = parseParamsB(d.ID)
		}
		out = append(out, localModel{
			ID:            d.ID,
			ParamsB:       params,
			ContextLength: d.Meta.NCtxTrain,
			ServedContext: props.DefaultGenerationSettings.NCtx,
			Embedding:     strings.Contains(strings.ToLower(d.ID), "embed"),
		})
	}
	return out, nil
}

// endpointAnswers reports whether anything answers HTTP at root+"/v1/models"
// (any status). The non-interactive bootstrap uses it to leave a working
// zero-config LiteLLM on the default endpoint alone.
func endpointAnswers(ctx context.Context, client *http.Client, root string) bool {
	if root == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, localListTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, llm.NormalizeBaseURL(root)+"/models", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// windowLabel renders a token count as "32k" (or the raw number when it is
// not a whole multiple of 1024).
func windowLabel(n int) string {
	if n > 0 && n%1024 == 0 {
		return fmt.Sprintf("%dk", n/1024)
	}
	return strconv.Itoa(n)
}

// localServerStage is the production LocalProbe (bootstrap.go stage 3). It
// acts on a localDetection made before the chain ran: prints the no-model
// hints when there is no offer; otherwise asks (Interactive) or announces,
// creates the Ollama num_ctx variant when needed, and seats the offer.
type localServerStage struct {
	Detection   localDetection
	Interactive bool
	In          io.Reader
	Out         io.Writer
	Client      *http.Client
}

func (s localServerStage) Probe() (ResolvedBackend, bool) {
	o := s.Detection.Offer
	if o == nil {
		for _, h := range s.Detection.Hints {
			fmt.Fprintln(s.Out, "cortex: "+h)
		}
		return ResolvedBackend{}, false
	}
	if s.Interactive {
		fmt.Fprintf(s.Out, "Found %s with %s (%s context). Use it? [Y/n] ", o.Spec.Name, o.BaseModel, windowLabel(o.Window))
		answer := strings.ToLower(strings.TrimSpace(readLineUnbuffered(s.In)))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Fprintf(s.Out, "Skipped %s.\n", o.Spec.Name)
			return ResolvedBackend{}, false
		}
	}
	if o.Note != "" {
		fmt.Fprintln(s.Out, "cortex: note: "+o.Note)
	}
	model := o.Model
	if o.DeriveNumCtx {
		if err := createOllamaVariant(context.Background(), s.Client, o.Spec.Root, o.Model, o.BaseModel, o.Window); err != nil {
			model = o.BaseModel
			fmt.Fprintf(s.Out, "cortex: warning: could not create %s (%v). Ollama may run %s with a context smaller than %d tokens and cut off long prompts without an error. To fix it, restart Ollama with OLLAMA_CONTEXT_LENGTH=%d.\n",
				o.Model, err, o.BaseModel, o.Window, o.Window)
		} else {
			fmt.Fprintf(s.Out, "cortex: created Ollama model %s (%s with num_ctx %d); Ollama's OpenAI API cannot set the context per request, so the model carries it.\n",
				o.Model, o.BaseModel, o.Window)
		}
	}
	return ResolvedBackend{
		Source:   o.Source(),
		Backend:  llm.Backend(o.Spec.Kind),
		Endpoint: o.Endpoint(),
		Model:    model,
		Window:   o.Window,
	}, true
}

// readLineUnbuffered reads up to and including '\n' one byte at a time, so
// a later reader on the same stream (the guided OpenRouter prompt) still
// sees everything after this line.
func readLineUnbuffered(r io.Reader) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

// ollamaContextWarning is the session-start check for a configured Ollama
// backend (backend.type "ollama"): it asks the server (/api/show) what
// num_ctx the code model carries and returns a warning when that is below
// the configured window — or when the model sets none, so the server
// default applies. "" when the backend is not Ollama, nothing is pinned,
// the server can't be asked, or the model's num_ctx covers the window.
// The check exists because Ollama's /v1 endpoint cannot set num_ctx per
// request (see the file comment).
func ollamaContextWarning(ctx context.Context, client *http.Client, backendType string, spec ModelSpec) string {
	if !strings.EqualFold(backendType, string(kindOllama)) || spec.Model == "" || spec.Window <= 0 {
		return ""
	}
	root := strings.TrimSuffix(strings.TrimRight(spec.Endpoint, "/"), "/v1")
	info, err := ollamaShow(ctx, client, root, spec.Model)
	if err != nil {
		return ""
	}
	switch {
	case info.NumCtx >= spec.Window:
		return ""
	case info.NumCtx > 0:
		return fmt.Sprintf("warning: Ollama serves %s with num_ctx %d, below models.code.window (%d); Ollama cuts longer prompts off without an error. "+
			"Give the model a larger num_ctx or lower the window — see \"Local model servers\" in docs/configuration.md.",
			spec.Model, info.NumCtx, spec.Window)
	default:
		return fmt.Sprintf("warning: %s sets no num_ctx, so Ollama serves it with the server default (OLLAMA_CONTEXT_LENGTH; 4096 on Ollama 0.9), "+
			"which can be below models.code.window (%d); Ollama cuts longer prompts off without an error. "+
			"Start Ollama with OLLAMA_CONTEXT_LENGTH=%d or give the model a num_ctx — see \"Local model servers\" in docs/configuration.md.",
			spec.Model, spec.Window, spec.Window)
	}
}
