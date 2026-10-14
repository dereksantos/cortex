package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestCompletionKeepsFileNames: installing the script must not take away the
// file-name completion a shell offers without it (`cortex study <path>`).
func TestCompletionKeepsFileNames(t *testing.T) {
	for _, tc := range []struct {
		shell, want string
	}{
		{"bash", "complete -o default -F _cortex cortex"},
		{"zsh", "*) _files ;;"},
		{"zsh", "else _files; fi"},
	} {
		t.Run(tc.shell+" "+tc.want, func(t *testing.T) {
			script, _ := completionScript(tc.shell)
			if !strings.Contains(script, tc.want) {
				t.Errorf("%s script is missing %q:\n%s", tc.shell, tc.want, script)
			}
		})
	}
	t.Run("fish flag rules allow files", func(t *testing.T) {
		script, _ := completionScript("fish")
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "__fish_seen_subcommand_from") && strings.Contains(line, " -f ") {
				t.Errorf("fish flag rule turns off file names: %s", line)
			}
		}
	})
}

// TestCompletionRegistersWhenSourced runs each script in its real shell, the
// way `source <(cortex completion <shell>)` installs it, and checks the
// completion function is registered for cortex. A shell that is not
// installed is skipped.
func TestCompletionRegistersWhenSourced(t *testing.T) {
	for _, tc := range []struct {
		shell string
		args  func(script, dir string) []string
		want  string
	}{
		{"bash", func(script, _ string) []string {
			return []string{"--norc", "--noprofile", "-c", "source " + script + " && complete -p cortex"}
		}, "-o default -F _cortex"},
		{"zsh", func(script, dir string) []string {
			return []string{"-f", "-c", "autoload -U compinit && compinit -u -d " + filepath.Join(dir, "zcompdump") +
				" && source " + script + " && print -r -- ${_comps[cortex]}"}
		}, "_cortex"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			bin, err := exec.LookPath(tc.shell)
			if err != nil {
				t.Skipf("%s not installed", tc.shell)
			}
			text, _ := completionScript(tc.shell)
			dir := t.TempDir()
			script := filepath.Join(dir, "cortex."+tc.shell)
			if err := os.WriteFile(script, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, tc.args(script, dir)...).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: sourcing the script failed: %v\n%s", tc.shell, err, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("%s: cortex completion not registered, want %q in:\n%s", tc.shell, tc.want, out)
			}
		})
	}
}
