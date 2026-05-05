package fractal

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickRegions_SmallFile(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	regs := PickRegions(1024, 4, rng)
	if len(regs) != 1 {
		t.Fatalf("small file should produce 1 region, got %d", len(regs))
	}
	if regs[0].Offset != 0 || regs[0].Length != 1024 {
		t.Errorf("expected whole-file region, got %+v", regs[0])
	}
}

func TestPickRegions_NonOverlapping(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	regs := PickRegions(200*1024, 5, rng)
	if len(regs) == 0 {
		t.Fatal("expected at least one region")
	}
	for i := 0; i < len(regs); i++ {
		for j := i + 1; j < len(regs); j++ {
			a, b := regs[i], regs[j]
			ae := a.Offset + int64(a.Length)
			be := b.Offset + int64(b.Length)
			if a.Offset < be && b.Offset < ae {
				t.Errorf("regions %+v and %+v overlap", a, b)
			}
		}
	}
	for _, r := range regs {
		if r.Length < MinWindowChars || r.Length > MaxWindowChars {
			t.Errorf("region length %d out of bounds [%d,%d]", r.Length, MinWindowChars, MaxWindowChars)
		}
	}
}

func TestPickRegions_HeadInclusionFraction(t *testing.T) {
	const trials = 500
	headHits := 0
	for s := int64(1); s <= trials; s++ {
		rng := rand.New(rand.NewSource(s))
		regs := PickRegions(200*1024, 3, rng)
		for _, r := range regs {
			if r.Offset == 0 {
				headHits++
				break
			}
		}
	}
	// Across `count=3` rolls per trial, P(head appears at least once) ~
	// 1 - (1 - 1/3)^3 ≈ 0.70. Allow a wide tolerance.
	pct := float64(headHits) / float64(trials)
	if pct < 0.40 || pct > 0.95 {
		t.Errorf("head inclusion rate out of range: %.2f", pct)
	}
}

func TestNeighborRegions_Clamping(t *testing.T) {
	r := Region{Path: "x", Offset: 0, Length: 4096}
	got := NeighborRegions(r, 100*1024)
	if len(got) == 0 {
		t.Fatal("expected at least one neighbor")
	}
	for _, n := range got {
		if n.Offset < 0 || n.Offset+int64(n.Length) > 100*1024 {
			t.Errorf("neighbor %+v out of bounds", n)
		}
		if n.Length != r.Length {
			t.Errorf("neighbor length should equal parent length")
		}
	}
}

func TestNeighborRegions_NoSelfReturn(t *testing.T) {
	r := Region{Path: "x", Offset: 8192, Length: 4096}
	for _, n := range NeighborRegions(r, 64*1024) {
		if n.Offset == r.Offset {
			t.Errorf("neighbor must not equal source offset")
		}
	}
}

func TestReadRegion_RuneBoundaryClamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	// Mix of ASCII and a 3-byte rune at a crossing point.
	body := strings.Repeat("a", 1000) + "日本語" + strings.Repeat("b", 1000)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read a window straddling the multi-byte chars.
	got, err := ReadRegion(path, 999, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("empty region")
	}
	// Length is allowed to be < 20 because of clamping; just ensure no
	// invalid runes leaked through.
	for _, r := range got {
		if r == 0xFFFD {
			t.Errorf("replacement rune leaked: %q", got)
		}
	}
}
