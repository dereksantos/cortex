package study

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dereksantos/cortex/internal/projectscan"
)

// Tier 1 defaults — knobs surfaced via UniversalAnalyzer struct so
// callers can override without touching the analyzer body.
const (
	DefaultWindowLines    = 400
	DefaultWindowOverlap  = 40
	universalMaxFileBytes = 1 * 1024 * 1024 // 1 MiB per-file cap
	universalSanityCap    = 10000           // cap total files scanned
)

// languageRootMarkers fire first when two markers share a depth (per
// the pinned module-marker rule). The order WITHIN this slice doesn't
// matter — the rule only cares which list a marker belongs to.
var languageRootMarkers = []string{
	"go.mod",
	"package.json",
	"pyproject.toml",
	"setup.py",
	"Cargo.toml",
	"pom.xml",
	"Gemfile",
	"composer.json",
	"mix.exs",
	"pubspec.yaml",
}

// buildHelperMarkers fire only when no language-root marker fires at
// the same depth.
var buildHelperMarkers = []string{
	"Makefile",
	"CMakeLists.txt",
	"build.gradle",
}

// UniversalAnalyzer is the Tier 1 BoundaryAnalyzer: filesystem-only,
// language-agnostic. Configured by struct fields rather than a Config
// argument so the BoundaryAnalyzer interface stays minimal.
type UniversalAnalyzer struct {
	WindowLines   int
	WindowOverlap int
	Salt          string
}

// Tier returns 1 (universal).
func (UniversalAnalyzer) Tier() int { return 1 }

// Analyze walks the project, builds chunks + modules + edges, and
// derives a deterministic RNGSeed from the project-state hash.
func (a UniversalAnalyzer) Analyze(ctx context.Context, projectRoot string, ignore *projectscan.IgnoreSet) (*BoundaryOutput, error) {
	if ignore == nil {
		ignore = projectscan.LoadIgnoreSet(projectRoot)
	}
	windowLines := a.WindowLines
	if windowLines <= 0 {
		windowLines = DefaultWindowLines
	}
	overlap := a.WindowOverlap
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= windowLines {
		overlap = windowLines - 1
	}

	moduleCache := make(map[string]moduleAssignment) // dir → assignment

	// Per-file records (collected first, deterministically sorted, then
	// processed). This isolates randomness in filesystem walk order
	// from the chunk-emission order, satisfying the determinism contract.
	type fileRec struct {
		abs   string
		rel   string
		size  int64
		mtime int64 // unix seconds (UTC)
	}
	var files []fileRec

	err := filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path == projectRoot {
				return nil
			}
			if ignore.IsDirExcluded(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.IsFileExcluded(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() == 0 || info.Size() > universalMaxFileBytes {
			return nil
		}
		// Layer 3 magic-byte sniff: catches sensitive content that
		// slipped past layers 1 + 2 (e.g., .txt file containing a
		// PEM private key).
		if ignore.IsSensitiveByMagicBytes(path) {
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		rel = filepath.ToSlash(rel)
		files = append(files, fileRec{
			abs:   path,
			rel:   rel,
			size:  info.Size(),
			mtime: info.ModTime().UTC().Unix(),
		})
		if len(files) >= universalSanityCap {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("study: walk: %w", err)
	}

	// Sort files lexically by rel path (defensive — WalkDir already
	// emits lexical order, but we don't rely on Go's implementation
	// detail).
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	// Build the state hash from sorted (relpath:size:mtime) tuples.
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s:%d:%d\n", f.rel, f.size, f.mtime)
	}
	stateHash := hex.EncodeToString(h.Sum(nil))

	// Derive RNG seed deterministically from state hash + salt.
	seedHasher := fnv.New64a()
	seedHasher.Write([]byte(stateHash))
	seedHasher.Write([]byte(a.Salt))
	rngSeed := int64(seedHasher.Sum64())

	// Process files: read bytes, line-index, chunk, assign module.
	out := &BoundaryOutput{
		ProjectRoot: projectRoot,
		StateHash:   stateHash,
		RNGSeed:     rngSeed,
		FileHashes:  make(map[string]string, len(files)),
	}
	moduleIndex := make(map[string]*Module) // moduleID → Module
	totalFiles := 0
	totalLines := 0
	effTotalLines := 0

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			continue
		}
		lineStarts, totalLineCount := indexLines(data)
		if totalLineCount == 0 {
			continue
		}

		assign := resolveModule(f.abs, projectRoot, moduleCache)
		lang := langFor(filepath.Ext(f.abs))

		chunks := buildChunks(f.abs, f.rel, assign.id, lang, data, lineStarts, totalLineCount, windowLines, overlap)
		if len(chunks) == 0 {
			continue
		}

		// Per-file content hash — the drift-detection key. Edits to a
		// file invalidate exactly its chunks; untouched files keep their
		// covered set across studies.
		fh := sha256.Sum256(data)
		out.FileHashes[f.rel] = hex.EncodeToString(fh[:])

		mod, ok := moduleIndex[assign.id]
		if !ok {
			mod = &Module{
				ID:         assign.id,
				RootPath:   assign.rootPath,
				HasMarker:  assign.markerName != "",
				MarkerName: assign.markerName,
			}
			moduleIndex[assign.id] = mod
		}
		mod.Files++
		for _, ch := range chunks {
			out.Chunks = append(out.Chunks, ch)
			mod.ChunkIDs = append(mod.ChunkIDs, ch.ID)
			mod.Lines += (ch.LineEnd - ch.LineStart + 1)
			mod.EffLines += ch.EffLines
			totalLines += (ch.LineEnd - ch.LineStart + 1)
			effTotalLines += ch.EffLines
		}
		totalFiles++
	}

	// Materialize modules in deterministic order.
	out.Modules = make([]Module, 0, len(moduleIndex))
	for _, m := range moduleIndex {
		out.Modules = append(out.Modules, *m)
	}
	sort.Slice(out.Modules, func(i, j int) bool { return out.Modules[i].ID < out.Modules[j].ID })

	// Sort chunks deterministically (by RelPath then LineStart).
	sort.Slice(out.Chunks, func(i, j int) bool {
		a := out.Chunks[i]
		b := out.Chunks[j]
		if a.RelPath != b.RelPath {
			return a.RelPath < b.RelPath
		}
		return a.LineStart < b.LineStart
	})

	out.TotalLines = totalLines
	out.EffTotalLines = effTotalLines
	out.TotalFiles = totalFiles

	// Edges (Tier 1): sibling fs_dir between modules whose parent dirs
	// match. Sorted deterministically.
	out.Edges = buildSiblingEdges(out.Modules, projectRoot)

	return out, nil
}

// moduleAssignment is the result of resolving which module a file
// belongs to. id is rel-path-style (project-relative dir), rootPath
// is the absolute marker dir, markerName is the marker filename
// that fired (empty when the top-level-dir fallback applied).
type moduleAssignment struct {
	id         string
	rootPath   string
	markerName string
}

// resolveModule walks up from filePath's parent dir looking for a
// marker. Nearest ancestor wins. At the same depth, language-root
// markers take precedence over build-helper markers. If no marker
// fires anywhere up to projectRoot, the file's top-level directory
// under root becomes its module; files directly at root use "." as
// the module ID.
//
// moduleCache memoizes assignments per-directory so a project with
// thousands of files in the same module doesn't re-walk the ancestor
// chain for each file.
func resolveModule(filePath, projectRoot string, moduleCache map[string]moduleAssignment) moduleAssignment {
	dir := filepath.Dir(filePath)
	if cached, ok := moduleCache[dir]; ok {
		return cached
	}

	current := dir
	for {
		// Check this directory for markers (language-root first to
		// satisfy the same-depth tie-break rule).
		for _, m := range languageRootMarkers {
			if fileExistsAt(filepath.Join(current, m)) {
				rel, _ := filepath.Rel(projectRoot, current)
				rel = filepath.ToSlash(rel)
				if rel == "" || rel == "." {
					rel = "."
				}
				asg := moduleAssignment{id: rel, rootPath: current, markerName: m}
				moduleCache[dir] = asg
				return asg
			}
		}
		for _, m := range buildHelperMarkers {
			if fileExistsAt(filepath.Join(current, m)) {
				rel, _ := filepath.Rel(projectRoot, current)
				rel = filepath.ToSlash(rel)
				if rel == "" || rel == "." {
					rel = "."
				}
				asg := moduleAssignment{id: rel, rootPath: current, markerName: m}
				moduleCache[dir] = asg
				return asg
			}
		}
		// Stop conditions.
		if current == projectRoot {
			// No marker found anywhere on the way to root. Fall back
			// to the file's top-level dir under root (or "." if the
			// file is directly at root).
			rel, _ := filepath.Rel(projectRoot, filePath)
			rel = filepath.ToSlash(rel)
			parts := strings.SplitN(rel, "/", 2)
			var id, rootPath string
			if len(parts) == 1 {
				id = "."
				rootPath = projectRoot
			} else {
				id = parts[0]
				rootPath = filepath.Join(projectRoot, parts[0])
			}
			asg := moduleAssignment{id: id, rootPath: rootPath, markerName: ""}
			moduleCache[dir] = asg
			return asg
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Filesystem root reached without hitting projectRoot —
			// shouldn't happen for valid inputs, but degrade gracefully.
			asg := moduleAssignment{id: ".", rootPath: projectRoot, markerName: ""}
			moduleCache[dir] = asg
			return asg
		}
		current = parent
	}
}

// fileExistsAt returns true if the path exists and is a regular file.
// Pure helper — no side effects.
func fileExistsAt(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// indexLines walks `data` to find the byte offset of each line's
// start. The returned `lineStarts` has length totalLines+1: index 0
// is byte 0, index N is the byte after the last newline (or len(data)
// when the file lacks a trailing newline). totalLines is 0 for empty
// input, otherwise ≥ 1.
//
// CRLF is handled implicitly: '\r' bytes are not line separators on
// their own; only '\n' counts.
func indexLines(data []byte) (lineStarts []int64, totalLines int) {
	if len(data) == 0 {
		return nil, 0
	}
	lineStarts = make([]int64, 0, 64)
	lineStarts = append(lineStarts, 0)
	for i, b := range data {
		if b == '\n' {
			// next line starts at i+1
			if i+1 <= len(data) {
				lineStarts = append(lineStarts, int64(i+1))
			}
		}
	}
	// totalLines = number of lines actually present. If the file ends
	// with a newline, lineStarts has totalLines+1 entries (the last
	// one points past EOF). If it doesn't, the last partial-line is
	// still a line.
	totalLines = len(lineStarts) - 1
	if len(data) > 0 && data[len(data)-1] != '\n' {
		// Last line had content but no terminating newline; count it.
		totalLines = len(lineStarts)
	}
	// Make sure lineStarts has a sentinel at len(data) so chunk
	// builders can always compute (lineStarts[end] - lineStarts[start]).
	if len(lineStarts) == totalLines {
		lineStarts = append(lineStarts, int64(len(data)))
	}
	return lineStarts, totalLines
}

// buildChunks slices a file's content into WindowLines-line chunks
// with WindowOverlap-line overlap. Each chunk records absolute byte
// offset + length so the controller can later do
// fractal.ReadRegion(path, offset, length) without re-scanning.
func buildChunks(
	absPath, relPath, moduleID, lang string,
	data []byte,
	lineStarts []int64,
	totalLines, windowLines, overlap int,
) []Chunk {
	if totalLines <= 0 {
		return nil
	}
	step := windowLines - overlap
	if step <= 0 {
		step = windowLines
	}

	var chunks []Chunk
	for start := 1; start <= totalLines; start += step {
		end := start + windowLines - 1
		if end > totalLines {
			end = totalLines
		}
		startByte := lineStarts[start-1]
		var endByte int64
		if end < len(lineStarts) {
			endByte = lineStarts[end]
		} else {
			endByte = int64(len(data))
		}
		if endByte > int64(len(data)) {
			endByte = int64(len(data))
		}
		length := int(endByte - startByte)
		if length <= 0 {
			break
		}
		body := data[startByte:endByte]
		eff := effectiveLinesOf(body, lang)
		ch := Chunk{
			ID:         chunkID(relPath, start, end),
			Path:       absPath,
			RelPath:    relPath,
			LineStart:  start,
			LineEnd:    end,
			ByteOffset: startByte,
			ByteLength: length,
			EffLines:   eff,
			EstTokens:  length / 4,
			ModuleID:   moduleID,
			Lang:       lang,
		}
		chunks = append(chunks, ch)
		if end >= totalLines {
			break
		}
	}
	return chunks
}

// chunkID returns the stable identifier for a chunk: first 16 hex
// chars of sha256(relpath + ":" + line_start + ":" + line_end). 16
// chars = 64 bits of entropy, well below collision risk for the
// typical 10K-chunk scale.
func chunkID(relPath string, lineStart, lineEnd int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%d:%d", relPath, lineStart, lineEnd)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildSiblingEdges emits fs_dir edges between every pair of modules
// whose parent directories match. Tier 1 only — Lévy / RWR samplers
// will consume this; the day-one hierarchical sampler ignores edges.
// Sorted (FromModuleID, ToModuleID) for determinism.
func buildSiblingEdges(modules []Module, projectRoot string) []Edge {
	if len(modules) < 2 {
		return nil
	}
	parents := make(map[string][]string)
	for _, m := range modules {
		parent := filepath.Dir(m.RootPath)
		parents[parent] = append(parents[parent], m.ID)
	}
	var edges []Edge
	for _, ids := range parents {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				edges = append(edges,
					Edge{FromModuleID: ids[i], ToModuleID: ids[j], Kind: "fs_dir", Weight: 1.0},
					Edge{FromModuleID: ids[j], ToModuleID: ids[i], Kind: "fs_dir", Weight: 1.0},
				)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].FromModuleID != edges[j].FromModuleID {
			return edges[i].FromModuleID < edges[j].FromModuleID
		}
		return edges[i].ToModuleID < edges[j].ToModuleID
	})
	return edges
}
