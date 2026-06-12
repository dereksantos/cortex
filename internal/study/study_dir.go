package study

import (
	"context"
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
	files, err := walkSourceFiles(req.Path, ignore)
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
	// per file in deterministic (sorted relpath) order.
	var total int64
	for _, f := range files {
		total += f.size
	}
	if total/studyCharsPerToken < int64(window/2) {
		return StudyResponse{Mode: "read", ReadContent: readCorpus(files, relBase)}, nil
	}

	// Corpus boundary: the universal analyzer scoped to this directory.
	// TODO(study): files over universalMaxFileBytes are skipped by the
	// walk, so a huge file inside a studied dir is invisible here; route
	// such files through per-file byte grids merged into this boundary.
	an := UniversalAnalyzer{Salt: req.Session}
	out, err := an.Analyze(ctx, req.Path, ignore)
	if err != nil {
		return StudyResponse{}, fmt.Errorf("study: analyze %s: %w", req.Path, err)
	}

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
