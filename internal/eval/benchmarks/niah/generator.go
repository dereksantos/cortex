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

// FillerMode selects the corpus used to pad the haystack around the
// needle. The choice determines whether NIAH exercises retrieval
// *quality* (adversarial: filler competes with the probe) or just
// the pipeline *shape* (lorem: no probe overlap, needle wins
// trivially).
type FillerMode string

const (
	// FillerAdversarial — phrases share words with the default probe
	// terms ("secret", "recipe", "code"). Multiple chunks become
	// candidates at retrieval time, so the scorer has to discriminate
	// the needle from look-alike noise. This is the default because
	// it produces meaningful regression signal; lorem-only mode is
	// the easy-mode escape hatch.
	FillerAdversarial FillerMode = "adversarial"

	// FillerLorem — phrases drawn from a clean corpus with no overlap
	// with the default probe. Use this when triaging a regression to
	// rule out the substrate (capture / ingest / chunking) before
	// touching the scorer.
	FillerLorem FillerMode = "lorem"
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

	// Seed determines the deterministic shuffle of the filler corpus.
	// Same Seed + same other opts → byte-identical Haystack.Text.
	Seed int64

	// FillerMode selects the corpus. Zero value (empty string)
	// resolves to FillerAdversarial — the meaningful default.
	FillerMode FillerMode
}

// Haystack is the synthesized text plus the byte offset where the
// needle was placed. Callers use NeedleOffset for debugging missed
// retrievals (e.g. "needle was at offset 24576 of 65536, which falls
// in chunk 12 of 32; why didn't chunk 12's capture surface?").
type Haystack struct {
	Text         string
	NeedleOffset int
}

// loremCorpus is a clean filler with NO overlap with the default probe
// terms ("secret", "recipe", "code"). Used for triaging: if a
// regression reproduces with FillerLorem, the substrate (capture /
// ingest / chunking) is broken; if it only shows with FillerAdversarial,
// the scorer/ranker is the suspect.
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

// adversarialCorpus is the default filler. Every phrase contains at
// least one of the default probe terms (secret, recipe, code) so that
// many chunks become candidates at retrieval time. The retriever now
// has to discriminate the needle ("The secret recipe code is X") from
// noisy chunks that share 1–2 of the same terms — which is exactly
// the workload Cortex faces in production. A scorer regression that
// gives equal weight to partial-match chunks would push the needle
// out of position 1; NIAH catches that.
//
// Crucially, no phrase here is the literal needle. A future-needle
// drift that accidentally matches a corpus phrase would defeat the
// substring check; the lint-ish test in filler_test.go is the guard.
var adversarialCorpus = []string{
	"the team agreed to keep this decision a secret until the launch. ",
	"a backup recipe is filed under the standard contingency folder. ",
	"the code review pointed out three small wins for next sprint. ",
	"sharing a secret with the wider org defeats the purpose. ",
	"a great recipe is half technique and half ingredient quality. ",
	"this code path is only exercised by the integration suite. ",
	"the secret to good operations is boring, predictable systems. ",
	"a recipe for disaster: optional dependencies between services. ",
	"writing code for humans first means avoiding clever shortcuts. ",
	"every secret handshake protocol adds operational fragility. ",
	"a recipe card pinned to the wall outlives any wiki article. ",
	"the code freeze starts tomorrow at noon local time, no exceptions. ",
	"some recipes call for patience that the calendar does not allow. ",
	"the team's secret weapon is just disciplined incident review. ",
	"a recipe for trust: do what you said you would, on time. ",
	"this code base rewards readers more than it rewards writers. ",
	"keeping a secret in a distributed system is harder than it looks. ",
	"a recipe is easier to copy than the judgment behind it. ",
	"the code owner field is a polite fiction in this monorepo. ",
	"a quiet secret about caching: invalidation is the only hard part. ",
}

// fillerCorpus returns the phrase slice for a given mode. The empty
// mode resolves to FillerAdversarial — see GenerateOpts.FillerMode.
func fillerCorpus(mode FillerMode) []string {
	switch mode {
	case FillerLorem:
		return loremCorpus
	case FillerAdversarial, "":
		return adversarialCorpus
	default:
		return adversarialCorpus
	}
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
	corpus := fillerCorpus(opts.FillerMode)

	var b strings.Builder
	b.Grow(targetBytes + len(opts.Needle))
	for b.Len() < targetBytes {
		phrase := corpus[r.Intn(len(corpus))]
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
