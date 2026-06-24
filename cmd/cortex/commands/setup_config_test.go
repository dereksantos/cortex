package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectEnv(t *testing.T) {
	t.Setenv(orKeyEnv, "sk-or-present")
	t.Setenv(anthropicKeyEnv, "")
	// Probe answers only for the LiteLLM port.
	probe := func(addr string) bool { return addr == litellmAddr }

	det := detectEnv(probe)
	if !det.litellm || det.ollama {
		t.Errorf("port detection wrong: %+v", det)
	}
	if !det.openRouterKey || det.anthropicKey {
		t.Errorf("env-key detection wrong: %+v", det)
	}
}

func TestBuildConfig(t *testing.T) {
	noTags := func() []string { return nil }

	t.Run("free", func(t *testing.T) {
		c, err := buildConfig("free", "", "", noTags)
		if err != nil {
			t.Fatal(err)
		}
		if c.Backend.Type != "openrouter" || c.Backend.KeyEnv != orKeyEnv {
			t.Errorf("backend = %+v", c.Backend)
		}
		if c.Models["code"].Model != freeCodeModel || c.Models["study"].Model != freeStudyModel {
			t.Errorf("free models = %+v", c.Models)
		}
	})

	t.Run("openrouter pins supplied models", func(t *testing.T) {
		c, _ := buildConfig("openrouter", "anthropic/claude-sonnet", "", noTags)
		if c.Models["code"].Model != "anthropic/claude-sonnet" {
			t.Errorf("code = %q", c.Models["code"].Model)
		}
		if _, ok := c.Models["study"]; ok {
			t.Errorf("study should be unset, got %+v", c.Models)
		}
	})

	t.Run("local-litellm needs no pinned models", func(t *testing.T) {
		c, _ := buildConfig("local-litellm", "", "", noTags)
		if c.Backend.Type != "litellm" || c.Backend.Endpoint != litellmEndpoint {
			t.Errorf("backend = %+v", c.Backend)
		}
		if c.Models != nil {
			t.Errorf("litellm models should be nil (discovery), got %+v", c.Models)
		}
	})

	t.Run("local-ollama auto-fills from tags", func(t *testing.T) {
		tags := func() []string { return []string{"qwen2.5-coder:7b", "qwen2.5:7b-instruct"} }
		c, _ := buildConfig("local-ollama", "", "", tags)
		if c.Backend.Type != "ollama" {
			t.Errorf("backend = %+v", c.Backend)
		}
		if c.Models["code"].Model != "qwen2.5-coder:7b" {
			t.Errorf("code = %q, want the coder tag", c.Models["code"].Model)
		}
		if c.Models["study"].Model != "qwen2.5:7b-instruct" {
			t.Errorf("study = %q, want the instruct tag", c.Models["study"].Model)
		}
	})

	t.Run("explicit models override ollama auto-fill", func(t *testing.T) {
		called := false
		tags := func() []string { called = true; return []string{"x"} }
		c, _ := buildConfig("local-ollama", "my-code", "my-study", tags)
		if called {
			t.Error("tags should not be queried when models are supplied")
		}
		if c.Models["code"].Model != "my-code" || c.Models["study"].Model != "my-study" {
			t.Errorf("models = %+v", c.Models)
		}
	})

	t.Run("unknown backend errors", func(t *testing.T) {
		if _, err := buildConfig("nope", "", "", noTags); err == nil {
			t.Error("want error for unknown backend")
		}
	})
}

func TestPickOllamaModels(t *testing.T) {
	cases := []struct {
		name             string
		tags             []string
		wantCode, wantSt string
	}{
		{"coder + instruct", []string{"llama3", "qwen2.5-coder:7b", "qwen2.5:7b-instruct"}, "qwen2.5-coder:7b", "qwen2.5:7b-instruct"},
		{"only one model", []string{"llama3.1:8b"}, "llama3.1:8b", "llama3.1:8b"},
		{"empty", nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, study := pickOllamaModels(c.tags)
			if code != c.wantCode || study != c.wantSt {
				t.Errorf("got (%q,%q), want (%q,%q)", code, study, c.wantCode, c.wantSt)
			}
		})
	}
}

func TestOllamaTagsParse(t *testing.T) {
	get := func(url string) ([]byte, error) {
		return []byte(`{"models":[{"name":"qwen2.5-coder:7b"},{"name":"llama3.1:8b"}]}`), nil
	}
	tags := ollamaTags(ollamaEndpoint, get)
	if len(tags) != 2 || tags[0] != "qwen2.5-coder:7b" {
		t.Errorf("tags = %v", tags)
	}
}

func TestWriteToRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")
	if err := freeConfig().writeTo(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Must parse back into the same wire shape the loop reads.
	var got cfgFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
	if got.Backend.KeyEnv != orKeyEnv || got.Models["code"].Model != freeCodeModel {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("config should end with a trailing newline")
	}
}

func TestUserCfgPathHonorsCortexHome(t *testing.T) {
	t.Setenv("CORTEX_HOME", "/tmp/cortex-home-test")
	if got := userCfgPath(); got != "/tmp/cortex-home-test/config.json" {
		t.Errorf("userCfgPath = %q, want CORTEX_HOME-rooted path", got)
	}
}

func TestChooseBackend(t *testing.T) {
	t.Run("gated options: local shown only when detected", func(t *testing.T) {
		det := detected{litellm: true, ollama: true}
		var out strings.Builder
		in := bufio.NewReader(strings.NewReader("2\n"))
		got, err := chooseBackend(det, in, &out)
		if err != nil {
			t.Fatal(err)
		}
		// Option 1 is free, 2 is local-litellm (litellm listed before ollama).
		if got != "local-litellm" {
			t.Errorf("choice = %q, want local-litellm", got)
		}
		if !strings.Contains(out.String(), "Local LiteLLM") {
			t.Errorf("local option not offered: %q", out.String())
		}
	})

	t.Run("no local backends → only free + openrouter", func(t *testing.T) {
		var out strings.Builder
		in := bufio.NewReader(strings.NewReader("2\n"))
		got, _ := chooseBackend(detected{}, in, &out)
		if got != "openrouter" {
			t.Errorf("choice = %q, want openrouter (option 2 with no local)", got)
		}
		if strings.Contains(out.String(), "Local") {
			t.Errorf("local should not be offered: %q", out.String())
		}
	})

	t.Run("invalid selection errors", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("9\n"))
		if _, err := chooseBackend(detected{}, in, &strings.Builder{}); err == nil {
			t.Error("want error for out-of-range selection")
		}
	})
}
