package niah

import (
	"strings"
	"testing"
)

// TestGenerateDeterministic — same seed + opts → byte-identical haystack.
// This is the load-bearing test of the package: every other test assumes
// that re-running the same flags reproduces the same haystack, so a CI
// failure here is reproducible by anyone.
func TestGenerateDeterministic(t *testing.T) {
	opts := GenerateOpts{
		Length: 4096,
		Depth:  0.5,
		Needle: "The secret recipe code is 4F-9X-2B.",
		Seed:   42,
	}
	a := Generate(opts)
	b := Generate(opts)
	if a.Text != b.Text {
		t.Fatalf("Generate is non-deterministic for seed=42:\nlen(a)=%d len(b)=%d", len(a.Text), len(b.Text))
	}
	if a.NeedleOffset != b.NeedleOffset {
		t.Fatalf("NeedleOffset diverges: a=%d b=%d", a.NeedleOffset, b.NeedleOffset)
	}
}

// TestGenerateDifferentSeeds — different seeds give different filler.
// Without this guarantee, --seed would be a no-op and noise injection
// across runs would collapse to a single trace.
func TestGenerateDifferentSeeds(t *testing.T) {
	base := GenerateOpts{
		Length: 4096,
		Depth:  0.5,
		Needle: "needle-X",
		Seed:   1,
	}
	a := Generate(base)
	other := base
	other.Seed = 2
	b := Generate(other)
	if a.Text == b.Text {
		t.Fatalf("seeds 1 and 2 produced identical haystacks (filler not seeded)")
	}
}

// TestGenerateNeedlePresent — needle substring is in the output and
// the recorded offset points at it.
func TestGenerateNeedlePresent(t *testing.T) {
	needle := "The secret recipe code is 4F-9X-2B."
	cases := []struct {
		name  string
		depth float64
	}{
		{"start", 0.0},
		{"middle", 0.5},
		{"end", 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Generate(GenerateOpts{
				Length: 4096,
				Depth:  tc.depth,
				Needle: needle,
				Seed:   7,
			})
			if !strings.Contains(h.Text, needle) {
				t.Fatalf("needle missing from haystack at depth %.2f", tc.depth)
			}
			if h.NeedleOffset < 0 || h.NeedleOffset+len(needle) > len(h.Text) {
				t.Fatalf("NeedleOffset=%d out of range [0,%d)", h.NeedleOffset, len(h.Text))
			}
			if got := h.Text[h.NeedleOffset : h.NeedleOffset+len(needle)]; got != needle {
				t.Fatalf("NeedleOffset points at %q, want %q", got, needle)
			}
		})
	}
}

// TestGenerateDepthPlacement — the needle's offset tracks the requested
// depth fraction. 0.0 lands at the start; 1.0 lands at the end (within
// the needle's own length); 0.5 lands somewhere in the middle.
func TestGenerateDepthPlacement(t *testing.T) {
	needle := "NEEDLE"
	opts := GenerateOpts{
		Length: 4096,
		Needle: needle,
		Seed:   3,
	}
	opts.Depth = 0.0
	start := Generate(opts)
	if start.NeedleOffset != 0 {
		t.Errorf("depth=0.0 NeedleOffset=%d, want 0", start.NeedleOffset)
	}

	opts.Depth = 1.0
	end := Generate(opts)
	tail := len(end.Text) - len(needle)
	if end.NeedleOffset != tail {
		t.Errorf("depth=1.0 NeedleOffset=%d, want %d (text len %d, needle len %d)",
			end.NeedleOffset, tail, len(end.Text), len(needle))
	}

	opts.Depth = 0.5
	mid := Generate(opts)
	if mid.NeedleOffset == 0 || mid.NeedleOffset == tail {
		t.Errorf("depth=0.5 NeedleOffset=%d collapsed to an endpoint", mid.NeedleOffset)
	}
}

// TestGenerateLengthApprox — the haystack is roughly Length*4 chars
// (the brief's byte-approximation for token count). Exact-equal is not
// required because the needle's substitution may shift bytes by ±len(needle),
// but the order of magnitude must be right or downstream depth math drifts.
func TestGenerateLengthApprox(t *testing.T) {
	cases := []int{1024, 4096, 8192}
	for _, L := range cases {
		h := Generate(GenerateOpts{
			Length: L,
			Depth:  0.5,
			Needle: "n",
			Seed:   1,
		})
		want := L * 4
		// Allow ±10% slack: filler quantization to phrase boundaries
		// would otherwise force exact-fit padding.
		min := want - want/10
		max := want + want/10
		if len(h.Text) < min || len(h.Text) > max {
			t.Errorf("Length=%d → text=%d chars, want in [%d,%d]",
				L, len(h.Text), min, max)
		}
	}
}

// TestGenerateZeroLengthIsNeedleOnly — Length=0 is the degenerate case
// (no filler). The needle alone should appear, and a depth fraction is
// meaningless against an empty filler. Used for unit-level isolation
// when callers want to test "pure" search without any noise.
func TestGenerateZeroLengthIsNeedleOnly(t *testing.T) {
	needle := "JUST-THE-NEEDLE"
	h := Generate(GenerateOpts{Length: 0, Depth: 0.5, Needle: needle, Seed: 1})
	if h.Text != needle {
		t.Fatalf("Length=0 should yield needle only; got %q", h.Text)
	}
	if h.NeedleOffset != 0 {
		t.Fatalf("Length=0 NeedleOffset=%d, want 0", h.NeedleOffset)
	}
}
