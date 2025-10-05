// Package models provides LLM model information and requirements
package models

import "fmt"

// Model represents an LLM model with its requirements
type Model struct {
	Name        string
	Size        string
	RAMGB       float64
	Description string
	Recommended bool
}

// AllModels returns a list of common Ollama models with their requirements
func AllModels() []Model {
	return []Model{
		{
			Name:        "phi3:mini",
			Size:        "2.0 GB",
			RAMGB:       2.0,
			Description: "Fastest, lightweight model (3.8B params)",
			Recommended: false,
		},
		{
			Name:        "mistral:7b",
			Size:        "4.1 GB",
			RAMGB:       4.5,
			Description: "Best quality/speed balance (7.2B params)",
			Recommended: true,
		},
		{
			Name:        "llama3.2:3b",
			Size:        "2.0 GB",
			RAMGB:       2.5,
			Description: "Fast, good quality (3B params)",
			Recommended: false,
		},
		{
			Name:        "llama3.1:8b",
			Size:        "4.7 GB",
			RAMGB:       5.0,
			Description: "High quality (8B params)",
			Recommended: false,
		},
		{
			Name:        "codellama:7b",
			Size:        "3.8 GB",
			RAMGB:       4.5,
			Description: "Optimized for code (7B params)",
			Recommended: false,
		},
		{
			Name:        "llama3.1:70b",
			Size:        "40 GB",
			RAMGB:       48.0,
			Description: "Best quality, very slow (70B params)",
			Recommended: false,
		},
	}
}

// FilterByRAM returns models that can run with given RAM
func FilterByRAM(availableRAM float64) []Model {
	models := AllModels()
	var compatible []Model

	for _, m := range models {
		if m.RAMGB <= availableRAM {
			compatible = append(compatible, m)
		}
	}

	return compatible
}

// GetRecommended returns the recommended model for given RAM
func GetRecommended(availableRAM float64) *Model {
	compatible := FilterByRAM(availableRAM)

	// First try to find recommended model that fits
	for _, m := range compatible {
		if m.Recommended {
			return &m
		}
	}

	// Otherwise return largest compatible model
	if len(compatible) > 0 {
		return &compatible[len(compatible)-1]
	}

	return nil
}

// FormatStatus returns a status indicator for a model given available RAM
func FormatStatus(model Model, availableRAM float64) string {
	if model.RAMGB <= availableRAM {
		if model.Recommended {
			return "✅ ⭐ Recommended"
		}
		return "✅ Compatible"
	}
	return fmt.Sprintf("❌ Needs %.1f GB (have %.1f GB)", model.RAMGB, availableRAM)
}
