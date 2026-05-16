package niah

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/pkg/events"
)

// Default values for NIAH. The needle is intentionally distinctive so
// the probe (needle minus the secret code) lands precisely on the
// chunk that contains it; the lorem corpus has no overlap with these
// terms ("secret", "recipe", "code").
const (
	DefaultNeedle       = "The secret recipe code is 4F-9X-2B."
	DefaultSeed   int64 = 1
	cortexVersion       = "0.1.0"

	// chunkSize is the per-capture window in chars. Sized to stay under
	// the 500-char truncation in Reflex's eventToResult so the needle
	// survives the truncation step. chunkStride is the advance between
	// chunks; (size - stride) bytes overlap, which guarantees a needle
	// of length ≤ stride is fully contained in at least one chunk.
	chunkSize   = 400
	chunkStride = 320
)

// DefaultDepths are the canonical NIAH depths: start, middle, end.
// Pinned in code so a casual flag drop (`cortex eval --benchmark niah`)
// still exercises the three positions that catch most depth-bias bugs.
var DefaultDepths = []float64{0.0, 0.5, 1.0}

// Payload is the per-instance data the runner needs at Run time.
// Stashed on Instance.Payload by Load; pulled back out by Run.
type Payload struct {
	LengthLabel string // e.g. "8k" — preserved verbatim for ScenarioID
	LengthTok   int    // resolved token count, e.g. 8192
	Depth       float64
	Needle      string
	Seed        int64
	FillerMode  FillerMode // empty → adversarial (the meaningful default)
}

func init() { benchmarks.Register("niah", func() benchmarks.Benchmark { return &runner{} }) }

type runner struct{}

func (runner) Name() string { return "niah" }

// ApplyArgs implements benchmarks.ArgsApplier so the CLI dispatcher
// doesn't need a switch-on-name to wire NIAH's flags. Repeated
// --length / --depth accumulate into comma-separated Filter values;
// --needle / --seed / --filler are singletons. --model is rejected
// here so an operator who forgets that NIAH measures retrieval (not
// LLMs) gets a clean error instead of a silently-honored flag.
func (runner) ApplyArgs(args []string, opts *benchmarks.LoadOpts) error {
	if opts.Filter == nil {
		opts.Filter = map[string]string{}
	}
	var lengths, depths []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--length":
			if i+1 >= len(args) {
				return errors.New("--length requires a value")
			}
			lengths = append(lengths, args[i+1])
			i++
		case "--depth":
			if i+1 >= len(args) {
				return errors.New("--depth requires a value")
			}
			depths = append(depths, args[i+1])
			i++
		case "--needle":
			if i+1 >= len(args) {
				return errors.New("--needle requires a value")
			}
			opts.Filter["needle"] = args[i+1]
			i++
		case "--seed":
			if i+1 >= len(args) {
				return errors.New("--seed requires a value")
			}
			opts.Filter["seed"] = args[i+1]
			i++
		case "--filler":
			if i+1 >= len(args) {
				return errors.New("--filler requires a value (adversarial|lorem)")
			}
			opts.Filter["filler"] = args[i+1]
			i++
		case "-m", "--model":
			return errors.New("--model is not valid with --benchmark niah (NIAH measures retrieval, not LLMs)")
		}
	}
	if len(lengths) > 0 {
		opts.Filter["lengths"] = joinExpandingCSV(lengths)
	}
	if len(depths) > 0 {
		opts.Filter["depths"] = joinExpandingCSV(depths)
	}
	return nil
}

// joinExpandingCSV joins values with commas, flattening any
// already-comma-separated values inside. Empty/whitespace fragments
// are dropped so "8k, ,16k" round-trips to "8k,16k".
func joinExpandingCSV(vals []string) string {
	var out []string
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return strings.Join(out, ",")
}

// Load expands the (length × depth) cross-product from opts.Filter into
// one Instance per combination. Filter keys "lengths" and "depths" carry
// comma-separated lists (the CLI joins repeated flags this way); "needle"
// and "seed" carry single values. opts.Limit trims the cross-product
// AFTER expansion, per the brief.
func (runner) Load(_ context.Context, opts benchmarks.LoadOpts) ([]benchmarks.Instance, error) {
	needle := DefaultNeedle
	if v := opts.Filter["needle"]; v != "" {
		needle = v
	}
	seed := DefaultSeed
	if v := opts.Filter["seed"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse seed %q: %w", v, err)
		}
		seed = n
	}
	var fillerMode FillerMode
	if v := opts.Filter["filler"]; v != "" {
		switch FillerMode(v) {
		case FillerLorem, FillerAdversarial:
			fillerMode = FillerMode(v)
		default:
			return nil, fmt.Errorf("unknown filler mode %q (want %q or %q)", v, FillerAdversarial, FillerLorem)
		}
	}

	type lenSpec struct {
		label string
		tok   int
	}
	var lengths []lenSpec
	if v := opts.Filter["lengths"]; v != "" {
		for _, part := range splitCSV(v) {
			n, err := ParseLengthLabel(part)
			if err != nil {
				return nil, err
			}
			lengths = append(lengths, lenSpec{label: part, tok: n})
		}
	}
	if len(lengths) == 0 {
		lengths = []lenSpec{{label: "8k", tok: 8 * 1024}}
	}

	depths := DefaultDepths
	if v := opts.Filter["depths"]; v != "" {
		depths = nil
		for _, part := range splitCSV(v) {
			d, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return nil, fmt.Errorf("parse depth %q: %w", part, err)
			}
			depths = append(depths, d)
		}
	}

	insts := make([]benchmarks.Instance, 0, len(lengths)*len(depths))
	for _, L := range lengths {
		for _, d := range depths {
			insts = append(insts, benchmarks.Instance{
				ID: fmt.Sprintf("niah/%s-%g", L.label, d),
				Payload: Payload{
					LengthLabel: L.label,
					LengthTok:   L.tok,
					Depth:       d,
					Needle:      needle,
					Seed:        seed,
					FillerMode:  fillerMode,
				},
			})
		}
	}

	if opts.Limit > 0 && opts.Limit < len(insts) {
		insts = insts[:opts.Limit]
	}
	return insts, nil
}

// Run executes one NIAH instance through the cortex CLI as a black box
// (per docs/prompts/eval-principles.md). The flow:
//
//	1. Generate the haystack (in-memory, no Cortex involvement)
//	2. Chunk into overlapping windows -> []*events.Event
//	3. exec `cortex capture --bulk --workdir env.Workdir` over NDJSON stdin
//	4. exec `cortex ingest --workdir env.Workdir`
//	5. exec `cortex search --workdir env.Workdir --json` and parse output
//	6. Score on substring match (offline, no Cortex involvement)
//
// All Cortex interaction is mediated by the CLI; no internal package
// imports. This means the benchmark genuinely tests what users run.
func (runner) Run(ctx context.Context, inst benchmarks.Instance, env benchmarks.Env) (*evalv2.CellResult, error) {
	p, ok := inst.Payload.(Payload)
	if !ok {
		return nil, fmt.Errorf("niah: payload type=%T, want niah.Payload", inst.Payload)
	}
	if env.Workdir == "" {
		return nil, errors.New("niah: env.Workdir is required (fresh per-instance .cortex/)")
	}

	start := time.Now()

	binary, err := benchmarks.ResolveCortexBinary()
	if err != nil {
		return nil, fmt.Errorf("niah: %w", err)
	}

	hay := Generate(GenerateOpts{
		Length:     p.LengthTok,
		Depth:      p.Depth,
		Needle:     p.Needle,
		Seed:       p.Seed,
		FillerMode: p.FillerMode, // zero value resolves to adversarial
	})

	sessionID := fmt.Sprintf("niah-%s-%g", p.LengthLabel, p.Depth)
	evs := chunkHaystack(hay.Text, sessionID)

	if err := benchmarks.RunBulkCapture(ctx, binary, env.Workdir, evs); err != nil {
		return nil, fmt.Errorf("niah: capture haystack: %w", err)
	}
	if err := benchmarks.RunIngest(ctx, binary, env.Workdir); err != nil {
		return nil, fmt.Errorf("niah: ingest: %w", err)
	}

	probe := buildProbe(p.Needle)
	out, err := benchmarks.RunSearch(ctx, binary, env.Workdir, benchmarks.SearchFast, 10, probe)
	if err != nil {
		return nil, fmt.Errorf("niah: search: %w", err)
	}

	score := scoreRetrieval(out, p.Needle)
	fillerLabel := string(p.FillerMode)
	if fillerLabel == "" {
		fillerLabel = string(FillerAdversarial)
	}
	notes := fmt.Sprintf(
		"length=%s depth=%.2f filler=%s top_score=%.3f runner_up=%.3f gap=%.3f results=%d needle_position=%s",
		p.LengthLabel, p.Depth, fillerLabel,
		score.TopScore, score.RunnerUpScore, score.ScoreGap, score.ResultCount, score.Position,
	)

	cell := &evalv2.CellResult{
		SchemaVersion:        evalv2.CellResultSchemaVersion,
		RunID:                newRunID(),
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
		Benchmark:            "niah",
		ScenarioID:           inst.ID,
		SessionID:            sessionID,
		Harness:              evalv2.HarnessCortex,
		Provider:             evalv2.ProviderLocal,
		Model:                "niah-substring",
		ContextStrategy:      evalv2.StrategyCortex,
		CortexVersion:        cortexVersion,
		Temperature:          0,
		LatencyMs:            time.Since(start).Milliseconds(),
		TaskSuccessCriterion: evalv2.CriterionScenarioAssertion,
		TaskSuccess:          score.Hit,
		Notes:                notes,
	}
	if score.Hit {
		cell.TestsPassed = 1
	} else {
		cell.TestsFailed = 1
	}

	if env.Verbose {
		fmt.Printf("[niah] %s %s top=%.3f gap=%.3f results=%d position=%s\n",
			inst.ID, passFail(score.Hit), score.TopScore, score.ScoreGap, score.ResultCount, score.Position)
	}
	return cell, nil
}

// chunkHaystack splits text into overlapping windows and returns one
// events.Event per chunk. The overlap (chunkSize - chunkStride) is
// sized so any substring of length ≤ chunkStride is fully contained in
// at least one chunk — protecting the needle from being split across
// chunk boundaries.
//
// Pure function: no I/O, no time-dependent IDs. Timestamp is set to
// time.Now once per chunk because the journal serializer rejects zero
// timestamps; the precise value doesn't matter for retrieval.
func chunkHaystack(text, sessionID string) []*events.Event {
	if text == "" {
		return nil
	}
	now := time.Now()
	var evs []*events.Event
	for i := 0; i < len(text); i += chunkStride {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		evs = append(evs, &events.Event{
			Source:     events.SourceGeneric,
			EventType:  events.EventToolUse,
			Timestamp:  now,
			ToolName:   "niah_chunk",
			ToolInput:  map[string]any{"chunk_offset": i},
			ToolResult: text[i:end],
			Context: events.EventContext{
				SessionID: sessionID,
			},
			Metadata: map[string]any{"capture_type": "observation"},
		})
		if end == len(text) {
			break
		}
	}
	return evs
}

// buildProbe derives the search query from the needle. For
// well-formed needles ("The secret recipe code is 4F-9X-2B.") this
// drops the secret-looking trailing token, leaving a probe phrase
// that lexically lands on the needle's chunk via Reflex's text
// fallback. For pathological needles (single token, all digits)
// it falls back to the whole needle.
func buildProbe(needle string) string {
	words := strings.Fields(needle)
	if len(words) <= 2 {
		return needle
	}
	// Drop the trailing token, which is typically the "secret value"
	// portion (e.g. "4F-9X-2B."). Punctuation trims off naturally.
	trimmed := strings.Join(words[:len(words)-1], " ")
	trimmed = strings.TrimRight(trimmed, ".,;:!?")
	if trimmed == "" {
		return needle
	}
	return trimmed
}

// retrievalScore is the structured summary of a single search response.
// It captures enough signal for an operator to triage a regression
// without re-running the benchmark:
//
//   - Hit: did the needle survive at all (substring in any top-K)?
//   - Position: 1-indexed rank of the earliest matching result, or
//     "missing" on a miss.
//   - TopScore, RunnerUpScore: the two highest scores returned.
//   - ScoreGap: TopScore − RunnerUpScore. A shrinking gap across
//     runs is the leading indicator of scorer regression — the
//     needle still hits position 1 but only barely.
//   - ResultCount: how many chunks the CLI actually returned. A drop
//     from N to 1 is the leading indicator of a retrieval regression
//     (overly aggressive filtering, broken text-search fallback).
//
// Nil-safe: zero results → Hit=false, Position="missing", zeros for
// the scalar fields.
type retrievalScore struct {
	Hit           bool
	Position      string
	TopScore      float64
	RunnerUpScore float64
	ScoreGap      float64
	ResultCount   int
}

func scoreRetrieval(out *benchmarks.SearchOutput, needle string) retrievalScore {
	s := retrievalScore{Position: "missing"}
	if out == nil || len(out.Results) == 0 {
		return s
	}
	s.ResultCount = len(out.Results)

	// Find the two highest scores — robust to unsorted result slices.
	for _, r := range out.Results {
		switch {
		case r.Score > s.TopScore:
			s.RunnerUpScore = s.TopScore
			s.TopScore = r.Score
		case r.Score > s.RunnerUpScore:
			s.RunnerUpScore = r.Score
		}
	}
	s.ScoreGap = s.TopScore - s.RunnerUpScore

	for i, r := range out.Results {
		if strings.Contains(r.Content, needle) {
			s.Hit = true
			s.Position = strconv.Itoa(i + 1)
			break
		}
	}
	return s
}

// ParseLengthLabel parses CLI length tokens ("8k", "16K", "4000") into
// an integer token count. Suffix is case-insensitive.
func ParseLengthLabel(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty length")
	}
	low := strings.ToLower(s)
	mult := 1
	if strings.HasSuffix(low, "k") {
		mult = 1024
		low = strings.TrimSuffix(low, "k")
	}
	n, err := strconv.Atoi(low)
	if err != nil {
		return 0, fmt.Errorf("parse length %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative length %q", s)
	}
	return n * mult, nil
}

// splitCSV splits a comma-separated list and trims whitespace from
// each value, dropping empties. Used uniformly for --lengths and
// --depths.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// newRunID returns a 16-byte hex string. Persister uniqueness is
// enforced via SQLite's UNIQUE(run_id) constraint, so collisions
// surface as INSERT-or-IGNORE no-ops rather than silent overwrites.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
