package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// exercise.go owns everything about one Exercism polyglot exercise as it
// exists in the pinned checkout: what it declares about itself, how a
// pristine working copy of it is built, and what the agent is told to do.
//
// The one rule that must never be relaxed: `.meta/` holds `example.go` — the
// reference SOLUTION. It is excluded from every working copy. Leaking it
// would make the benchmark measure copying, not coding.
const (
	metaDir = ".meta"
	docsDir = ".docs"
)

// Exercise is one practice exercise resolved from the pinned checkout.
type Exercise struct {
	// Name is the directory name ("alphametics") and the row's exercise key.
	Name string
	// Dir is the exercise's source directory inside the pinned checkout.
	Dir string
	// Solution are the stub files the agent is expected to implement,
	// relative to the exercise root (from .meta/config.json files.solution).
	Solution []string
	// Test are the test files the agent must not modify.
	Test []string
}

// metaConfig is the subset of .meta/config.json this runner reads.
type metaConfig struct {
	Files struct {
		Solution []string `json:"solution"`
		Test     []string `json:"test"`
	} `json:"files"`
}

// DiscoverExercises lists every Go practice exercise in the pinned checkout,
// sorted by name so `--only N` selects a stable prefix across runs.
func DiscoverExercises(srcRoot string) ([]Exercise, error) {
	practice := filepath.Join(srcRoot, "go", "exercises", "practice")
	entries, err := os.ReadDir(practice)
	if err != nil {
		return nil, fmt.Errorf("failed to read practice dir %s: %w", practice, err)
	}
	var out []Exercise
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ex, err := loadExercise(filepath.Join(practice, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, ex)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadExercise(dir string) (Exercise, error) {
	ex := Exercise{Name: filepath.Base(dir), Dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, metaDir, "config.json"))
	if err != nil {
		return ex, fmt.Errorf("failed to read %s config: %w", ex.Name, err)
	}
	var mc metaConfig
	if err := json.Unmarshal(b, &mc); err != nil {
		return ex, fmt.Errorf("failed to parse %s config: %w", ex.Name, err)
	}
	ex.Solution = mc.Files.Solution
	ex.Test = mc.Files.Test
	if len(ex.Solution) == 0 {
		return ex, fmt.Errorf("exercise %s declares no solution files", ex.Name)
	}
	return ex, nil
}

// SelectExercises applies --exercise (explicit names, in the order given) and
// --only N (first N of the discovered order). An explicit selection wins; the
// two are not combined.
func SelectExercises(all []Exercise, names []string, only int) ([]Exercise, error) {
	if len(names) > 0 {
		byName := make(map[string]Exercise, len(all))
		for _, e := range all {
			byName[e.Name] = e
		}
		var out []Exercise
		for _, n := range names {
			e, ok := byName[n]
			if !ok {
				return nil, fmt.Errorf("unknown exercise %q (run with --list to see the %d available)", n, len(all))
			}
			out = append(out, e)
		}
		return out, nil
	}
	if only > 0 && only < len(all) {
		return all[:only], nil
	}
	return all, nil
}

// PrepareWorkdir materialises a pristine working copy of the exercise at dest:
// every file the exercism template ships EXCEPT `.meta/` (the reference
// solution) and `.docs/` (which becomes the prompt instead, so the agent can't
// mine the instructions for hints it wasn't handed). It returns the sha256 of
// each declared solution file so a later comparison can tell whether the agent
// actually changed anything on disk.
func PrepareWorkdir(ex Exercise, dest string) (map[string]string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create workdir %s: %w", dest, err)
	}
	err := filepath.WalkDir(ex.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(ex.Dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if top := strings.Split(rel, string(filepath.Separator))[0]; top == metaDir || top == docsDir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dest, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, filepath.Join(dest, rel))
	})
	if err != nil {
		return nil, fmt.Errorf("failed to stage %s: %w", ex.Name, err)
	}
	return HashSolutionFiles(ex, dest)
}

// HashSolutionFiles digests every declared solution file under root. A file
// that does not exist hashes to "" — a deleted stub still counts as a change.
func HashSolutionFiles(ex Exercise, root string) (map[string]string, error) {
	out := make(map[string]string, len(ex.Solution))
	for _, rel := range ex.Solution {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				out[rel] = ""
				continue
			}
			return nil, fmt.Errorf("failed to hash %s: %w", rel, err)
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// CountChanged reports how many solution files differ between two digests.
func CountChanged(before, after map[string]string) int {
	n := 0
	for rel, b := range before {
		if after[rel] != b {
			n++
		}
	}
	return n
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// BuildPrompt assembles the single turn the agent is given: the exercise's
// own instructions verbatim, then the task frame (which files to implement,
// which not to touch, how correctness is checked).
//
// The frame deliberately states constraints, not a procedure — a recipe
// ("first read X, then write Y") would measure the recipe rather than the
// agent (see CLAUDE.md's note on non-prescriptive prompts).
func BuildPrompt(ex Exercise) (string, error) {
	var b strings.Builder
	main := filepath.Join(ex.Dir, docsDir, "instructions.md")
	body, err := os.ReadFile(main)
	if err != nil {
		return "", fmt.Errorf("failed to read instructions for %s: %w", ex.Name, err)
	}
	b.Write(body)
	if extra, err := os.ReadFile(filepath.Join(ex.Dir, docsDir, "instructions.append.md")); err == nil {
		b.WriteString("\n\n")
		b.Write(extra)
	}
	b.WriteString("\n\n# Task\n\n")
	b.WriteString("Implement the exercise above in this Go module so its existing tests pass.\n\n")
	fmt.Fprintf(&b, "- Implement: %s\n", strings.Join(ex.Solution, ", "))
	if len(ex.Test) > 0 {
		fmt.Fprintf(&b, "- Do not modify the test files: %s\n", strings.Join(ex.Test, ", "))
	}
	b.WriteString("- Keep the existing package name and the exported signatures the tests call.\n")
	b.WriteString("- Standard library only: do not add dependencies or edit go.mod.\n")
	b.WriteString("- Correctness is decided by `go test ./...` in this directory.\n")
	return b.String(), nil
}
