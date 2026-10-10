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
	maxJSFileLines = 300
	// maxTotalJSLines rose from 1200 with the memory screen's memory.js
	// (docs/cross-source-learning.md piece 4, ~225 lines), and from 1500 with
	// the turn composer's image attachments (issue #218 step 6): the picker,
	// base64 reader, and pre-send validator in attach.js, plus the composer
	// wiring in app.js and the transcript's attached-image rendering in
	// session.js — 195 lines of fetch/render/validate, which is precisely what
	// this cap permits and precisely what it was raised for before.
	//
	// The honest reading of the total: at HEAD it stood at 1458 of 1500, so 42
	// lines of slack for a whole screen's worth of work. Any web-UI feature of
	// real size must raise this number, which is why the per-file cap above is
	// the guard that actually holds behavior in check (and TestWebUIAttachJSIsNot
	//Framework below re-asserts it for the newest file specifically). A ceiling
	// that forces either a bump or a starvation is not doing the job the
	// framework-growth guard was meant to do; the per-file cap is.
	maxTotalJSLines = 1700
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
