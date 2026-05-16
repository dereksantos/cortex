// Package niah implements the needle-in-a-haystack benchmark: hide a
// known fact at a configurable depth inside a synthetic haystack, then
// verify Cortex's Reflex layer can retrieve it. The benchmark is a
// permanent regression smoke for embedding + rerank quality; if a
// needle goes missing at 32K, it shows up here in CI before it becomes
// a multi-point LongMemEval regression.
package niah

import (
	"math/rand"
	"strings"
)

// GenerateOpts controls one haystack synthesis. Defaults are caller
// responsibility (the runner sets them from CLI flags); zero values are
// valid (Length=0 produces a needle-only haystack, useful for unit
// isolation).
type GenerateOpts struct {
	// Length is the *target* haystack size measured in tokens. The
	// actual byte length is approximated as Length*4 (the standard
	// "1 token ≈ 4 chars" rule of thumb), so a Length=16384 request
	// produces ~64 KiB of text.
	Length int

	// Depth is the fractional position (0.0..1.0) where the needle
	// lands inside the filler. 0.0 puts the needle at the start; 1.0
	// puts it at the end (such that the needle's tail aligns with the
	// haystack's tail). Out-of-range values are clamped.
	Depth float64

	// Needle is the literal string that must be retrievable. The
	// generator splices it in verbatim — no transformation — so the
	// retriever's substring match is unambiguous.
	Needle string

	// Seed determines the deterministic shuffle of the lorem corpus.
	// Same Seed + same other opts → byte-identical Haystack.Text.
	Seed int64
}

// Haystack is the synthesized text plus the byte offset where the
// needle was placed. Callers use NeedleOffset for debugging missed
// retrievals (e.g. "needle was at offset 24576 of 65536, which falls
// in chunk 12 of 32; why didn't chunk 12's capture surface?").
type Haystack struct {
	Text         string
	NeedleOffset int
}

// loremCorpus is a small, finite pool of phrases used to build filler.
// Kept short so the generator's output is recognizable as synthetic and
// doesn't accidentally collide with real Cortex content during search.
// New phrases can be appended without breaking determinism — the seed
// indexes into the full slice, so longer corpora just give more variety.
var loremCorpus = []string{
	"the quick brown fox jumps over the lazy dog. ",
	"lorem ipsum dolor sit amet, consectetur adipiscing elit. ",
	"sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. ",
	"ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. ",
	"duis aute irure dolor in reprehenderit in voluptate velit esse. ",
	"excepteur sint occaecat cupidatat non proident, sunt in culpa qui. ",
	"officia deserunt mollit anim id est laborum, consectetur tempor. ",
	"all work and no play makes jack a dull boy, again and again. ",
	"a journey of a thousand miles begins with a single step forward. ",
	"the only thing necessary for the triumph of evil is good men do nothing. ",
	"in the middle of difficulty lies opportunity for those who look. ",
	"that which does not kill us makes us stronger over time and trial. ",
	"to be or not to be, that is the question of every philosophical inquiry. ",
	"all that glitters is not gold, but some of it certainly is. ",
	"the road to hell is paved with good intentions and broken promises. ",
	"absence makes the heart grow fonder as the days turn into weeks. ",
}

// Generate produces a Haystack matching opts. Determinism: same opts
// (including Seed) yield byte-identical Text and NeedleOffset.
//
// Algorithm: build approximately Length*4 chars of filler by drawing
// phrases from loremCorpus via a seeded rand.Rand, then splice the
// needle in at byte offset round(Depth * fillerLen). For Depth=1.0
// the needle is appended at the very end (its tail aligns with the
// haystack tail). For Length=0 the haystack is the needle alone.
func Generate(opts GenerateOpts) Haystack {
	if opts.Length <= 0 {
		return Haystack{Text: opts.Needle, NeedleOffset: 0}
	}

	depth := opts.Depth
	if depth < 0 {
		depth = 0
	}
	if depth > 1 {
		depth = 1
	}

	targetBytes := opts.Length * 4
	r := rand.New(rand.NewSource(opts.Seed)) //nolint:gosec // deterministic, not cryptographic

	var b strings.Builder
	b.Grow(targetBytes + len(opts.Needle))
	for b.Len() < targetBytes {
		phrase := loremCorpus[r.Intn(len(loremCorpus))]
		b.WriteString(phrase)
	}
	filler := b.String()

	// Splice point inside filler. Clamp so the needle fits without
	// overrunning targetBytes; for Depth=1.0 the needle's tail aligns
	// with the haystack tail.
	maxOffset := len(filler)
	splice := int(depth * float64(maxOffset))
	if splice > maxOffset {
		splice = maxOffset
	}
	if splice < 0 {
		splice = 0
	}

	text := filler[:splice] + opts.Needle + filler[splice:]
	return Haystack{Text: text, NeedleOffset: splice}
}
