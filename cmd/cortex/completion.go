package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// completionCommands returns the subcommands in usageLines with the long flags
// each one takes, in the order usageLines lists them. The command spec is the
// text before the description, which starts after two or more spaces. Deriving
// both from usageLines means the scripts cannot miss a subcommand that the help
// text lists.
var longFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

func completionCommands() (names []string, flags map[string][]string) {
	flags = map[string][]string{}
	seen := map[string]bool{}
	for _, line := range usageLines {
		spec := strings.TrimSpace(strings.Split(line, "  ")[0])
		words := strings.Fields(spec)
		if len(words) < 2 {
			continue
		}
		name := words[1]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		for _, flag := range longFlag.FindAllString(spec, -1) {
			if !containsString(flags[name], flag) {
				flags[name] = append(flags[name], flag)
			}
		}
	}
	return names, flags
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// completionScript returns a static completion script for shell, or false when
// the shell is not one of bash, zsh or fish.
func completionScript(shell string) (string, bool) {
	names, flags := completionCommands()
	switch shell {
	case "bash":
		var cases strings.Builder
		for _, name := range names {
			if len(flags[name]) > 0 {
				fmt.Fprintf(&cases, "        %s) COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") ) ;;\n", name, strings.Join(flags[name], " "))
			}
		}
		return fmt.Sprintf(`_cortex() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "%s" -- "$cur") )
        return
    fi
    case "${COMP_WORDS[1]}" in
%s        *) COMPREPLY=() ;;
    esac
}
complete -F _cortex cortex
`, strings.Join(names, " "), cases.String()), true
	case "zsh":
		var cases strings.Builder
		for _, name := range names {
			if len(flags[name]) > 0 {
				fmt.Fprintf(&cases, "        %s) compadd -- %s ;;\n", name, strings.Join(flags[name], " "))
			}
		}
		return fmt.Sprintf(`#compdef cortex
_cortex() {
    if (( CURRENT == 2 )); then
        compadd -- %s
        return
    fi
    case "$words[2]" in
%s    esac
}
_cortex "$@"
`, strings.Join(names, " "), cases.String()), true
	case "fish":
		var out strings.Builder
		fmt.Fprintf(&out, "complete -c cortex -f -n '__fish_use_subcommand' -a '%s'\n", strings.Join(names, " "))
		for _, name := range names {
			for _, flag := range flags[name] {
				fmt.Fprintf(&out, "complete -c cortex -f -n '__fish_seen_subcommand_from %s' -l %s\n", name, strings.TrimPrefix(flag, "--"))
			}
		}
		return out.String(), true
	}
	return "", false
}

// runCompletionCLI prints the completion script for `cortex completion <shell>`.
func runCompletionCLI(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cortex completion <bash|zsh|fish>")
		os.Exit(2)
	}
	script, ok := completionScript(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "cortex completion: unknown shell %q (use bash, zsh or fish)\n", args[0])
		os.Exit(2)
	}
	fmt.Print(script)
}
