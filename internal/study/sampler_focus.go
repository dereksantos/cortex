package study

import (
	"bufio"
	"math/rand"
	"os"
)

// FocusSampler wraps any Sampler and biases its draws toward chunks
// whose line range intersects a focus region — the net-new knob that
// makes DENSIFY/TARGET deepening land where the agent asked. It does
// NOT touch the inner sampler or the Sampler interface: each pick is
// delegated to Inner, so anti-coverage and determinism are preserved;
// the bias is applied by hiding out-of-focus chunks (marking them
// covered) on biased picks. When the in-focus set is exhausted it falls
// back to a normal draw, so it never returns fewer chunks than Inner
// would.
//
// in-focus membership is computed once from the grid's line bounds
// (provisional pre-refinement bounds are close enough to steer the
// draw; refinement later corrects the exact range).
type FocusSampler struct {
	Inner   Sampler
	Bias    float64 // P(a pick is restricted to in-focus chunks); default 0.7
	inFocus map[string]bool
}

// FocusSpec is a resolved focus: a normalized line range and, when the
// underlying file is readable, the exact byte range those lines span.
// Byte resolution is what makes focus-by-line accurate even when the
// file's real bytes-per-line differs from the byte grid's estimate.
type FocusSpec struct {
	Lines [2]int
	Bytes [2]int64 // exact byte span of Lines; [0,0] when unresolved
}

const focusDefaultBias = 0.7

// newFocusSampler precomputes the in-focus chunk set for out and returns
// a FocusSampler. An unresolved/empty focus yields an empty in-focus set,
// in which case the sampler is a pass-through to Inner.
//
// Membership prefers the resolved BYTE range (accurate); it falls back
// to the byte grid's provisional line bounds only when the file can't be
// read (e.g. synthetic grids in tests).
func newFocusSampler(inner Sampler, out *BoundaryOutput, f Focus) *FocusSampler {
	fs := &FocusSampler{Inner: inner, Bias: focusDefaultBias, inFocus: map[string]bool{}}
	spec, _ := ResolveFocus(out.ProjectRoot, f)

	if spec.Bytes[1] > spec.Bytes[0] {
		bs, be := spec.Bytes[0], spec.Bytes[1]
		for _, c := range out.Chunks {
			cs, ce := c.ByteOffset, c.ByteOffset+int64(c.ByteLength)
			if cs < be && ce > bs {
				fs.inFocus[c.ID] = true
			}
		}
		return fs
	}

	lo, hi := spec.Lines[0], spec.Lines[1]
	if lo == 0 && hi == 0 {
		return fs
	}
	for _, c := range out.Chunks {
		if c.LineStart <= hi && c.LineEnd >= lo {
			fs.inFocus[c.ID] = true
		}
	}
	return fs
}

// Name identifies the sampler in study metadata.
func (f *FocusSampler) Name() string { return f.Inner.Name() + "+focus" }

// Next draws up to k chunk IDs, biasing toward in-focus chunks.
func (f *FocusSampler) Next(out *BoundaryOutput, covered map[string]bool, k int, rng *rand.Rand) []string {
	if len(f.inFocus) == 0 {
		return f.Inner.Next(out, covered, k, rng)
	}

	// work = covered ∪ already-picked; mutated as we go so the inner
	// single-draws never repeat.
	work := make(map[string]bool, len(covered))
	for id := range covered {
		work[id] = true
	}

	picked := make([]string, 0, k)
	for i := 0; i < k; i++ {
		var id string
		if rng.Float64() < f.Bias {
			// Restrict to in-focus: hide every out-of-focus chunk.
			tmp := make(map[string]bool, len(work)+len(out.Chunks))
			for cid := range work {
				tmp[cid] = true
			}
			for _, c := range out.Chunks {
				if !f.inFocus[c.ID] {
					tmp[c.ID] = true
				}
			}
			if got := f.Inner.Next(out, tmp, 1, rng); len(got) > 0 {
				id = got[0]
			}
		}
		if id == "" {
			// Non-biased pick, or in-focus exhausted → normal draw.
			if got := f.Inner.Next(out, work, 1, rng); len(got) > 0 {
				id = got[0]
			}
		}
		if id == "" {
			break
		}
		picked = append(picked, id)
		work[id] = true
	}
	return picked
}

// ResolveFocus turns a Focus request into a concrete line range. A line
// range passes through (normalized so Lines[0] <= Lines[1]). Symbol and
// Query resolution is layered on later — AST (go/ast) for precise
// symbol spans, streaming grep otherwise — and currently yields an empty
// spec, which makes the focus sampler a no-op pass-through.
func ResolveFocus(path string, f Focus) (FocusSpec, error) {
	if f.Lines[0] > 0 || f.Lines[1] > 0 {
		lo, hi := f.Lines[0], f.Lines[1]
		if hi < lo {
			lo, hi = hi, lo
		}
		spec := FocusSpec{Lines: [2]int{lo, hi}}
		if path != "" {
			if sb, eb, err := resolveLineRangeToBytes(path, lo, hi); err == nil {
				spec.Bytes = [2]int64{sb, eb}
			}
		}
		return spec, nil
	}
	// TODO(study): resolve Symbol via go/ast and Query via streaming grep
	// into a line range. Until then, unresolved focus is an empty spec.
	return FocusSpec{}, nil
}

// resolveLineRangeToBytes scans path once, returning the byte offset
// where line lo begins and the byte offset where line hi ends (the start
// of line hi+1, or EOF). O(bytes-to-line-hi) sequential IO. Lines are
// 1-indexed; lo/hi past EOF clamp to the file size.
func resolveLineRangeToBytes(path string, lo, hi int) (int64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64*1024)

	var pos int64
	line := 1
	startByte, endByte := int64(-1), int64(-1)
	if lo <= 1 {
		startByte = 0
	}
	for startByte < 0 || endByte < 0 {
		b, rerr := br.ReadByte()
		if rerr != nil {
			break
		}
		pos++ // pos is now the byte offset just AFTER b
		if b == '\n' {
			line++
			if line == lo && startByte < 0 {
				startByte = pos
			}
			if line == hi+1 && endByte < 0 {
				endByte = pos
			}
		}
	}
	size := pos // when a marker wasn't hit, the scan ran to EOF, so pos == size
	if startByte < 0 {
		startByte = size
	}
	if endByte < 0 {
		endByte = size
	}
	return startByte, endByte, nil
}
