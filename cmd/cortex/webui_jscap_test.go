// webui_jscap_test.go — M5.3a: mechanically bounds the web UI's hand-written
// JavaScript (GOAL.md §3 P5: "JS is fetch/render/SSE-append only, enforced
// mechanically (M5.3 size caps)"; §6 M5.3: "a Go test over the embedded FS
// asserts each .js file ≤ 300 lines and total JS ≤ 1200 lines"). This is a
// standing regression guard, not a one-time check: as the four screens
// (dashboard/session/landscape/models) grow real fetch/render/SSE-append
// logic in later M5.3 splits, this test is what keeps that logic from
// silently growing into a framework — the caps apply to whatever .js files
// end up under cmd/cortex/webui/, not a fixed enumerated list.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

const (
	maxJSFileLines  = 300
	maxTotalJSLines = 1200
)

func TestWebUIJavaScriptSizeCaps(t *testing.T) {
	total := 0
	err := fs.WalkDir(webUIFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		data, err := fs.ReadFile(webUIFS(), path)
		if err != nil {
			return err
		}
		lines := strings.Count(string(data), "\n") + 1
		if lines > maxJSFileLines {
			t.Errorf("%s: %d lines, want <= %d (per-file cap)", path, lines, maxJSFileLines)
		}
		total += lines
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk embedded webui FS: %v", err)
	}
	if total > maxTotalJSLines {
		t.Errorf("total JS lines across embedded assets = %d, want <= %d", total, maxTotalJSLines)
	}
}
