package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/study"
	"github.com/dereksantos/cortex/pkg/llm"
)

// TestASTEvalLive A/Bs the byte-grid vs the go/ast boundary producer on a real
// Go file (cmd/loop/main.go by default), measuring the thing AST chunking is
// supposed to win: digest accuracy. The headline metric is SYMBOL FIDELITY — of
// the code symbols the digest names (backtick-quoted identifiers), how many
// actually exist in the source. Hallucinated symbols (the RunShell/Search
// problem) drive it down.
//
// Gated behind CORTEX_AST_LIVE. Run with:
//
//	CORTEX_AST_LIVE=1 go test ./cmd/loop/ -run TestASTEvalLive -v -timeout 30m
//
// Overrides: CORTEX_WM_ENDPOINT / CORTEX_WM_MODEL / CORTEX_WM_KEY / CORTEX_WM_WINDOW
// / CORTEX_WM_PASSES, CORTEX_AST_FILE (default cmd/loop/main.go).
func TestASTEvalLive(t *testing.T) {
	if os.Getenv("CORTEX_AST_LIVE") == "" {
		t.Skip("set CORTEX_AST_LIVE=1 to run the AST-vs-byte-grid eval")
	}
	endpoint := envOr("CORTEX_WM_ENDPOINT", "http://localhost:11434/v1")
	model := envOr("CORTEX_WM_MODEL", "qwen2.5-coder:1.5b")
	window := envInt("CORTEX_WM_WINDOW", 32768)
	passes := envInt("CORTEX_WM_PASSES", 3)
	file := envOr("CORTEX_AST_FILE", "main.go")

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	// Ground truth: every identifier token present in the source.
	idRe := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)
	inSource := map[string]bool{}
	for _, w := range idRe.FindAllString(string(src), -1) {
		inSource[w] = true
	}

	provider := llm.NewOpenAICompatClient(llm.EndpointConfig{
		Name:    "ast-eval",
		BaseURL: endpoint,
		APIKey:  os.Getenv("CORTEX_WM_KEY"),
		Timeout: 10 * time.Minute,
	})
	provider.SetModel(model)
	provider.SetTemperature(0)
	provider.SetMaxTokens(study.CompletionTokenBudget(window, 0))

	goal := "Understand the architecture, responsibilities, and structure of this file to propose a clean refactoring into multiple files: name the key types, functions, and their responsibilities."

	type res struct {
		cov     float64
		g, f    int
		claimed int
		real    int // claimed symbols that exist in source
		digestB int
		err     string
	}
	// backtickIdent pulls single-identifier code spans (`Foo`, `parseX`) — how a
	// model references a symbol in prose.
	backtickIdent := regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{3,})`")

	run := func(useAST bool) res {
		req := study.StudyRequest{
			Path:   file,
			Goal:   goal,
			Window: study.SampleTokenBudget(window, 0),
			UseAST: useAST,
			Infer:  study.ProviderInfer(provider),
		}
		r := res{}
		out, rerr := study.StudyLoop(context.Background(), req, study.ModelCurator{Provider: provider}, passes)
		if rerr != nil {
			r.err = rerr.Error()
			return r
		}
		r.cov = 100 * out.CoveragePct
		r.g, r.f, _ = scoreGroundedness(string(src), "code", out)
		digest := strings.Join(out.Digests, "\n")
		r.digestB = len(digest)
		seen := map[string]bool{}
		for _, m := range backtickIdent.FindAllStringSubmatch(digest, -1) {
			tok := m[1]
			if seen[tok] {
				continue
			}
			seen[tok] = true
			r.claimed++
			if inSource[tok] {
				r.real++
			}
		}
		return r
	}

	byteRes := run(false)
	astRes := run(true)

	fmt.Printf("\n--- AST vs byte-grid (model=%s, window=%d, passes=%d, file=%s) ---\n", model, window, passes, file)
	fmt.Printf("%-10s %6s %8s %9s %12s %9s\n", "producer", "cov%", "grounded", "failed", "symbols", "digestB")
	for _, c := range []struct {
		name string
		r    res
	}{{"byte-grid", byteRes}, {"ast", astRes}} {
		if c.r.err != "" {
			fmt.Printf("%-10s ERROR: %s\n", c.name, firstLine(c.r.err))
			continue
		}
		fid := 0.0
		if c.r.claimed > 0 {
			fid = 100 * float64(c.r.real) / float64(c.r.claimed)
		}
		fmt.Printf("%-10s %5.0f%% %8d %9d %4d/%-4d %2.0f%% %8d\n",
			c.name, c.r.cov, c.r.g, c.r.f, c.r.real, c.r.claimed, fid, c.r.digestB)
	}
	fmt.Println("symbols = real/claimed backtick-quoted identifiers (higher fidelity = fewer hallucinated symbols)")
}
