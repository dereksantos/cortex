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
// line range by ResolveFocus (added with the focus sampler).
type Focus struct {
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

	// Covered, when non-nil, is the session's accumulated covered-chunk
	// set. StudyFile seeds the sampler from it (so a deepening pass draws
	// NEW regions, not the same ones) and adds the chunks it samples to
	// it. Nil → a fresh, single-pass draw. StudyLoop threads this across
	// passes to realize "re-study refines rather than repeats."
	Covered map[string]bool

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
}

// StudyFile runs the size-adaptive read. See the package doc above.
func StudyFile(ctx context.Context, req StudyRequest) (StudyResponse, error) {
	fi, err := os.Stat(req.Path)
	if err != nil {
		return StudyResponse{}, fmt.Errorf("study: stat %s: %w", req.Path, err)
	}
	if fi.IsDir() {
		return StudyResponse{}, fmt.Errorf("study: %s is a directory, not a file", req.Path)
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
	k := ResolveDensity(req.Density)
	out := BuildByteGrid(req.Path, relPath, size, ByteGridOpts{
		WindowTokens: window,
		TargetFill:   req.Fill,
		Salt:         req.Session,
		ModTimeUnix:  fi.ModTime().Unix(),
	})

	var sampler Sampler = &HierarchicalSampler{}
	if req.sampler != nil {
		sampler = req.sampler
	}
	if req.Focus != nil {
		sampler = newFocusSampler(sampler, out, *req.Focus)
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

	lineBase := streamingLineBase(req.Path)
	sampled := make([]SampledChunk, 0, len(ids))
	seenEff := 0
	for _, id := range ids {
		ch := byID[id]
		if ch == nil {
			continue
		}
		if rerr := RefineChunk(ch, lineBase); rerr != nil {
			return StudyResponse{}, rerr
		}
		body, berr := fractal.ReadRegion(ch.Path, ch.ByteOffset, ch.ByteLength)
		if berr != nil {
			return StudyResponse{}, fmt.Errorf("study: read region %s@%d: %w", ch.RelPath, ch.ByteOffset, berr)
		}
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
	}

	resp := StudyResponse{
		Mode:      "study",
		Coverage:  cov,
		Sampled:   sampled,
		Exhausted: len(ids) < k || cov.Pct >= studyDefaultMaxCoverage,
		Deepen: Deepen{
			Densify: DeepenRef{Session: req.Session, Density: nextDenserDensity(req.Density)},
			Target:  DeepenRef{Session: req.Session, Focus: req.Focus},
		},
	}

	// Phase-2 inference. Nil Infer → mechanical --sample-only path. On
	// inference error the mechanical resp (sample + coverage) is still
	// returned alongside the error so callers can degrade gracefully.
	if req.Infer != nil {
		io, ierr := req.Infer(ctx, InferInput{
			Path:    req.Path,
			RelPath: relPath,
			Sampled: sampled,
			Focus:   req.Focus,
			Goal:    req.Goal,
		})
		if ierr != nil {
			return resp, fmt.Errorf("study: inference: %w", ierr)
		}
		resp.Digest = io.Digest
		resp.Leads = io.Leads
		resp.Citations = ValidateCitations(io.Citations, sampled, nil)
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
