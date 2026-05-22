package study

import (
	"math/rand"
	"strings"
	"testing"
)

// synthBoundary builds a BoundaryOutput with N modules × M chunks each,
// every chunk having ChunkLines effective lines. Deterministic given
// the input parameters.
func synthBoundary(modules, chunksPerMod, effLinesPerChunk int) *BoundaryOutput {
	out := &BoundaryOutput{
		ProjectRoot:   "/synth",
		RNGSeed:       42,
		StateHash:     "synthetic",
		EffTotalLines: modules * chunksPerMod * effLinesPerChunk,
		TotalLines:    modules * chunksPerMod * effLinesPerChunk,
		TotalFiles:    modules * chunksPerMod,
	}
	for m := 0; m < modules; m++ {
		mid := nameOf("mod", m)
		mod := Module{ID: mid, RootPath: "/synth/" + mid}
		for c := 0; c < chunksPerMod; c++ {
			ch := Chunk{
				ID:         nameOf(mid+"-chunk", c),
				Path:       "/synth/" + mid + "/file_" + nameOf("f", c) + ".go",
				RelPath:    mid + "/file_" + nameOf("f", c) + ".go",
				LineStart:  1,
				LineEnd:    effLinesPerChunk,
				ByteOffset: 0,
				ByteLength: effLinesPerChunk * 10,
				EffLines:   effLinesPerChunk,
				EstTokens:  effLinesPerChunk * 2,
				ModuleID:   mid,
				Lang:       "go",
			}
			out.Chunks = append(out.Chunks, ch)
			mod.ChunkIDs = append(mod.ChunkIDs, ch.ID)
			mod.EffLines += effLinesPerChunk
			mod.Lines += effLinesPerChunk
			mod.Files++
		}
		out.Modules = append(out.Modules, mod)
	}
	return out
}

func nameOf(prefix string, i int) string {
	return prefix + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestSampler_DeterministicSequence(t *testing.T) {
	out := synthBoundary(5, 10, 20)
	covered := map[string]bool{}

	s := &HierarchicalSampler{}
	var seq1, seq2 []string

	rng1 := rand.New(rand.NewSource(42))
	for i := 0; i < 10; i++ {
		seq1 = append(seq1, s.Next(out, covered, 4, rng1)...)
	}
	rng2 := rand.New(rand.NewSource(42))
	for i := 0; i < 10; i++ {
		seq2 = append(seq2, s.Next(out, covered, 4, rng2)...)
	}

	if len(seq1) != len(seq2) {
		t.Fatalf("sequences differ in length: %d vs %d", len(seq1), len(seq2))
	}
	for i := range seq1 {
		if seq1[i] != seq2[i] {
			t.Errorf("sequence drift at %d: %q vs %q", i, seq1[i], seq2[i])
		}
	}
}

func TestSampler_NoRepeatInSingleCall(t *testing.T) {
	out := synthBoundary(2, 5, 10)
	covered := map[string]bool{}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(7))

	picks := s.Next(out, covered, 6, rng)
	seen := map[string]bool{}
	for _, id := range picks {
		if seen[id] {
			t.Errorf("duplicate in single Next() call: %s", id)
		}
		seen[id] = true
	}
}

func TestSampler_RespectsCovered(t *testing.T) {
	out := synthBoundary(3, 4, 10)
	// Mark all of mod-0's chunks as covered.
	covered := map[string]bool{}
	for _, c := range out.Chunks {
		if c.ModuleID == "mod-0" {
			covered[c.ID] = true
		}
	}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(1))
	// Draw many times; mod-0 chunks must NEVER appear.
	for i := 0; i < 50; i++ {
		picks := s.Next(out, covered, 4, rng)
		for _, id := range picks {
			if strings.HasPrefix(id, "mod-0-chunk-") {
				t.Errorf("covered module-0 chunk drawn: %s", id)
			}
		}
	}
}

func TestSampler_AllCoveredReturnsEmpty(t *testing.T) {
	out := synthBoundary(2, 3, 5)
	covered := map[string]bool{}
	for _, c := range out.Chunks {
		covered[c.ID] = true
	}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(0))
	picks := s.Next(out, covered, 5, rng)
	if len(picks) != 0 {
		t.Errorf("expected 0 picks when all covered, got %d", len(picks))
	}
}

// TestSampler_AntiCoverageBias verifies that over many draws, a
// module with more uncovered chunks gets sampled more often than one
// with fewer. Stochastic test — use a fixed seed and a generous
// margin so it's reliable.
func TestSampler_AntiCoverageBias(t *testing.T) {
	out := synthBoundary(2, 10, 10) // 2 modules × 10 chunks
	// Pre-cover 9/10 chunks of mod-0, 0/10 of mod-1.
	covered := map[string]bool{}
	for _, c := range out.Chunks {
		if c.ModuleID == "mod-0" && strings.HasSuffix(c.ID, "mod-0-chunk-0") {
			continue
		}
		if c.ModuleID == "mod-0" {
			covered[c.ID] = true
		}
	}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(99))

	counts := map[string]int{}
	totalPicks := 0
	for i := 0; i < 200; i++ {
		// Reset covered each iteration (only the pre-set state) by
		// using a fresh copy, since we don't want this call's drawn
		// set to leak between iterations.
		c := make(map[string]bool, len(covered))
		for k, v := range covered {
			c[k] = v
		}
		picks := s.Next(out, c, 1, rng)
		for _, id := range picks {
			counts[mod(id)]++
			totalPicks++
		}
	}
	if counts["mod-1"] <= counts["mod-0"] {
		t.Errorf("mod-1 (10 uncovered) should win more often than mod-0 (1 uncovered); got mod-0=%d mod-1=%d",
			counts["mod-0"], counts["mod-1"])
	}
}

// mod extracts the module prefix from a synthetic chunk ID like
// "mod-1-chunk-3".
func mod(chunkID string) string {
	// IDs are "mod-N-chunk-M"; we want "mod-N".
	parts := strings.SplitN(chunkID, "-chunk-", 2)
	if len(parts) == 0 {
		return chunkID
	}
	return parts[0]
}

func TestSampler_Name(t *testing.T) {
	s := &HierarchicalSampler{}
	if name := s.Name(); name != "hierarchical-v1" {
		t.Errorf("Name = %q, want hierarchical-v1", name)
	}
}

func TestSampler_EmptyBoundary(t *testing.T) {
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(0))
	out := &BoundaryOutput{}
	if picks := s.Next(out, nil, 4, rng); len(picks) != 0 {
		t.Errorf("empty boundary picks = %d, want 0", len(picks))
	}
	if picks := s.Next(nil, nil, 4, rng); len(picks) != 0 {
		t.Errorf("nil boundary picks = %d, want 0", len(picks))
	}
}

func TestSampler_BatchHigherThanAvailable(t *testing.T) {
	out := synthBoundary(1, 3, 5) // 3 total chunks
	covered := map[string]bool{}
	s := &HierarchicalSampler{}
	rng := rand.New(rand.NewSource(5))
	picks := s.Next(out, covered, 10, rng)
	if len(picks) != 3 {
		t.Errorf("expected 3 picks (all available), got %d", len(picks))
	}
}
