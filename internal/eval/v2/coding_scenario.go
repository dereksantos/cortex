//go:build !windows

package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CodingScenario describes one end-to-end coding eval: seed
// directory, the task prompt the agent receives, run mode, scoring
// fixtures, and (for multi-session) the retry knobs.
//
// Loaded via LoadCodingScenario from a YAML file. The YAML format is
// intentionally minimal — a hand-rolled key:value parser handles it
// so we don't pull in a YAML dependency the rest of internal/eval/v2
// doesn't need. Supported keys (each on its own line):
//
//	id: <string>
//	name: <string>
//	mode: single | multi-session
//	max_tries: <int>            # multi-session only; default 1 for single
//	dream_idle_seconds: <int>   # multi-session only; default 30
//	generations: <int>          # passed via --generations to the binary
//	seed_dir: <path relative to the scenario file>
//	fixtures_dir: <path relative to the scenario file>
//	freeform_input: <path relative to the scenario file>
//	prompt: |
//	  <multi-line prompt, indented by exactly 2 spaces>
//
// Each non-prompt key takes a single-line value. The `prompt: |`
// block reads subsequent lines until the indentation drops below 2
// spaces (or EOF). That's enough YAML for the GoL scenarios; if
// future coding scenarios need more, swap to gopkg.in/yaml.v3.
type CodingScenario struct {
	ID               string
	Name             string
	Mode             string // "single" | "multi-session"
	MaxTries         int
	DreamIdleSeconds int
	Generations      int
	SeedDir          string // absolute
	FixturesDir      string // absolute
	FreeformInput    string // absolute
	Prompt           string

	// Path is the absolute scenario file path; used to resolve the
	// other path fields.
	Path string
}

// LoadCodingScenario reads and validates a coding scenario YAML.
// Returns an error if required fields are missing, the mode is
// unknown, or any referenced file is absent on disk.
func LoadCodingScenario(path string) (*CodingScenario, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	bb, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}

	s := &CodingScenario{Path: abs}
	if err := parseCodingScenario(string(bb), s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Resolve relative paths against the scenario file's directory.
	scenarioDir := filepath.Dir(abs)
	if !filepath.IsAbs(s.SeedDir) {
		s.SeedDir = filepath.Join(scenarioDir, s.SeedDir)
	}
	if !filepath.IsAbs(s.FixturesDir) {
		s.FixturesDir = filepath.Join(scenarioDir, s.FixturesDir)
	}
	if !filepath.IsAbs(s.FreeformInput) {
		s.FreeformInput = filepath.Join(scenarioDir, s.FreeformInput)
	}

	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// validate enforces required fields, known modes, and on-disk presence
// of referenced files.
func (s *CodingScenario) validate() error {
	if s.ID == "" {
		return fmt.Errorf("scenario id is required")
	}
	if s.Prompt == "" {
		return fmt.Errorf("scenario %s: prompt is required", s.ID)
	}
	switch s.Mode {
	case "single", "multi-session":
	default:
		return fmt.Errorf("scenario %s: unknown mode %q (want single|multi-session)", s.ID, s.Mode)
	}
	if s.Mode == "multi-session" {
		if s.MaxTries < 2 {
			return fmt.Errorf("scenario %s: multi-session mode requires max_tries >= 2 (got %d)", s.ID, s.MaxTries)
		}
		if s.DreamIdleSeconds < 0 {
			return fmt.Errorf("scenario %s: dream_idle_seconds must be >= 0 (got %d)", s.ID, s.DreamIdleSeconds)
		}
	} else if s.MaxTries == 0 {
		s.MaxTries = 1
	}
	if s.Generations <= 0 {
		s.Generations = 4 // matches the GoL spec used in the eval
	}
	if s.SeedDir == "" {
		return fmt.Errorf("scenario %s: seed_dir is required", s.ID)
	}
	if _, err := os.Stat(s.SeedDir); err != nil {
		return fmt.Errorf("scenario %s: seed_dir %s: %w", s.ID, s.SeedDir, err)
	}
	if s.FixturesDir == "" {
		return fmt.Errorf("scenario %s: fixtures_dir is required", s.ID)
	}
	if _, err := os.Stat(s.FixturesDir); err != nil {
		return fmt.Errorf("scenario %s: fixtures_dir %s: %w", s.ID, s.FixturesDir, err)
	}
	if s.FreeformInput != "" {
		if _, err := os.Stat(s.FreeformInput); err != nil {
			return fmt.Errorf("scenario %s: freeform_input %s: %w", s.ID, s.FreeformInput, err)
		}
	}
	return nil
}

// parseCodingScenario implements the minimal YAML-ish parser described
// on the CodingScenario doc. Lines are processed in order; the
// `prompt: |` block consumes subsequent indented lines.
func parseCodingScenario(body string, s *CodingScenario) error {
	lines := strings.Split(body, "\n")
	inPrompt := false
	var prompt strings.Builder
	promptIndent := -1

	for i, raw := range lines {
		// Promote tabs to 2 spaces for consistent indent measurement.
		ln := strings.ReplaceAll(raw, "\t", "  ")
		trimmed := strings.TrimSpace(ln)

		if inPrompt {
			// End the block when indentation drops below promptIndent
			// or we hit a non-empty line with the same indent and a
			// `key:` shape.
			indent := leadingSpaces(ln)
			if trimmed == "" {
				if promptIndent < 0 {
					// haven't seen a content line yet; skip blanks
					continue
				}
				prompt.WriteString("\n")
				continue
			}
			if promptIndent < 0 {
				promptIndent = indent
			}
			if indent < promptIndent {
				inPrompt = false
				// fall through to process this line as a normal key
			} else {
				prompt.WriteString(ln[promptIndent:])
				prompt.WriteString("\n")
				continue
			}
		}

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			return fmt.Errorf("line %d: expected key: value (got %q)", i+1, trimmed)
		}
		key := strings.TrimSpace(trimmed[:idx])
		val := strings.TrimSpace(trimmed[idx+1:])

		switch key {
		case "id":
			s.ID = stripQuotes(val)
		case "name":
			s.Name = stripQuotes(val)
		case "mode":
			s.Mode = stripQuotes(val)
		case "max_tries":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("line %d: max_tries: %w", i+1, err)
			}
			s.MaxTries = n
		case "dream_idle_seconds":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("line %d: dream_idle_seconds: %w", i+1, err)
			}
			s.DreamIdleSeconds = n
		case "generations":
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("line %d: generations: %w", i+1, err)
			}
			s.Generations = n
		case "seed_dir":
			s.SeedDir = stripQuotes(val)
		case "fixtures_dir":
			s.FixturesDir = stripQuotes(val)
		case "freeform_input":
			s.FreeformInput = stripQuotes(val)
		case "prompt":
			if val != "|" {
				return fmt.Errorf("line %d: prompt must be a `|` block (got %q)", i+1, val)
			}
			inPrompt = true
			promptIndent = -1
		default:
			return fmt.Errorf("line %d: unknown key %q", i+1, key)
		}
	}
	s.Prompt = strings.TrimSpace(prompt.String())
	if s.DreamIdleSeconds == 0 && s.Mode == "multi-session" {
		s.DreamIdleSeconds = 30
	}
	return nil
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
			continue
		}
		break
	}
	return n
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
