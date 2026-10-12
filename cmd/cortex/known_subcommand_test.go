// known_subcommand_test.go — pins knownSubcommand() (the issue #262 guard's
// allow-list) to usageLines, so the guard and the documented subcommand list
// cannot drift apart.
package main

import (
	"strings"
	"testing"
)

func TestKnownSubcommandsCoverUsage(t *testing.T) {
	for _, line := range usageLines {
		rest := strings.TrimPrefix(line, "cortex")
		if rest == "" || rest[0] != ' ' {
			continue
		}
		// The bare REPL line has no subcommand token: "cortex" + 2+ spaces.
		if strings.HasPrefix(rest, "  ") {
			continue
		}
		name := strings.Fields(rest)[0]
		if !knownSubcommand(name) {
			t.Errorf("usageLines documents %q but knownSubcommand() rejects it", name)
		}
	}
	if knownSubcommand("sutdy") {
		t.Errorf("knownSubcommand(%q) = true, want false", "sutdy")
	}
}
