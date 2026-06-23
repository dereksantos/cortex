package study

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// Directory studies: the same size-adaptive identity as study_file, with
// a corpus's file tree as the boundary space instead of one file's byte
// space (docs/study-file.md "Boundary producers").
//
//	study(dir) =
//	    read all files, labelled        if Σ size(files) < window/2
//	    UniversalAnalyzer → sample →    otherwise
//	      infer (same pipeline)
//
// The sub-threshold branch is the comprehension win for small models:
// "study this package" inlines the whole package, each file labelled
// with its caller-relative path, so provenance survives concatenation.
// Over threshold, the universal analyzer carves the tree into modules +
// line-windowed chunks (REAL line bounds — nothing to lazily refine) and
// the shared sampler/inference/citation machinery runs unchanged.

// studyDir handles StudyFile requests whose Path is a directory.
func studyDir(ctx context.Context, req StudyRequest) (StudyResponse, error) {
	ignore := projectscan.LoadIgnoreSet(req.Path)
	// Uncapped walk: oversized files must count toward the threshold and
	// be studied (via byte grids below), not silently vanish.
	files, err := walkSourceFiles(req.Path, ignore, 0)
	if err != nil {
		return StudyResponse{}, err
	}

	window := req.Window
	if window <= 0 {
		window = studyDefaultCtxWindow
	}

	relBase := filepath.ToSlash(req.RelPath)
	if relBase == "." {
		relBase = ""
	}

	// Threshold: the whole corpus fits → inline it, one labelled section
	// per file in deterministic (sorted relpath) order. (An over-cap file
	// can only reach this branch when the window genuinely fits it.)
	var total int64
	for _, f := range files {
		total += f.size
	}
	if total/studyCharsPerToken < int64(window/2) {
		return StudyResponse{Mode: "read", ReadContent: readCorpus(files, relBase)}, nil
	}

	// Corpus boundary: the universal analyzer scoped to this directory
	// (its walk re-applies the reading cap), with every over-cap file
	// merged in as a per-file byte grid so huge files are sampled like
	// any other region instead of being invisible.
	an := UniversalAnalyzer{Salt: req.Session}
	out, err := an.Analyze(ctx, req.Path, ignore)
	if err != nil {
		return StudyResponse{}, fmt.Errorf("study: analyze %s: %w", req.Path, err)
	}
	var large []sourceFile
	for _, f := range files {
		if f.size > universalMaxFileBytes {
			large = append(large, f)
		}
	}
	// Scope to the root module: nested modules (dirs with their own
	// language-root marker like go.mod) are separate projects — exclude
	// them so studying the repo root doesn't sample eval fixtures or
	// vendored subprojects. Marker-less subdirs (src/, cmd/, internal/)
	// are part of the root module's tree and are kept.
	large = scopeToRootModule(out, req.Path, large)
	mergeLargeFiles(out, large, window, req.Fill, req.Session)

	// Citations must be meaningful to the CALLER: chunk relpaths are
	// analyzer-relative (to the studied dir), so prefix them with the
	// dir's own caller-relative path. Chunk IDs hash the unprefixed
	// relpath, so session coverage is independent of the caller's base.
	if relBase != "" {
		for i := range out.Chunks {
			out.Chunks[i].RelPath = joinRel(relBase, out.Chunks[i].RelPath)
		}
	}

	display := relBase
	if display == "" {
		display = filepath.Base(req.Path)
	}
	return sampleAndInfer(ctx, req, out, display, meanChunkBytes(out.Chunks), window)
}

// mergeLargeFiles folds per-file byte grids for over-cap files into the
// corpus boundary, so the dir study sees ALL its content: the universal
// analyzer reads + line-chunks everything up to its cap, and anything
// bigger is gridded from size alone (the byte grid never reads the file
// to chunk it) and sampled like any other region. Band modules are
// namespaced by relpath — every grid emits band-00..band-NN and two
// large files must not share coverage groups. The state hash + RNG seed
// are re-derived so large-file drift changes the draw deterministically.
func mergeLargeFiles(out *BoundaryOutput, large []sourceFile, window int, fill float64, salt string) {
	if len(large) == 0 {
		return
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", out.StateHash)
	for _, f := range large {
		grid := BuildByteGrid(f.abs, f.rel, f.size, ByteGridOpts{
			WindowTokens: window,
			TargetFill:   fill,
			Salt:         salt,
			ModTimeUnix:  f.mtime,
		})
		for i := range grid.Chunks {
			grid.Chunks[i].ModuleID = f.rel + "#" + grid.Chunks[i].ModuleID
		}
		for i := range grid.Modules {
			grid.Modules[i].ID = f.rel + "#" + grid.Modules[i].ID
		}
		out.Chunks = append(out.Chunks, grid.Chunks...)
		out.Modules = append(out.Modules, grid.Modules...)
		out.EffTotalLines += grid.EffTotalLines
		out.TotalLines += grid.TotalLines
		out.TotalFiles++
		out.FileHashes[f.rel] = byteGridDriftKey(f.size, f.mtime)
		fmt.Fprintf(h, "%s:%d:%d\n", f.rel, f.size, f.mtime)
	}
	out.StateHash = hex.EncodeToString(h.Sum(nil))
	out.RNGSeed = seedFrom(out.StateHash, salt)
}

// readCorpus concatenates every walked file with a "----- relpath -----"
// header (the same labelling the inference prompt uses), so even the
// read-mode result keeps per-file provenance.
func readCorpus(files []sourceFile, relBase string) string {
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f.abs)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "----- %s -----\n", joinRel(relBase, f.rel))
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// joinRel prefixes a walk-relative path with the caller-relative base.
// Slash-separated on both sides (chunk relpaths are ToSlash'd).
func joinRel(base, rel string) string {
	if base == "" {
		return rel
	}
	return path.Join(base, rel)
}

// meanChunkBytes is the representative chunk size for budget-derived
// density over a corpus, where chunk sizes are heterogeneous (unlike the
// uniform byte grid).
func meanChunkBytes(chunks []Chunk) int {
	if len(chunks) == 0 {
		return 0
	}
	total := 0
	for _, c := range chunks {
		total += c.ByteLength
	}
	return total / len(chunks)
}

// scopeToRootModule filters the boundary to the root module's tree,
// excluding nested modules — directories with their own language-root
// marker (go.mod, package.json, …) that aren't the studied root. Nested
// modules are separate projects; studying the repo root should not
// sample eval fixtures or vendored subprojects. Marker-less subdirs
// (src/, cmd/, internal/) belong to the root module's tree and are kept.
//
// No-op when the studied root has no marker itself (a plain directory of
// scripts, a docs folder): there's no objective boundary to enforce, so
// behavior is unchanged. The large-file list is filtered to match so
// over-cap files inside excluded modules aren't gridded back in.
func scopeToRootModule(out *BoundaryOutput, root string, large []sourceFile) []sourceFile {
	rootHasMarker := false
	for _, m := range out.Modules {
		if m.RootPath == root && m.HasMarker {
			rootHasMarker = true
			break
		}
	}
	if !rootHasMarker {
		return large
	}
	excluded := map[string]bool{}
	for _, m := range out.Modules {
		if m.HasMarker && m.RootPath != root {
			excluded[m.ID] = true
		}
	}
	if len(excluded) == 0 {
		return large
	}

	// Filter chunks in place.
	kept := out.Chunks[:0]
	for _, c := range out.Chunks {
		if !excluded[c.ModuleID] {
			kept = append(kept, c)
		}
	}
	out.Chunks = kept

	// Filter modules in place.
	mods := out.Modules[:0]
	for _, m := range out.Modules {
		if !excluded[m.ID] {
			mods = append(mods, m)
		}
	}
	out.Modules = mods

	// Filter large files under excluded modules.
	keptLarge := large[:0]
	for _, f := range large {
		nested := false
		for id := range excluded {
			if f.rel == id || strings.HasPrefix(f.rel, id+"/") {
				nested = true
				break
			}
		}
		if !nested {
			keptLarge = append(keptLarge, f)
		}
	}

	// Recompute totals from the kept chunks so coverage fractions stay
	// accurate (the sampler's denominator is EffTotalLines).
	out.EffTotalLines = 0
	out.TotalLines = 0
	filesSeen := map[string]bool{}
	for _, c := range out.Chunks {
		out.EffTotalLines += c.EffLines
		out.TotalLines += c.LineEnd - c.LineStart + 1
		filesSeen[c.RelPath] = true
	}
	out.TotalFiles = len(filesSeen)
	return keptLarge
}
