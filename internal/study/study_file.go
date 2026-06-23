package study

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
)

// study_file is the size-adaptive reading primitive. It degenerates to
// a plain whole-file read when the file fits the consuming model's
// window, and otherwise samples a bounded set of byte regions, refines
// them to real line bounds, and (when an InferFunc is wired) infers a
// provenance-constrained digest over only the sampled regions.
//
// The library carries no DAG or harness coupling: it is a pure
// request/response so both the DAG and a hand-built harness can call it.
// Window resolution (probing the consuming model) stays in the adapter;
// the library just takes req.Window.

// Focus drives DENSIFY/TARGET deepening: a line range, a symbol, or a
// semantic query. Lines is authoritative; Symbol/Query are resolved to a
// line range by ResolveFocus (added with the focus sampler). Path names
// a file or subtree (caller-relative, slash-separated) for corpus
// studies, where line numbers alone are ambiguous across files; Lines
// then narrows within it.
type Focus struct {
	Path   string `json:"path,omitempty"`
	Lines  [2]int `json:"lines,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Query  string `json:"query,omitempty"`
}

// StudyRequest is the tool contract input (see docs/study-file.md).
type StudyRequest struct {
	Path       string  // absolute or workdir-relative file path
	RelPath    string  // project-relative path for citations; defaults to base(Path)
	Density    Density // "sparse"|"normal"|"dense"|int k; default normal
	Focus      *Focus  // optional; drives DENSIFY/TARGET
	Session    string  // resumable coverage key; optional
	Window     int     // consuming-model window in tokens; 0 → default
	ContextDir string  // .cortex/ for cached probes (adapter use)
	Goal       string  // optional task hint passed to inference

	// Fill is the per-chunk target as a fraction of Window; 0 → the
	// byte-grid default (1/8). Smaller fill + larger Density trades
	// chunk size for chunk count at the same total sample (keep
	// Density × Fill ≤ 1 so one pass fits the window). Chunk bytes
	// still clamp to the grid's [2KB, 256KB] bounds.
	Fill float64

	// Numbered overrides per-line snippet numbering in the inference
	// prompt (see InferInput.Numbered). Nil → format default.
	Numbered *bool

	// Covered, when non-nil, is the session's accumulated covered-chunk
	// set. StudyFile seeds the sampler from it (so a deepening pass draws
	// NEW regions, not the same ones) and adds the chunks it samples to
	// it. Nil → a fresh, single-pass draw. StudyLoop threads this across
	// passes to realize "re-study refines rather than repeats."
	Covered map[string]bool

	// PriorFindings are earlier passes' distilled results, carried as working
	// memory. sampleAndInfer renders them at the prompt front (before the new
	// sample, so the [system][goal][findings] prefix is cache-stable) and
	// carves their token budget out of the sample, growth-capped with a sample
	// floor (see FindingsBudgetChars). Nil → today's independent-pass behavior.
	// StudyLoop threads these across passes; a single StudyFile call leaves them
	// nil.
	PriorFindings []Finding

	// NoWorkingMemory disables the findings prefix in StudyLoop — passes run
	// independently, as before P1. The eval baseline (findings on vs off) and a
	// kill-switch until the eval makes working memory a default.
	NoWorkingMemory bool

	// CurateFindings (P2) replaces P1's recency-drop bound with value-ranked
	// keep/compress/evict when the findings block crosses its budget. Off → P1
	// behavior. Compress overrides the mechanical compressor (e.g. an LLM
	// attend.distill); OnEvict is called per evicted finding so the harness can
	// demote it to the journal. See curate.go.
	CurateFindings bool
	Compress       CompressFunc
	OnEvict        func(Finding)

	// DirectedSampling (P3) steers each deepening pass toward the most recent
	// unexplored Lead accumulated across prior findings, instead of a blind
	// disjoint draw. Requires working memory. Off → the curator's blind
	// densify/target decision stands. See directed.go.
	DirectedSampling bool

	// UseAST chunks Go files by top-level declaration (go/ast) instead of byte
	// windows — coherent, declaration-aligned regions. Falls back to the byte
	// grid for non-Go or unparseable files. See analyzer_ast.go.
	UseAST bool

	// Infer, when non-nil, runs phase-2 inference over the sampled
	// regions. Nil → mechanical sample only (the --sample-only path).
	Infer InferFunc

	// sampler overrides the default HierarchicalSampler. Test seam.
	sampler Sampler
}

// SampledChunk is one mechanically-sampled region, labelled with its
// real relpath:line_start-line_end after refinement. The Snippet is the
// exact bytes read — this is what phase-2 inference sees.
type SampledChunk struct {
	RelPath    string `json:"relpath"`
	LineStart  int    `json:"line_start"`
	LineEnd    int    `json:"line_end"`
	ByteOffset int64  `json:"byte_offset"`
	ByteLength int    `json:"byte_length"`
	EffLines   int    `json:"eff_lines"`
	Snippet    string `json:"snippet"`
}

// Citation is a provenance-validated claim: every citation's line range
// must fall inside some sampled chunk (see ValidateCitations).
type Citation struct {
	RelPath    string `json:"relpath"`
	LineStart  int    `json:"line_start"`
	LineEnd    int    `json:"line_end"`
	ByteOffset int64  `json:"byte_offset,omitempty"`
	Claim      string `json:"claim"`
}

// Lead is a candidate region worth deepening — emitted when inference
// references something it did NOT sample (off-sample need → lead, never
// a hallucinated citation).
type Lead struct {
	RelPath  string `json:"relpath"`
	NearLine int    `json:"near_line"`
	Why      string `json:"why"`
}

// Finding is one prior pass's distilled result, carried into later passes as
// working memory (the curated-findings-prefix design,
// docs/working-memory-study.md). It is stored STRUCTURED — not pre-joined into
// a string — so P2 curation can score/compress/evict units while preserving
// each finding's citation anchors (the relay contract). P1 appends and renders;
// it does not curate.
type Finding struct {
	Pass      int        `json:"pass"`
	Digest    string     `json:"digest"`
	Citations []Citation `json:"citations,omitempty"`
	Leads     []Lead     `json:"leads,omitempty"`
}

// Coverage is the fraction of effective lines the sample has seen.
type Coverage struct {
	EffLinesSeen  int     `json:"eff_lines_seen"`
	EffLinesTotal int     `json:"eff_lines_total"`
	Pct           float64 `json:"pct"`
}

// DeepenRef mirrors a follow-up request: how to ask for more.
type DeepenRef struct {
	Session string  `json:"session,omitempty"`
	Density Density `json:"density,omitempty"`
	Focus   *Focus  `json:"focus,omitempty"`
}

// Deepen carries the two deepening affordances the curator/agent uses.
type Deepen struct {
	Densify DeepenRef `json:"densify"`
	Target  DeepenRef `json:"target"`
}

// StudyResponse is the tool contract output (see docs/study-file.md).
type StudyResponse struct {
	Mode        string         `json:"mode"` // "read" | "study"
	ReadContent string         `json:"-"`    // populated when Mode=="read"
	Digest      string         `json:"digest,omitempty"`
	Citations   []Citation     `json:"citations,omitempty"`
	Coverage    Coverage       `json:"coverage"`
	Leads       []Lead         `json:"leads,omitempty"`
	Deepen      Deepen         `json:"deepen"`
	Exhausted   bool           `json:"exhausted"`
	Sampled     []SampledChunk `json:"-"` // mechanical sample (checkpoint/inference source)
	// FindingRelays counts citations this pass admitted by relaying a prior
	// finding's grounded citation (admitFindingRelays) — a continuity signal for
	// citation REUSE. ~0 under study's disjoint sampling (see eval-journal
	// 2026-06-19); kept as a cheap correctness check (always 0 with WM off).
	FindingRelays int `json:"finding_relays,omitempty"`
	// CachePrefix is this pass's stable [system][goal][findings] prefix (P4).
	// StudyLoop compares consecutive prefixes to measure cross-pass cache
	// warmth. Not serialized — it duplicates prompt content already on the wire.
	CachePrefix string `json:"-"`
}

// StudyFile runs the size-adaptive read. See the package doc above.
// Directories take the corpus path (study_dir.go): same threshold, same
// sampler and provenance machinery, with the file tree as the boundary
// space instead of one file's byte space.
func StudyFile(ctx context.Context, req StudyRequest) (StudyResponse, error) {
	fi, err := os.Stat(req.Path)
	if err != nil {
		return StudyResponse{}, fmt.Errorf("study: stat %s: %w", req.Path, err)
	}
	if fi.IsDir() {
		return studyDir(ctx, req)
	}
	size := fi.Size()

	window := req.Window
	if window <= 0 {
		window = studyDefaultCtxWindow
	}

	// Threshold: it fits → just read it.
	if size/studyCharsPerToken < int64(window/2) {
		content, rerr := fractal.ReadRegion(req.Path, 0, int(size))
		if rerr != nil {
			return StudyResponse{}, fmt.Errorf("study: read %s: %w", req.Path, rerr)
		}
		return StudyResponse{Mode: "read", ReadContent: content}, nil
	}

	// Study path: sample → refine → (infer).
	relPath := req.RelPath
	if relPath == "" {
		relPath = filepath.Base(req.Path)
	}
	opts := ByteGridOpts{
		WindowTokens: window,
		TargetFill:   req.Fill,
		Salt:         req.Session,
		ModTimeUnix:  fi.ModTime().Unix(),
	}
	var out *BoundaryOutput
	// AST boundary producer for Go files (opt-in): declaration-aligned chunks
	// instead of byte windows. Falls back to the byte grid for non-Go files or
	// on any parse error — never fatal.
	if req.UseAST && langFor(filepath.Ext(relPath)) == "go" {
		if src, rerr := fractal.ReadRegion(req.Path, 0, int(size)); rerr == nil {
			if g, _, aerr := BuildASTGrid(req.Path, relPath, []byte(src), opts); aerr == nil {
				out = g
			}
		}
	}
	if out == nil {
		out = BuildByteGrid(req.Path, relPath, size, opts)
	}

	// The first chunk's size seeds budget-derived density (byte grid is uniform;
	// for the AST grid this is just the header/first-decl size — a reasonable unit).
	autoUnit := 0
	if len(out.Chunks) > 0 {
		autoUnit = out.Chunks[0].ByteLength
	}
	return sampleAndInfer(ctx, req, out, relPath, autoUnit, window)
}

// sampleAndInfer is the post-boundary pipeline shared by the file and
// directory paths: density resolution → (focus) sampler → region reads
// with lazy refinement for grids that need it → coverage → deepening
// affordances → optional phase-2 inference.
func sampleAndInfer(ctx context.Context, req StudyRequest, out *BoundaryOutput, display string, autoUnit, window int) (StudyResponse, error) {
	// Density resolution. An explicit Density (named level or int) is
	// honored as-is. Nil derives k from the budget: window / chunk
	// target, so one pass samples the full window in unit-sized
	// fragments — maximum breadth at the format's coherence size (the
	// 2026-06-10 granularity sweep: breadth at unit size beats fewer,
	// coarser fragments at equal data).
	// Per-call INPUT budget in chars — the same budget MakePlan derives, which
	// reserves prompt overhead + output room and takes a fill fraction of the
	// window. Sizing k from this (not the raw window) is what keeps one inference
	// call inside the model's context: the old `window * charsPerToken` form
	// targeted ~100% of the window in input alone, so the call overflowed once
	// the system prompt, snippet numbering, and the completion were added (and
	// code tokenizes denser than the 4-chars/token estimate). Passes don't enter
	// this — each pass is a separate, independently-budgeted call.
	budgetChars := SampleTokenBudget(window, studyDefaultTargetFill) * studyCharsPerToken

	// Working memory: prior findings ride the prompt front and consume part of
	// the window, so carve their budget out of the sample — growth-capped, and
	// floored so the new sample never starves (docs/working-memory-study.md).
	// The retained set is what BOTH the prompt and the relay validation use.
	// P2 (CurateFindings) value-ranks keep/compress/evict under budget pressure;
	// otherwise P1's recency drop bounds the block.
	fbudget := FindingsBudgetChars(window, len(req.PriorFindings))
	var findings []Finding
	if req.CurateFindings {
		var evicted []Finding
		findings, evicted = curateFindings(req.PriorFindings, fbudget, req.Goal, req.Compress)
		if req.OnEvict != nil {
			for _, f := range evicted {
				req.OnEvict(f)
			}
		}
	} else {
		findings = trimFindingsToBudget(req.PriorFindings, fbudget)
	}
	if cost := findingsCharsTotal(findings); cost > 0 {
		if budgetChars-cost < sampleFloorChars(window) {
			budgetChars = sampleFloorChars(window)
		} else {
			budgetChars -= cost
		}
	}

	k := ResolveDensity(req.Density)
	if req.Density == nil && autoUnit > 0 {
		// Nil → k IS the budget-derived count (budget / unit), so one pass
		// fills the window at unit granularity. Take it directly rather than
		// max(normal, ak): when the budget affords FEWER fragments than the
		// normal-8 default (small window or large unit — now the common case
		// at the 0.3 fill), the old max kept k=8, overshooting the budget so
		// the cumulative cap below trimmed the sample while Densify still
		// advertised the inflated k. ak==0 (unit > whole budget) keeps the
		// default so the draw is never empty.
		if ak := budgetChars / autoUnit; ak > 0 {
			k = ak
		}
	}

	var sampler Sampler = &HierarchicalSampler{}
	if req.sampler != nil {
		sampler = req.sampler
	}
	// Focus precedence: an explicit Focus (a curator/directed target) wins;
	// otherwise pass-1 sampling is blind breadth (the sampler's anti-coverage
	// bias spreads draws across uncovered regions).
	focus := req.Focus
	if focus != nil {
		sampler = newFocusSampler(sampler, out, *focus)
	}

	// Session coverage: seed the sampler with what prior passes already
	// saw so this pass draws new regions, then fold this pass's picks
	// back in. Nil Covered → a fresh single-pass draw (unchanged).
	covered := req.Covered
	if covered == nil {
		covered = map[string]bool{}
	}
	rng := rand.New(rand.NewSource(out.RNGSeed))
	ids := sampler.Next(out, covered, k, rng)

	byID := make(map[string]*Chunk, len(out.Chunks))
	for i := range out.Chunks {
		byID[out.Chunks[i].ID] = &out.Chunks[i]
	}

	// Line-base lookups are per file: byte-grid chunks all share one
	// path; corpus chunks arrive Refined, so their entry is never built.
	lineBases := map[string]func(int64) (int, error){}
	lineBaseFor := func(path string) func(int64) (int, error) {
		lb, ok := lineBases[path]
		if !ok {
			lb = streamingLineBase(path)
			lineBases[path] = lb
		}
		return lb
	}

	sampled := make([]SampledChunk, 0, len(ids))
	seenEff := 0
	usedChars := 0
	for _, id := range ids {
		ch := byID[id]
		if ch == nil {
			continue
		}
		if !ch.Refined {
			if rerr := RefineChunk(ch, lineBaseFor(ch.Path)); rerr != nil {
				return StudyResponse{}, rerr
			}
		}
		body, berr := fractal.ReadRegion(ch.Path, ch.ByteOffset, ch.ByteLength)
		if berr != nil {
			return StudyResponse{}, fmt.Errorf("study: read region %s@%d: %w", ch.RelPath, ch.ByteOffset, berr)
		}
		// Budget guard on the AUTO path: there k is budget-derived, and a
		// directory study mixes chunk sizes across files so the derived k can
		// still overshoot — this cumulative cap makes a single call fit
		// regardless. Always keep at least one chunk so the call is never empty;
		// skipped chunks stay uncovered, so the next deepening pass draws them.
		// An explicit Density is the caller's choice and is honored as requested.
		if req.Density == nil && len(sampled) > 0 && usedChars+len(body) > budgetChars {
			break
		}
		usedChars += len(body)
		covered[id] = true // fold into session coverage for the next pass
		seenEff += ch.EffLines
		sampled = append(sampled, SampledChunk{
			RelPath:    ch.RelPath,
			LineStart:  ch.LineStart,
			LineEnd:    ch.LineEnd,
			ByteOffset: ch.ByteOffset,
			ByteLength: ch.ByteLength,
			EffLines:   ch.EffLines,
			Snippet:    body,
		})
	}

	cov := Coverage{EffLinesSeen: seenEff, EffLinesTotal: out.EffTotalLines}
	if out.EffTotalLines > 0 {
		cov.Pct = float64(seenEff) / float64(out.EffTotalLines)
		// The numerator counts REFINED eff-lines but the denominator is
		// the grid's bytes-per-line estimate; files with short lines
		// (markdown) push the ratio past 1. Clamp — coverage is a
		// fraction, and >100% misreads as a scoring bug.
		if cov.Pct > 1 {
			cov.Pct = 1
		}
	}

	resp := StudyResponse{
		Mode:      "study",
		Coverage:  cov,
		Sampled:   sampled,
		Exhausted: len(ids) < k || cov.Pct >= studyDefaultMaxCoverage,
		Deepen: Deepen{
			Densify: DeepenRef{Session: req.Session, Density: densifyDensity(req.Density, k)},
			Target:  DeepenRef{Session: req.Session, Focus: req.Focus},
		},
	}

	// Phase-2 inference. Nil Infer → mechanical --sample-only path. On
	// inference error the mechanical resp (sample + coverage) is still
	// returned alongside the error so callers can degrade gracefully.
	inferInput := InferInput{
		Path:          req.Path,
		RelPath:       display,
		Sampled:       sampled,
		Focus:         req.Focus,
		Goal:          req.Goal,
		Numbered:      req.Numbered,
		PriorFindings: findings,
	}
	resp.CachePrefix = CacheablePrefix(inferInput) // P4: the cache-stable head
	if req.Infer != nil {
		io, ierr := req.Infer(ctx, inferInput)
		if ierr != nil {
			return resp, fmt.Errorf("study: inference: %w", ierr)
		}
		resp.Digest = io.Digest
		resp.Leads = io.Leads
		resp.Citations = ValidateCitations(io.Citations, sampled, nil)
		// Working memory: also keep citations that faithfully relay a prior
		// finding's already-grounded citation (the model saw the anchor in the
		// findings prefix, not in this pass's sample). The count is the
		// continuity signal.
		grounded := len(resp.Citations)
		resp.Citations = admitFindingRelays(io.Citations, resp.Citations, findings)
		resp.FindingRelays = len(resp.Citations) - grounded
		for i := range resp.Citations {
			if off, ok := byteOffsetForCitation(resp.Citations[i], sampled); ok {
				resp.Citations[i].ByteOffset = off
			}
		}
	}
	return resp, nil
}

// byteOffsetForCitation anchors a validated citation to the byte offset
// of the sampled chunk that contains it, so downstream consumers can emit
// a real file:line+byte reference.
func byteOffsetForCitation(c Citation, sampled []SampledChunk) (int64, bool) {
	for _, s := range sampled {
		if s.RelPath == c.RelPath && c.LineStart >= s.LineStart && c.LineEnd <= s.LineEnd {
			return s.ByteOffset, true
		}
	}
	return 0, false
}

// densifyDensity returns the density for the Densify deepening
// affordance. Explicit densities step up a named level; a nil (auto)
// density already fills the window each pass, so densifying means
// another full-budget pass over new regions at the same k — the
// covered set guarantees novelty, and going denser would overflow.
func densifyDensity(d Density, autoK int) Density {
	if d == nil {
		return autoK
	}
	return nextDenserDensity(d)
}

// nextDenserDensity returns the next density level up, for the Densify
// deepening affordance.
func nextDenserDensity(d Density) Density {
	switch ResolveDensity(d) {
	case densitySparseK:
		return "normal"
	case densityNormalK:
		return "dense"
	default:
		return "dense"
	}
}
