package tools

import "strings"

// defang.go neutralizes tool-call control-token literals in tool-result content
// before it re-enters a model's context. Source files that IMPLEMENT a tool-call
// parser (pkg/llm/xml_tool_calls.go, json_tool_calls.go) contain literal control
// sequences like `<function=NAME>`, `</function>`, and `<tool_call>`. When a study
// reads those files to answer "how are tool calls parsed", a reasoning model
// ingests the literal tokens and reproduces them in its output — and a proxy whose
// response parser scans for native tool-call delimiters (LiteLLM's PEG grammar)
// then 500s on "output that does not match the expected peg-native format". The
// fix is to break the exact byte sequence at its source: insert a zero-width space
// after the opening angle so the token stays human- and model-legible (the study
// still sees `<function=…>` and can describe the parser) but is no longer the
// literal delimiter the proxy scans for. Purely a robustness transform on what the
// model reads — it changes no tool behavior and hides no information.

// zwsp is a zero-width space: invisible, but it makes the control sequence
// byte-distinct from the delimiter a tool-call parser matches.
const zwsp = "​"

// controlTokenOpeners are the literal lead-ins of tool-call delimiters that a
// response parser keys on. We split each at its opening angle and rejoin with a
// zero-width space, defanging both `<tag…>` and `</tag…>` forms.
var controlTokenOpeners = []string{
	"<function=",
	"<function ",
	"<function>",
	"</function>",
	"<tool_call>",
	"</tool_call>",
	"<tool_call ",
	"<|python_tag|>",
	"<|END_ACTION|>",
	"<|begin_of_action|>",
	"<|end_of_action|>",
}

// DefangControlTokens returns s with every tool-call control-token literal made
// byte-distinct (a zero-width space inserted after the leading `<`). Idempotent in
// practice: an already-defanged token no longer matches the un-defanged literal.
func DefangControlTokens(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	for _, tok := range controlTokenOpeners {
		if !strings.Contains(s, tok) {
			continue
		}
		// Insert the ZWSP right after the leading '<' (or '<|').
		var defanged string
		if strings.HasPrefix(tok, "<|") {
			defanged = "<|" + zwsp + tok[2:]
		} else {
			defanged = "<" + zwsp + tok[1:]
		}
		s = strings.ReplaceAll(s, tok, defanged)
	}
	return s
}
