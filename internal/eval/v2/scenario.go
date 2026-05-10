// Package eval provides a unified evaluation framework for Cortex.
// All evals measure one thing: ABR (Agentic Benefit Ratio).
package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario defines a single evaluation scenario.
// Each scenario establishes context and runs tests to measure ABR.
// Supports two formats:
//   - Flat: Context + Tests (simple, single-level)
//   - Tree: Tree node with children (context accumulation, supersession)
type Scenario struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`

	// Flat format (backward compatible)
	Context []Context `yaml:"context,omitempty"`
	Tests   []Test    `yaml:"tests,omitempty"`

	// Tree format (new)
	Tree *TreeNode `yaml:"tree,omitempty"`

	// Event graph format (LoCoMo-style causal chains)
	Events []Event `yaml:"events,omitempty"`
}

// TreeNode represents a node in the scenario tree.
// Tests at each node inherit all ancestor context.
type TreeNode struct {
	Context  []Context   `yaml:"context,omitempty"`
	Tests    []Test      `yaml:"tests,omitempty"`
	Children []*TreeNode `yaml:"children,omitempty"`
}

// Context represents a piece of knowledge Cortex should have.
type Context struct {
	Type       string `yaml:"type"`                 // decision, pattern, correction, constraint
	Content    string `yaml:"content"`              // The actual content
	Supersedes string `yaml:"supersedes,omitempty"` // Content this replaces (for decision evolution)
}

// Event represents a timestamped event with causal relationships.
// Used for LoCoMo-style causal chain reasoning scenarios.
type Event struct {
	ID       string   `yaml:"id"`                  // Unique event identifier
	Time     string   `yaml:"time"`                // Timestamp or time period (e.g., "2024-01", "Q2 2024")
	Content  string   `yaml:"content"`             // Event description
	CausedBy []string `yaml:"caused_by,omitempty"` // IDs of events that caused this one
}

// Test defines a query and expected results.
type Test struct {
	ID          string   `yaml:"id,omitempty"`
	Query       string   `yaml:"query"`
	Expect      Expect   `yaml:"expect"`
	CausalChain []string `yaml:"causal_chain,omitempty"` // Expected event chain for LoCoMo-style scenarios
}

// Expect defines what the response should include/exclude.
type Expect struct {
	Includes []string `yaml:"includes,omitempty"`
	Excludes []string `yaml:"excludes,omitempty"`

	// Ranking specifies expected retrieval order (for ABR measurement).
	// Each entry is a substring that should appear in results at that position.
	// When present, NDCG is calculated comparing actual vs expected ranking.
	Ranking []string `yaml:"ranking,omitempty"`
}

// Load reads a scenario from a YAML file.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}

	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse scenario: %w", err)
	}

	// Validate required fields
	if s.ID == "" {
		return nil, fmt.Errorf("scenario missing id")
	}

	// Determine format: tree or flat (events can be used with either)
	isTree := s.Tree != nil
	isFlat := len(s.Tests) > 0 || len(s.Context) > 0
	hasEvents := len(s.Events) > 0

	if isTree && isFlat {
		return nil, fmt.Errorf("scenario %s: cannot mix tree and flat formats", s.ID)
	}
	if !isTree && !isFlat && !hasEvents {
		return nil, fmt.Errorf("scenario %s: must have either tree, tests, or events", s.ID)
	}

	// Validate events and causal references
	if hasEvents {
		if err := validateEvents(&s); err != nil {
			return nil, fmt.Errorf("scenario %s: %w", s.ID, err)
		}
	}

	if isFlat || hasEvents {
		// Validate flat scenario
		if len(s.Tests) == 0 {
			return nil, fmt.Errorf("scenario %s has no tests", s.ID)
		}
		// Auto-generate test IDs if not provided
		for i := range s.Tests {
			if s.Tests[i].ID == "" {
				s.Tests[i].ID = fmt.Sprintf("test-%d", i+1)
			}
		}
	} else {
		// Validate and number tree tests
		autoNumberTreeTests(s.Tree, "")
	}

	return &s, nil
}

// validateEvents checks that all event references are valid.
func validateEvents(s *Scenario) error {
	// Build set of valid event IDs
	eventIDs := make(map[string]bool)
	for _, e := range s.Events {
		if e.ID == "" {
			return fmt.Errorf("event missing id")
		}
		if eventIDs[e.ID] {
			return fmt.Errorf("duplicate event id: %s", e.ID)
		}
		eventIDs[e.ID] = true
	}

	// Validate caused_by references
	for _, e := range s.Events {
		for _, ref := range e.CausedBy {
			if !eventIDs[ref] {
				return fmt.Errorf("event %s references unknown event: %s", e.ID, ref)
			}
		}
	}

	// Validate causal_chain references in tests
	for _, t := range s.Tests {
		for _, ref := range t.CausalChain {
			if !eventIDs[ref] {
				return fmt.Errorf("test %s references unknown event: %s", t.ID, ref)
			}
		}
	}

	return nil
}

// autoNumberTreeTests assigns IDs to tests in a tree that don't have them.
func autoNumberTreeTests(node *TreeNode, prefix string) {
	if node == nil {
		return
	}
	for i := range node.Tests {
		if node.Tests[i].ID == "" {
			if prefix == "" {
				node.Tests[i].ID = fmt.Sprintf("test-%d", i+1)
			} else {
				node.Tests[i].ID = fmt.Sprintf("%s-test-%d", prefix, i+1)
			}
		}
	}
	for i, child := range node.Children {
		childPrefix := fmt.Sprintf("node-%d", i+1)
		if prefix != "" {
			childPrefix = fmt.Sprintf("%s-%s", prefix, childPrefix)
		}
		autoNumberTreeTests(child, childPrefix)
	}
}

// LoadAll reads all scenarios from a directory.
func LoadAll(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenario dir: %w", err)
	}

	var scenarios []*Scenario
	for _, entry := range entries {
		if entry.IsDir() {
			// Skip subdirectories that hold scenarios in a different schema
			// loaded by their own dedicated path (e.g., measure/* is loaded
			// via LoadMeasureScenario when `cortex eval --measure` is used).
			if entry.Name() == "measure" {
				continue
			}
			// Recurse into subdirectories
			subScenarios, err := LoadAll(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			scenarios = append(scenarios, subScenarios...)
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		s, err := Load(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		scenarios = append(scenarios, s)
	}

	return scenarios, nil
}
