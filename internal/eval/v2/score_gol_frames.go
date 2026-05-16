//go:build !windows

package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ScoreGoLFrames is a thin specialization on top of RunRepoTests: it
// uses RunRepoTests for the build step and then layers fixture-based
// stdin/stdout frame diffing as its own test phase. The build env and
// truncation behavior come from the generic helper so SWE-bench and
// future Go-test benchmarks share the same plumbing.

// FrameDiffResult is the structured outcome of comparing the harness's
// built binary against canonical Game-of-Life fixtures (blinker,
// glider, …). One sub-result per fixture pair.
type FrameDiffResult struct {
	Cases      []FrameCaseResult
	Passed     int
	Failed     int
	AllPassed  bool
	BuildOK    bool
	BinaryPath string // path to the built binary (workdir/<module-name>)
	BuildOut   string // truncated build output on failure
}

// FrameCaseResult is one fixture pair's outcome.
type FrameCaseResult struct {
	Name       string // base name without extension
	Passed     bool
	WantBytes  int
	GotBytes   int
	DiffSample string // first ~512 chars of diff when Passed=false
	Err        string // shell or normalization error, if any
}

// ScoreGoLFrames builds the workdir's Go binary and runs it against
// each <fixturesDir>/<name>.in, diffing the result against
// <fixturesDir>/<name>.out after light normalization.
//
// Normalization:
//   - line endings collapsed to "\n"
//   - trailing whitespace stripped per line
//   - a single trailing newline allowed/added on both sides
//
// generations is the value passed via --generations. The binary is
// expected to accept this flag and emit gen 0 through gen N (so 5
// frames for generations=4), separated by blank lines.
//
// The binary is built via `go build -o <workdir>/gol .`. If your seed
// uses a different module name or main package layout, adjust the
// build args. For the GoL scenario the seed is `module gol` with
// main.go at the workdir root, so this lands as workdir/gol.
//
// On build failure, the function returns early with BuildOK=false and
// the build output captured in BuildOut. Cases is left empty.
func ScoreGoLFrames(ctx context.Context, workdir, fixturesDir string, generations int) (FrameDiffResult, error) {
	if !filepath.IsAbs(workdir) {
		return FrameDiffResult{}, fmt.Errorf("workdir must be absolute, got %q", workdir)
	}

	binaryPath := filepath.Join(workdir, "gol")
	repoRes, err := RunRepoTests(ctx, workdir, RepoTestSpec{
		BuildCmd: []string{"go", "build", "-o", binaryPath, "."},
		Timeout:  60 * time.Second,
	})
	if err != nil {
		return FrameDiffResult{BinaryPath: binaryPath}, err
	}
	if !repoRes.BuildOK {
		return FrameDiffResult{
			BuildOK:    false,
			BinaryPath: binaryPath,
			BuildOut:   repoRes.BuildOut,
		}, nil
	}

	pairs, err := discoverFramePairs(fixturesDir)
	if err != nil {
		return FrameDiffResult{BuildOK: true, BinaryPath: binaryPath}, err
	}
	if len(pairs) == 0 {
		return FrameDiffResult{BuildOK: true, BinaryPath: binaryPath}, fmt.Errorf("no fixture .in/.out pairs found under %s", fixturesDir)
	}

	res := FrameDiffResult{BuildOK: true, BinaryPath: binaryPath, AllPassed: true}
	for _, p := range pairs {
		caseRes := runFrameCase(ctx, binaryPath, workdir, p, generations)
		res.Cases = append(res.Cases, caseRes)
		if caseRes.Passed {
			res.Passed++
		} else {
			res.Failed++
			res.AllPassed = false
		}
	}
	return res, nil
}

// framePair holds the paths of one fixture pair.
type framePair struct {
	Name    string
	InPath  string
	OutPath string
}

// discoverFramePairs walks fixturesDir for *.in files and matches them
// to *.out siblings. Returns sorted by name for determinism.
func discoverFramePairs(dir string) ([]framePair, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("fixtures dir does not exist: %s", dir)
		}
		return nil, err
	}
	var pairs []framePair
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".in") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".in")
		outPath := filepath.Join(dir, base+".out")
		if _, err := os.Stat(outPath); err != nil {
			continue // unmatched .in; skip silently
		}
		pairs = append(pairs, framePair{
			Name:    base,
			InPath:  filepath.Join(dir, e.Name()),
			OutPath: outPath,
		})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Name < pairs[j].Name })
	return pairs, nil
}

// runFrameCase runs one binary invocation with the .in file on stdin
// and diffs stdout against the .out file. Returns a structured result.
func runFrameCase(ctx context.Context, binaryPath, workdir string, p framePair, generations int) FrameCaseResult {
	res := FrameCaseResult{Name: p.Name}

	want, err := os.ReadFile(p.OutPath)
	if err != nil {
		res.Err = fmt.Sprintf("read .out: %v", err)
		return res
	}
	in, err := os.ReadFile(p.InPath)
	if err != nil {
		res.Err = fmt.Sprintf("read .in: %v", err)
		return res
	}

	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binaryPath, "--generations", fmt.Sprintf("%d", generations))
	cmd.Dir = workdir
	cmd.Stdin = bytes.NewReader(in)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		res.Err = fmt.Sprintf("run: %v (output: %s)", err, truncateGoL(out.String(), 512))
		return res
	}

	gotNorm := normalizeFrames(out.String())
	wantNorm := normalizeFrames(string(want))

	res.GotBytes = len(gotNorm)
	res.WantBytes = len(wantNorm)
	if gotNorm == wantNorm {
		res.Passed = true
		return res
	}
	res.DiffSample = truncateGoL(simpleDiff(wantNorm, gotNorm), 512)
	return res
}

// normalizeFrames is the slack we cut the model: line-ending
// collapse, trailing-whitespace strip per line, single trailing
// newline. Anything more would require interpretation (e.g. accepting
// either `#` or `o` for alive); the seed README is explicit so the
// model knows the charset and we don't need to be that generous.
func normalizeFrames(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	out := strings.Join(lines, "\n")
	out = strings.TrimRight(out, "\n") + "\n"
	return out
}

// simpleDiff returns a unified-ish summary of the first 4 differing
// lines between want and got. Avoids the testing.go diff helpers
// (different package) and keeps the output narrow enough to inline in
// a CellResult.Notes field if needed.
func simpleDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	var b strings.Builder
	n := len(wl)
	if len(gl) > n {
		n = len(gl)
	}
	diffs := 0
	for i := 0; i < n && diffs < 4; i++ {
		w := ""
		if i < len(wl) {
			w = wl[i]
		}
		g := ""
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			fmt.Fprintf(&b, "line %d:\n  - %q\n  + %q\n", i+1, w, g)
			diffs++
		}
	}
	if diffs == 0 {
		fmt.Fprintf(&b, "no per-line diffs (trailing newline / length mismatch)\nwant len=%d got len=%d", len(want), len(got))
	}
	return b.String()
}

func truncateGoL(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncateGoLd)"
}
