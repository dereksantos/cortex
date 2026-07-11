// bootstrap_persist.go — M1.3: persist a BackendResolver.Resolve()
// result to the user config, and the ConfigProbe implementation that
// reads it back so a second bootstrap run's config stage short-circuits
// the rest of the chain (GOAL.md §6 M1.3, §3 P1 backend chain).
package main

import (
	"encoding/json"
	"fmt"

	"github.com/dereksantos/cortex/pkg/llm"
)

// PersistBackend writes a resolved backend to the user config file at
// path via read-modify-write (configwrite.go): the file's other
// top-level fields, and other roles under "models", survive byte-for-
// byte (GOAL.md §2). Only "backend.type" and, when b.Model is set,
// "models.<roleCode>.model" are touched.
func PersistBackend(path string, b ResolvedBackend) error {
	doc, err := readJSONDoc(path)
	if err != nil {
		return fmt.Errorf("failed to persist resolved backend: %w", err)
	}
	if err := setJSONPath(doc, []string{"backend", "type"}, string(b.Backend)); err != nil {
		return fmt.Errorf("failed to persist resolved backend: %w", err)
	}
	if b.Model != "" {
		if err := setJSONPath(doc, []string{"models", roleCode, "model"}, b.Model); err != nil {
			return fmt.Errorf("failed to persist resolved backend: %w", err)
		}
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return fmt.Errorf("failed to persist resolved backend: %w", err)
	}
	return nil
}

// fileConfigProbe is the concrete ConfigProbe (chain stage 1): it reads
// the user config file directly — not through LoadConfig/mergeConfig,
// so it sees exactly what PersistBackend last wrote — and reports a hit
// whenever a non-empty backend.type is present.
type fileConfigProbe struct {
	Path string
}

func (p fileConfigProbe) Probe() (ResolvedBackend, bool) {
	doc, err := readJSONDoc(p.Path)
	if err != nil {
		return ResolvedBackend{}, false
	}
	backendRaw, ok := doc["backend"]
	if !ok {
		return ResolvedBackend{}, false
	}
	var backend struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(backendRaw, &backend); err != nil || backend.Type == "" {
		return ResolvedBackend{}, false
	}

	model := ""
	if modelsRaw, ok := doc["models"]; ok {
		var models map[string]struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(modelsRaw, &models); err == nil {
			model = models[roleCode].Model
		}
	}
	return ResolvedBackend{Source: "config", Backend: llm.Backend(backend.Type), Model: model}, true
}
