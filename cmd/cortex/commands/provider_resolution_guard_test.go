package commands

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProviderResolutionGuard fails if any non-allowlisted file in
// cmd/cortex/commands/ reintroduces a direct call to
// llm.NewOllamaClient or llm.NewOpenRouterClient.
//
// Every runtime provider/embedder construction in this package must
// route through internal/llm.BuildProvider / BuildEmbedder so model id
// remains the single source of truth for routing (Phase 4 model_routes
// + slash-prefix → OpenRouter + bare → Ollama default). See
// docs/provider-resolution-refactor.md.
//
// Allowlist sites are tagged with a line-comment of the form
//
//	allowlist:llm.NewOllamaClient
//	allowlist:llm.NewOpenRouterClient
//
// somewhere within ~10 lines preceding the construction. New
// allowlist entries should land with a comment explaining why the
// site is genuinely not a runtime provider selection (first-run
// probe, catalog discovery, etc.).
func TestProviderResolutionGuard(t *testing.T) {
	bannedRe := regexp.MustCompile(`llm\.(NewOllamaClient|NewOpenRouterClient)\b`)
	allowRe := regexp.MustCompile(`allowlist:llm\.(NewOllamaClient|NewOpenRouterClient)`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	type violation struct {
		file string
		line int
		text string
	}
	var violations []violation

	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		scanner.Buffer(make([]byte, 1<<20), 1<<20)

		// Track recent allowlist comments by call type. A construction
		// is permitted when a matching `allowlist:llm.NewFoo` comment
		// appears within the previous `allowWindow` lines.
		const allowWindow = 10
		recent := make(map[string]int) // call name → line number of last allowlist marker

		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()

			if m := allowRe.FindStringSubmatch(line); m != nil {
				recent[m[1]] = lineNo
				continue
			}

			// Strip line comments and string literals so we don't trip on
			// references in doc comments. The cheap path: ignore any line
			// whose first non-whitespace chars are "//".
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}

			if m := bannedRe.FindStringSubmatch(line); m != nil {
				if at, ok := recent[m[1]]; ok && lineNo-at <= allowWindow {
					continue
				}
				violations = append(violations, violation{
					file: name,
					line: lineNo,
					text: trimmed,
				})
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan %s: %v", name, err)
		}
	}

	if len(violations) > 0 {
		var b strings.Builder
		b.WriteString("direct llm.NewOllamaClient / llm.NewOpenRouterClient call(s) outside the allowlist:\n")
		for _, v := range violations {
			b.WriteString("  ")
			b.WriteString(v.file)
			b.WriteString(":")
			b.WriteString(itoa(v.line))
			b.WriteString(": ")
			b.WriteString(v.text)
			b.WriteString("\n")
		}
		b.WriteString("\nRoute through internal/llm.BuildProvider or BuildEmbedder, or add an `allowlist:` comment within 10 lines explaining why this site is exempt. See docs/provider-resolution-refactor.md.")
		t.Fatal(b.String())
	}
}

// itoa converts an int to a string without dragging in strconv just
// for the error path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
