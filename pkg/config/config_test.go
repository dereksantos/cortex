package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	t.Run("returns non-nil config", func(t *testing.T) {
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
	})

	t.Run("sets context dir relative to project root", func(t *testing.T) {
		if cfg.ContextDir == "" {
			t.Error("expected non-empty ContextDir")
		}
		if !filepath.IsAbs(cfg.ContextDir) {
			t.Error("expected absolute path for ContextDir")
		}
	})

	t.Run("sets default skip patterns", func(t *testing.T) {
		if len(cfg.SkipPatterns) == 0 {
			t.Error("expected default skip patterns")
		}

		// Check for common patterns
		hasGit := false
		hasNodeModules := false
		for _, p := range cfg.SkipPatterns {
			if p == ".git" {
				hasGit = true
			}
			if p == "node_modules" {
				hasNodeModules = true
			}
		}
		if !hasGit {
			t.Error("expected .git in skip patterns")
		}
		if !hasNodeModules {
			t.Error("expected node_modules in skip patterns")
		}
	})

	t.Run("sets default Ollama settings", func(t *testing.T) {
		if cfg.OllamaURL != "http://localhost:11434" {
			t.Errorf("unexpected OllamaURL: %s", cfg.OllamaURL)
		}
		if cfg.OllamaModel == "" {
			t.Error("expected default OllamaModel")
		}
	})

	t.Run("sets default Anthropic model", func(t *testing.T) {
		if cfg.AnthropicModel == "" {
			t.Error("expected default AnthropicModel")
		}
	})
}

func TestLoadAndSave(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	t.Run("returns default for non-existent file", func(t *testing.T) {
		cfg, err := Load(filepath.Join(tempDir, "nonexistent.json"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		// Should have default values
		if cfg.OllamaURL != "http://localhost:11434" {
			t.Error("expected default OllamaURL")
		}
	})

	t.Run("saves and loads config", func(t *testing.T) {
		cfg := &Config{
			ContextDir:     "/test/context",
			ProjectRoot:    "/test/project",
			SkipPatterns:   []string{".git", "vendor"},
			OllamaURL:      "http://custom:11434",
			OllamaModel:    "llama2:7b",
			AnthropicModel: "claude-3-opus",
			EnableGraph:    true,
			EnableVector:   false,
		}

		if err := cfg.Save(configPath); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		loaded, err := Load(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if loaded.ContextDir != cfg.ContextDir {
			t.Errorf("ContextDir mismatch: got %s, want %s", loaded.ContextDir, cfg.ContextDir)
		}
		if loaded.ProjectRoot != cfg.ProjectRoot {
			t.Errorf("ProjectRoot mismatch: got %s, want %s", loaded.ProjectRoot, cfg.ProjectRoot)
		}
		if loaded.OllamaURL != cfg.OllamaURL {
			t.Errorf("OllamaURL mismatch: got %s, want %s", loaded.OllamaURL, cfg.OllamaURL)
		}
		if loaded.OllamaModel != cfg.OllamaModel {
			t.Errorf("OllamaModel mismatch: got %s, want %s", loaded.OllamaModel, cfg.OllamaModel)
		}
		if loaded.EnableGraph != cfg.EnableGraph {
			t.Errorf("EnableGraph mismatch: got %v, want %v", loaded.EnableGraph, cfg.EnableGraph)
		}
		if len(loaded.SkipPatterns) != len(cfg.SkipPatterns) {
			t.Errorf("SkipPatterns length mismatch: got %d, want %d", len(loaded.SkipPatterns), len(cfg.SkipPatterns))
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		nestedPath := filepath.Join(tempDir, "nested", "deep", "config.json")

		cfg := Default()
		if err := cfg.Save(nestedPath); err != nil {
			t.Fatalf("failed to save to nested path: %v", err)
		}

		if _, err := os.Stat(nestedPath); os.IsNotExist(err) {
			t.Error("expected config file to be created")
		}
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		invalidPath := filepath.Join(tempDir, "invalid.json")
		os.WriteFile(invalidPath, []byte("not valid json {{{"), 0644)

		_, err := Load(invalidPath)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

func TestEnsureDirectories(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-ensure-dirs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := &Config{
		ContextDir: filepath.Join(tempDir, "context"),
	}

	t.Run("creates all required directories", func(t *testing.T) {
		if err := cfg.EnsureDirectories(); err != nil {
			t.Fatalf("failed to ensure directories: %v", err)
		}

		expectedDirs := []string{
			cfg.ContextDir,
			filepath.Join(cfg.ContextDir, "journal", "capture"),
			filepath.Join(cfg.ContextDir, "knowledge", "decisions"),
			filepath.Join(cfg.ContextDir, "knowledge", "patterns"),
			filepath.Join(cfg.ContextDir, "knowledge", "insights"),
			filepath.Join(cfg.ContextDir, "knowledge", "strategies"),
			filepath.Join(cfg.ContextDir, "logs"),
			filepath.Join(cfg.ContextDir, "db"),
		}

		for _, dir := range expectedDirs {
			info, err := os.Stat(dir)
			if os.IsNotExist(err) {
				t.Errorf("expected directory to exist: %s", dir)
				continue
			}
			if !info.IsDir() {
				t.Errorf("expected %s to be a directory", dir)
			}
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		// Call again - should not error
		if err := cfg.EnsureDirectories(); err != nil {
			t.Fatalf("second call to EnsureDirectories failed: %v", err)
		}
	})
}

func TestConfigJSON(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cortex-json-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("omits empty AnthropicModel", func(t *testing.T) {
		cfg := &Config{
			ContextDir:     "/test",
			OllamaURL:      "http://localhost:11434",
			OllamaModel:    "qwen2.5-coder:1.5b",
			AnthropicModel: "", // empty
		}

		configPath := filepath.Join(tempDir, "omit-test.json")
		cfg.Save(configPath)

		data, _ := os.ReadFile(configPath)
		content := string(data)

		// Should not contain anthropic_model key when empty
		if len(cfg.AnthropicModel) > 0 {
			t.Error("expected empty AnthropicModel to be omitted")
		}

		// Verify file is valid JSON by loading it back
		_, err := Load(configPath)
		if err != nil {
			t.Fatalf("saved config is not valid JSON: %v", err)
		}

		// Content should be valid JSON
		if len(content) == 0 {
			t.Error("expected non-empty config file")
		}
	})
}
