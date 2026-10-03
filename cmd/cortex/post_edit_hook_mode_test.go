// post_edit_hook_mode_test.go covers the post-edit hook's MODE switch
// (issue #129 piece 2) at the config level: the CORTEX_POST_EDIT_HOOK env
// var overrides the config's tools.post_edit_hook, and an unrecognized
// value resolves to the safe off. The hook's behavioral matrix (mode ×
// trust) lives in internal/tools' TestHookModeByTrustAndCeiling — this
// package can only exercise the resolution the session installs.
package main

import (
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// cfgWithPostEditHook builds a *Config with the given tools.post_edit_hook
// value ("" = absent).
func cfgWithPostEditHook(v string) *Config {
	return &Config{Tools: ToolConfig{PostEditHook: v}}
}

func TestPostEditHookModeResolution(t *testing.T) {
	t.Run("env-overrides-config", func(t *testing.T) {
		t.Setenv("CORTEX_POST_EDIT_HOOK", "off")
		if got := cfgWithPostEditHook("all").postEditHookMode(); got != tools.HookModeOff {
			t.Fatalf("env off + config all: mode = %d, want off (env wins)", got)
		}
		t.Setenv("CORTEX_POST_EDIT_HOOK", "format")
		if got := cfgWithPostEditHook("all").postEditHookMode(); got != tools.HookModeFormat {
			t.Fatalf("env format + config all: mode = %d, want format (env wins)", got)
		}
		t.Setenv("CORTEX_POST_EDIT_HOOK", "all")
		if got := cfgWithPostEditHook("off").postEditHookMode(); got != tools.HookModeAll {
			t.Fatalf("env all + config off: mode = %d, want all (env wins — it OVERRIDES, not clamps)", got)
		}
	})

	t.Run("config-alone", func(t *testing.T) {
		if got := cfgWithPostEditHook("off").postEditHookMode(); got != tools.HookModeOff {
			t.Fatalf("config off: mode = %d, want off", got)
		}
		if got := cfgWithPostEditHook("format").postEditHookMode(); got != tools.HookModeFormat {
			t.Fatalf("config format: mode = %d, want format", got)
		}
		if got := cfgWithPostEditHook("all").postEditHookMode(); got != tools.HookModeAll {
			t.Fatalf("config all: mode = %d, want all", got)
		}
		if got := cfgWithPostEditHook("").postEditHookMode(); got != tools.HookModeAll {
			t.Fatalf("absent: mode = %d, want all (the default)", got)
		}
	})

	t.Run("unknown-env-value-resolves-to-off", func(t *testing.T) {
		t.Setenv("CORTEX_POST_EDIT_HOOK", "ful") // a typo
		if got := cfgWithPostEditHook("all").postEditHookMode(); got != tools.HookModeOff {
			t.Fatalf("env typo: mode = %d, want off (a typo must not enable a hook)", got)
		}
	})

	t.Run("unknown-config-value-resolves-to-off", func(t *testing.T) {
		if got := cfgWithPostEditHook("ful").postEditHookMode(); got != tools.HookModeOff {
			t.Fatalf("config typo: mode = %d, want off (a typo must not enable a hook)", got)
		}
	})

	t.Run("nil-config", func(t *testing.T) {
		var c *Config
		if got := c.postEditHookMode(); got != tools.HookModeAll {
			t.Fatalf("nil config: mode = %d, want all (the default)", got)
		}
	})
}
