// webui.go — Phase 5 (M5.1): the web UI's static assets, embedded into the
// binary via go:embed so `cortex serve` is self-contained (GOAL.md §3 P5:
// "hand-written HTML/CSS/JS under go:embed... no node toolchain, no build
// step, no vendored framework"). This increment lands only the embed+serve
// plumbing (a placeholder index.html/app.js/app.css) — the real
// view-model-driven screens are M5.2/M5.3.
package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed webui
var webUIAssets embed.FS

// webUIFS returns the embedded asset tree rooted at cmd/cortex/webui, with
// the "webui/" prefix stripped so paths inside the FS match request paths
// verbatim (e.g. "index.html", "app.js"). Backed entirely by the compiled-in
// embed.FS — no filesystem access at request time, which is what makes the
// binary self-contained (M5.1's "no filesystem presence" requirement).
func webUIFS() fs.FS {
	sub, err := fs.Sub(webUIAssets, "webui")
	if err != nil {
		// Unreachable in practice: "webui" is a go:embed'd literal directory
		// that always exists at build time — fs.Sub only errors on a
		// malformed root name, not a missing one.
		panic("webui: embedded asset root missing: " + err.Error())
	}
	return sub
}

// webUIHandler serves the embedded web UI assets (index.html, app.js,
// app.css, …) via the stdlib's fs-backed static file server — no
// third-party static-asset dependency, per GOAL.md's no-new-deps non-goal.
func webUIHandler() http.Handler {
	return http.FileServerFS(webUIFS())
}
