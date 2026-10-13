package main

import (
	"strings"
	"testing"
)

func TestCompletionScriptsNameEverySubcommand(t *testing.T) {
	names, _ := completionCommands()
	if len(names) == 0 {
		t.Fatal("no subcommands derived from usageLines")
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		script, ok := completionScript(shell)
		if !ok {
			t.Fatalf("%s: no script", shell)
		}
		for _, name := range names {
			if !strings.Contains(script, name) {
				t.Errorf("%s script is missing subcommand %q", shell, name)
			}
		}
	}
}

func TestCompletionCommandsOmitTheREPLLine(t *testing.T) {
	names, _ := completionCommands()
	for _, name := range names {
		if name == "interactive" || name == "cortex" {
			t.Errorf("REPL description leaked in as subcommand %q", name)
		}
	}
	if !containsString(names, "completion") {
		t.Error("completion itself is missing from the subcommand list")
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	if _, ok := completionScript("powershell"); ok {
		t.Error("unknown shell should not produce a script")
	}
}
