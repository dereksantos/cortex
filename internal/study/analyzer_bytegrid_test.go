package study

import (
	"math/rand"
	"testing"
)

func TestBuildByteGrid_CoversByteSpaceContiguously(t *testing.T) {
	size := int64(200000)
	out := BuildByteGrid("/abs/f.go", "f.go", size, ByteGridOpts{WindowTokens: 8192})
	if len(out.Chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	var prevEnd int64
	for i, c := range out.Chunks {
		if c.ByteOffset != prevEnd {
			t.Errorf("chunk %d ByteOffset=%d, want %d (gap or overlap)", i, c.ByteOffset, prevEnd)
		}
		if c.ByteLength <= 0 {
			t.Errorf("chunk %d ByteLength=%d, want > 0", i, c.ByteLength)
		}
		prevEnd = c.ByteOffset + int64(c.ByteLength)
	}
	if prevEnd != size {
		t.Errorf("last chunk ends at byte %d, want %d (full coverage)", prevEnd, size)
	}
}

func TestBuildByteGrid_ChunkCountScalesWithSize(t *testing.T) {
	opts := ByteGridOpts{WindowTokens: 8192}
	a := BuildByteGrid("/a", "a", 100000, opts)
	b := BuildByteGrid("/a", "a", 200000, opts)
	// 2x the bytes at the same density → roughly 2x the chunks (±1 for ceil).
	if got, lo, hi := len(b.Chunks), 2*len(a.Chunks)-1, 2*len(a.Chunks)+1; got < lo || got > hi {
		t.Errorf("doubling size: a=%d chunks, b=%d chunks; want b in [%d,%d]", len(a.Chunks), got, lo, hi)
	}
}

func TestBuildByteGrid_BandsSpreadModules(t *testing.T) {
	out := BuildByteGrid("/a", "a", 500000, ByteGridOpts{WindowTokens: 8192, Bands: 8})
	counts := map[string]int{}
	for _, c := range out.Chunks {
		counts[c.ModuleID]++
	}
	if len(counts) < 2 {
		t.Fatalf("expected the byte space spread across multiple bands, got %d", len(counts))
	}
	if len(counts) > 8 {
		t.Errorf("more bands than configured: got %d, want <= 8", len(counts))
	}
	// Bands are zero-padded (band-00, band-01, ...) so lexical order ==
	// byte order; the module IDs must be monotonic non-decreasing across
	// the byte-ordered chunk slice (contiguous bands).
	last := ""
	for i, c := range out.Chunks {
		if last != "" && c.ModuleID < last {
			t.Errorf("chunk %d band %q precedes previous band %q — bands not contiguous", i, c.ModuleID, last)
		}
		last = c.ModuleID
	}
}

func TestBuildByteGrid_Deterministic(t *testing.T) {
	o1 := BuildByteGrid("/a", "a", 123456, ByteGridOpts{WindowTokens: 8192, Salt: "s"})
	o2 := BuildByteGrid("/a", "a", 123456, ByteGridOpts{WindowTokens: 8192, Salt: "s"})
	if o1.RNGSeed != o2.RNGSeed {
		t.Errorf("RNGSeed not deterministic: %d vs %d", o1.RNGSeed, o2.RNGSeed)
	}
	if len(o1.Chunks) != len(o2.Chunks) {
		t.Fatalf("chunk count differs: %d vs %d", len(o1.Chunks), len(o2.Chunks))
	}
	for i := range o1.Chunks {
		if o1.Chunks[i].ID != o2.Chunks[i].ID {
			t.Errorf("chunk %d ID differs: %q vs %q", i, o1.Chunks[i].ID, o2.Chunks[i].ID)
		}
	}
}

func TestBuildByteGrid_IDsAreByteDerived(t *testing.T) {
	out := BuildByteGrid("/a", "rel/a.go", 50000, ByteGridOpts{WindowTokens: 8192})
	if len(out.Chunks) < 4 {
		t.Fatalf("want >= 4 chunks, got %d", len(out.Chunks))
	}
	c := out.Chunks[3]
	// The ID hashes byte coordinates, NOT line bounds — so refining the
	// provisional LineStart/LineEnd later must not change identity.
	want := byteChunkID(c.RelPath, c.ByteOffset, c.ByteLength)
	if c.ID != want {
		t.Errorf("chunk ID %q is not byte-derived (want %q)", c.ID, want)
	}
}

func TestBuildByteGrid_FeedsHierarchicalSampler(t *testing.T) {
	out := BuildByteGrid("/a", "a", 500000, ByteGridOpts{WindowTokens: 8192, Bands: 16})
	byID := map[string]Chunk{}
	for _, c := range out.Chunks {
		byID[c.ID] = c
	}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(42))
	picks := s.Next(out, map[string]bool{}, 8, rng)
	if len(picks) != 8 {
		t.Fatalf("HierarchicalSampler.Next returned %d picks, want 8", len(picks))
	}
	seen := map[string]bool{}
	bands := map[string]bool{}
	for _, id := range picks {
		if seen[id] {
			t.Errorf("duplicate pick %q", id)
		}
		seen[id] = true
		bands[byID[id].ModuleID] = true
	}
	if len(bands) < 2 {
		t.Errorf("expected sample spread across >= 2 bands, got %d", len(bands))
	}
}

func TestBuildByteGrid_TinyFileSingleChunk(t *testing.T) {
	out := BuildByteGrid("/a", "a", 100, ByteGridOpts{WindowTokens: 8192})
	if len(out.Chunks) != 1 {
		t.Fatalf("tiny file should yield 1 chunk, got %d", len(out.Chunks))
	}
	if out.Chunks[0].ByteLength != 100 {
		t.Errorf("single chunk ByteLength=%d, want 100", out.Chunks[0].ByteLength)
	}
}
