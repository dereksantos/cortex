//go:build probe
// +build probe

// Diagnostic-only test. Excluded from normal `go test ./...` by the
// `probe` build tag. Run with:
//
//	go test -tags=probe -run TestProbe ./internal/eval/benchmarks/longmemeval/
//
// Purpose: hydrate one upstream LongMemEval question into a workdir,
// then run cortex_search the same way the in-process harness tool
// does, and dump the top results so we can see what the model
// actually sees.

package longmemeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/config"
)

func TestProbe_CortexSearchOnPostcardsQuestion(t *testing.T) {
	cachePath := os.Getenv("HOME") + "/.cortex/benchmarks/longmemeval/longmemeval_oracle.json"
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache %s: %v (run cortex eval --benchmark longmemeval once to populate)", cachePath, err)
	}
	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		t.Fatalf("parse: %v", err)
	}
	qid := "01493427" // "How many new postcards have I added"
	var q *Question
	for i := range questions {
		if questions[i].QuestionID == qid {
			q = &questions[i]
			break
		}
	}
	if q == nil {
		t.Fatalf("qid %s not in dataset", qid)
	}

	workdir := t.TempDir()
	storeDir := filepath.Join(workdir, ".cortex")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := hydrateHaystack(context.Background(), storeDir, *q); err != nil {
		t.Fatalf("hydrate: %v", err)
	}

	cfg := &config.Config{ContextDir: storeDir}
	store, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()
	cx, err := intcognition.New(store, nil, nil, cfg)
	if err != nil {
		t.Fatalf("intcognition.New: %v", err)
	}

	queries := []string{
		q.Question,
		"postcards added",
		"how many postcards",
		"25 postcards",
		"postcard collection count",
		"started collecting again",
	}

	for _, qry := range queries {
		fmt.Println("\n==========================================================")
		fmt.Printf("QUERY: %q\n", qry)
		res, err := cx.Retrieve(context.Background(), cognition.Query{Text: qry, Limit: 5}, cognition.Fast)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			continue
		}
		if res == nil || len(res.Results) == 0 {
			fmt.Println("(no results)")
			continue
		}
		for i, r := range res.Results {
			snip := r.Content
			if len(snip) > 250 {
				snip = snip[:250] + "..."
			}
			fmt.Printf("  [%d]  score=%.3f  category=%s\n        %s\n", i+1, r.Score, r.Category, snip)
		}
	}
}
