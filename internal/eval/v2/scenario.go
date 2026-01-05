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

// Test defines a query and expected results.
type Test struct {
	ID     string `yaml:"id,omitempty"`
	Query  string `yaml:"query"`
	Expect Expect `yaml:"expect"`
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

	// Determine format: tree or flat
	isTree := s.Tree != nil
	isFlat := len(s.Tests) > 0 || len(s.Context) > 0

	if isTree && isFlat {
		return nil, fmt.Errorf("scenario %s: cannot mix tree and flat formats", s.ID)
	}
	if !isTree && !isFlat {
		return nil, fmt.Errorf("scenario %s: must have either tree or tests", s.ID)
	}

	if isFlat {
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
