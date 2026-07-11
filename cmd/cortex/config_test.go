package main

import (
	"path/filepath"
	"testing"
)

// TestUserConfigPathRoutesThroughUserhome pins that userConfigPath() is
// derived from internal/userhome's resolver rather than duplicating the
// $CORTEX_HOME / os.UserHomeDir lookup inline: redirecting CORTEX_HOME to
// a temp dir must redirect userConfigPath() too.
func TestUserConfigPathRoutesThroughUserhome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	got := userConfigPath()
	want := filepath.Join(tmp, "config.json")
	if got != want {
		t.Errorf("userConfigPath() = %q, want %q", got, want)
	}
}
