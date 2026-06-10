package study

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"path/filepath"
)

// Byte-grid producer: lays a deterministic chunk grid over ONE file's
// byte space from its size alone — it never opens the file to chunk it
// (the only IO is the caller's os.Stat). This is the net-new boundary
// producer for the "single huge file" target: a GB file and a KB file
// at the same window cost the same to grid.
//
// Line bounds and EffLines are PROVISIONAL here (estimated from an
// average bytes-per-line); RefineChunk fills the real values on first
// visit once a region is actually read. Chunk IDs hash byte
// coordinates, not line bounds, so identity survives that refinement.
//
// The grid reuses the existing flat Module/Chunk model so
// HierarchicalSampler.Next runs unchanged: chunks are partitioned into
// contiguous "bands" that play the role of modules, and the sampler's
// per-module anti-coverage bias then spreads draws across the whole
// file instead of clumping in one region.
const (
	byteGridMinChunkBytes = 1024 // floor admits the prose coherence unit (see boundary.go)
	byteGridMaxChunkBytes = 256 * 1024
	byteGridDefaultBands  = 16
	byteGridDefaultFill   = 0.125 // window-derived fallback for formats with no coherence unit
)

// ByteGridOpts configures the byte-grid producer. WindowTokens is the
// consuming model's context window (tokens); it sets the target chunk
// size. Zero values fall back to sensible defaults.
type ByteGridOpts struct {
	WindowTokens int     // consuming-model window in tokens; default studyDefaultCtxWindow
	TargetFill   float64 // explicit per-chunk window fraction; 0 → the format's coherence unit, then window/8
	Bands        int     // synthetic module count for anti-coverage spread; default 16
	Salt         string  // mixed into the RNG seed
	ModTimeUnix  int64   // file mtime; folds into the drift key + state hash
}

// BuildByteGrid lays the grid for a single file from its size alone and
// returns a BoundaryOutput the existing sampler consumes directly.
func BuildByteGrid(absPath, relPath string, size int64, opts ByteGridOpts) *BoundaryOutput {
	out := &BoundaryOutput{
		ProjectRoot: absPath,
		TotalFiles:  1,
		FileHashes:  map[string]string{},
	}
	out.StateHash = byteGridStateHash(relPath, size, opts.ModTimeUnix)
	out.RNGSeed = seedFrom(out.StateHash, opts.Salt)
	out.FileHashes[relPath] = byteGridDriftKey(size, opts.ModTimeUnix)
	if size <= 0 {
		return out
	}

	winTokens := opts.WindowTokens
	if winTokens <= 0 {
		winTokens = studyDefaultCtxWindow
	}
	lang := langFor(filepath.Ext(relPath))

	// Chunk-size targeting, most-specific first: an explicit TargetFill
	// wins; otherwise the format's coherence unit (boundary.go); only
	// formats with no known unit fall back to the window-derived 1/8.
	var target int
	if opts.TargetFill > 0 && opts.TargetFill <= 1 {
		target = int(float64(winTokens) * studyCharsPerToken * opts.TargetFill)
	} else if u := unitBytesFor(lang); u > 0 {
		target = u
	} else {
		target = int(float64(winTokens) * studyCharsPerToken * byteGridDefaultFill)
	}
	if target < byteGridMinChunkBytes {
		target = byteGridMinChunkBytes
	}
	if target > byteGridMaxChunkBytes {
		target = byteGridMaxChunkBytes
	}

	// Ceil-divide so the final chunk picks up the remainder.
	n := int((size + int64(target) - 1) / int64(target))
	if n < 1 {
		n = 1
	}
	bands := opts.Bands
	if bands <= 0 {
		bands = byteGridDefaultBands
	}
	if bands > n {
		bands = n
	}

	chunks := make([]Chunk, 0, n)
	for i := 0; i < n; i++ {
		off := int64(i) * int64(target)
		end := off + int64(target)
		if end > size {
			end = size
		}
		length := int(end - off)
		if length <= 0 {
			break
		}
		band := i * bands / n
		eff := length / studyCharsPerLine
		if eff < 1 {
			eff = 1
		}
		chunks = append(chunks, Chunk{
			ID:         byteChunkID(relPath, off, length),
			Path:       absPath,
			RelPath:    relPath,
			LineStart:  int(off/studyCharsPerLine) + 1,     // provisional
			LineEnd:    int((end-1)/studyCharsPerLine) + 1, // provisional
			ByteOffset: off,
			ByteLength: length,
			EffLines:   eff, // provisional
			EstTokens:  length / studyCharsPerToken,
			ModuleID:   fmt.Sprintf("band-%02d", band),
			Lang:       lang,
		})
	}

	// Roll chunks up into band modules. The sampler groups by ModuleID
	// itself, so these are for coherence/coverage bookkeeping; chunks
	// stay in byte order (== band order).
	modIdx := map[string]int{}
	var modules []Module
	effTotal := 0
	for _, c := range chunks {
		effTotal += c.EffLines
		mi, ok := modIdx[c.ModuleID]
		if !ok {
			mi = len(modules)
			modIdx[c.ModuleID] = mi
			modules = append(modules, Module{ID: c.ModuleID, RootPath: absPath, Files: 1})
		}
		modules[mi].ChunkIDs = append(modules[mi].ChunkIDs, c.ID)
		modules[mi].EffLines += c.EffLines
		modules[mi].Lines += c.EffLines
	}

	out.Chunks = chunks
	out.Modules = modules
	out.EffTotalLines = effTotal
	out.TotalLines = effTotal
	return out
}

// byteChunkID is the stable identifier for a byte-grid chunk. It hashes
// byte coordinates (not line bounds) and is prefixed "b" so it can
// never alias a line-based chunkID for the same file.
func byteChunkID(relPath string, off int64, length int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%d", relPath, off, length)
	return "b" + hex.EncodeToString(h.Sum(nil))[:15]
}

// byteGridStateHash is the determinism key for the grid: same file
// (relpath + size + mtime) → same hash → same RNG seed → same chunks.
func byteGridStateHash(relPath string, size, mtimeUnix int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%d", relPath, size, mtimeUnix)
	return hex.EncodeToString(h.Sum(nil))
}

// byteGridDriftKey is the cheap drift key stored in FileHashes. Unlike
// the universal analyzer's content sha256, this is a size+mtime
// composite so resuming a study never has to read the whole file. A
// content edit that preserves both size and mtime is missed — rare
// enough to accept for v1; a streamed content hash can opt in later.
func byteGridDriftKey(size, mtimeUnix int64) string {
	return fmt.Sprintf("%d:%d", size, mtimeUnix)
}

// seedFrom derives a deterministic RNG seed from a state hash + salt.
func seedFrom(stateHash, salt string) int64 {
	h := fnv.New64a()
	h.Write([]byte(stateHash))
	h.Write([]byte(salt))
	return int64(h.Sum64())
}
