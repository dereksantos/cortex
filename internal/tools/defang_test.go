package tools

import (
	"strings"
	"testing"
)

func TestDefangControlTokensBreaksLiterals(t_ *testing.T) {
	// Sourced from pkg/llm/xml_tool_calls.go — the exact literals a study reads
	// when answering "how are tool calls parsed", and that poison a reasoner's
	// output into a proxy peg-native 500.
	cases := []string{
		"<function=read_file>{}</function>",
		"wrapped in <tool_call>...</tool_call>",
		"a <|python_tag|> sentinel",
		"north emits <|END_ACTION|> at the end",
	}
	for _, in := range cases {
		out := DefangControlTokens(in)
		// The token text must survive (still legible to the model)…
		if !strings.Contains(out, "function") && !strings.Contains(out, "tool_call") &&
			!strings.Contains(out, "python_tag") && !strings.Contains(out, "END_ACTION") {
			t_.Errorf("defang stripped the token text entirely: %q -> %q", in, out)
		}
		// …but the exact delimiter literal must be gone.
		for _, tok := range controlTokenOpeners {
			if strings.Contains(in, tok) && strings.Contains(out, tok) {
				t_.Errorf("defang left the literal %q intact in %q", tok, out)
			}
		}
	}
}

func TestDefangControlTokensNoopOnPlainText(t_ *testing.T) {
	for _, in := range []string{"", "func Resolve() {}", "a < b && c > d", "no markup here"} {
		if out := DefangControlTokens(in); out != in {
			t_.Errorf("plain text must pass through unchanged: %q -> %q", in, out)
		}
	}
}
