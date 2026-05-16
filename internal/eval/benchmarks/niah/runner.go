package niah

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/capture"
	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/eval/benchmarks"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/processor"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
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
}

func init() { benchmarks.Register("niah", func() benchmarks.Benchmark { return &runner{} }) }

type runner struct{}

func (runner) Name() string { return "niah" }

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
				},
			})
		}
	}

	if opts.Limit > 0 && opts.Limit < len(insts) {
		insts = insts[:opts.Limit]
	}
	return insts, nil
}

// Run executes one NIAH instance: synthesize the haystack, capture it
// into a fresh per-workdir Cortex store, ingest, query via Fast mode,
// score on substring match. Returns a fully-validated CellResult that
// the CLI hands off to the standard Persister fan-out (journal →
// SQLite + JSONL).
func (runner) Run(ctx context.Context, inst benchmarks.Instance, env benchmarks.Env) (*evalv2.CellResult, error) {
	p, ok := inst.Payload.(Payload)
	if !ok {
		return nil, fmt.Errorf("niah: payload type=%T, want niah.Payload", inst.Payload)
	}
	if env.Workdir == "" {
		return nil, errors.New("niah: env.Workdir is required (fresh per-instance .cortex/)")
	}

	start := time.Now()

	hay := Generate(GenerateOpts{
		Length: p.LengthTok,
		Depth:  p.Depth,
		Needle: p.Needle,
		Seed:   p.Seed,
	})

	storeDir := filepath.Join(env.Workdir, ".cortex")
	cfg := &config.Config{ContextDir: storeDir}
	store, err := storage.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("niah: storage.New: %w", err)
	}
	defer store.Close()

	captureClient := capture.New(cfg)
	sessionID := fmt.Sprintf("niah-%s-%g", p.LengthLabel, p.Depth)
	if err := captureHaystack(captureClient, hay.Text, sessionID); err != nil {
		return nil, fmt.Errorf("niah: capture haystack: %w", err)
	}

	proc := processor.New(cfg, store)
	if _, err := proc.RunBatch(); err != nil {
		return nil, fmt.Errorf("niah: ingest: %w", err)
	}

	cx, err := intcognition.New(store, nil, nil, cfg)
	if err != nil {
		return nil, fmt.Errorf("niah: cognition.New: %w", err)
	}

	probe := buildProbe(p.Needle)
	res, err := cx.Retrieve(ctx, cognition.Query{Text: probe, Limit: 10}, cognition.Fast)
	if err != nil {
		return nil, fmt.Errorf("niah: retrieve: %w", err)
	}

	hit, topScore, position := scoreRetrieval(res, p.Needle)
	notes := fmt.Sprintf("length=%s depth=%.2f top_score=%.3f needle_position=%s",
		p.LengthLabel, p.Depth, topScore, position)

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
		TaskSuccess:          hit,
		Notes:                notes,
	}
	if hit {
		cell.TestsPassed = 1
	} else {
		cell.TestsFailed = 1
	}

	if env.Verbose {
		fmt.Printf("[niah] %s %s top_score=%.3f position=%s\n",
			inst.ID, passFail(hit), topScore, position)
	}
	return cell, nil
}

// captureHaystack chunks text into overlapping windows and writes one
// capture.event per chunk. The overlap (chunkSize - chunkStride) is
// sized so any substring of length ≤ chunkStride is fully contained in
// at least one chunk — protecting the needle from being split across
// chunk boundaries.
func captureHaystack(c *capture.Capture, text, sessionID string) error {
	if text == "" {
		return nil
	}
	for i := 0; i < len(text); i += chunkStride {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		ev := &events.Event{
			Source:     events.SourceGeneric,
			EventType:  events.EventToolUse,
			Timestamp:  time.Now(),
			ToolName:   "niah_chunk",
			ToolInput:  map[string]any{"chunk_offset": i},
			ToolResult: text[i:end],
			Context: events.EventContext{
				SessionID: sessionID,
			},
			Metadata: map[string]any{"capture_type": "observation"},
		}
		if err := c.CaptureEvent(ev); err != nil {
			return fmt.Errorf("capture chunk @%d: %w", i, err)
		}
		if end == len(text) {
			break
		}
	}
	return nil
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

// scoreRetrieval inspects res for any result whose Content contains
// the needle as a substring. Returns (hit, topScore, position):
//   - topScore is the MAX score across all returned results (robust
//     to unsorted result slices; matches operator intuition for
//     "best result's score").
//   - position is the 1-indexed rank of the EARLIEST matching result,
//     or "missing" when no result contains the needle.
//
// Nil-safe: a nil or empty result returns (false, 0, "missing").
func scoreRetrieval(res *cognition.ResolveResult, needle string) (bool, float64, string) {
	if res == nil || len(res.Results) == 0 {
		return false, 0, "missing"
	}
	var top float64
	for _, r := range res.Results {
		if r.Score > top {
			top = r.Score
		}
	}
	for i, r := range res.Results {
		if strings.Contains(r.Content, needle) {
			return true, top, strconv.Itoa(i + 1)
		}
	}
	return false, top, "missing"
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
