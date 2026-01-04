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
type Scenario struct {
	ID      string    `yaml:"id"`
	Name    string    `yaml:"name"`
	Context []Context `yaml:"context"`
	Tests   []Test    `yaml:"tests"`
}

// Context represents a piece of knowledge Cortex should have.
type Context struct {
	Type    string `yaml:"type"`    // decision, pattern, correction, constraint
	Content string `yaml:"content"` // The actual content
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
	if len(s.Tests) == 0 {
		return nil, fmt.Errorf("scenario %s has no tests", s.ID)
	}

	// Auto-generate test IDs if not provided
	for i := range s.Tests {
		if s.Tests[i].ID == "" {
			s.Tests[i].ID = fmt.Sprintf("test-%d", i+1)
		}
	}

	return &s, nil
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
