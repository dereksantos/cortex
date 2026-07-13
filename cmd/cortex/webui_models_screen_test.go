// webui_models_screen_test.go — M5.3e: the models screen. Renders GET
// /api/models' response (modelsResponse: role bindings + discovered fleet,
// live since M4.2c2a — serve_models.go) into a #models container, plus a
// scope switcher (user/project/session) and a per-role save button wired to
// M4.2c2b1/b2's PUT /api/models/{role}?scope=...[&project=...][&session=...]
// writes. No new endpoint needed — the read+write surface is already
// complete (M4.2c2a/b1/b2). Same structural-source-content testing
// convention as M5.3b/c/d (Decisions Log, set at M5.3b): this stdlib-only
// suite has no JS engine, so assertions are over the embedded JS/HTML source
// text, not executed DOM. The endpoints themselves are already
// httptest-covered by serve_models_test.go.
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func TestModelsScreenJSFetchesModelsEndpoint(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "models.js")
	if err != nil {
		t.Fatalf("ReadFile(models.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/models") {
		t.Error("models.js does not fetch /api/models")
	}
}

func TestModelsScreenIndexHTMLHasModelsContainer(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `id="models"`) {
		t.Error("index.html does not declare a #models container for the screen's rendered output")
	}
}

func TestIndexHTMLLoadsAppJSBeforeModelsJS(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "index.html")
	if err != nil {
		t.Fatalf("ReadFile(index.html): %v", err)
	}
	src := string(data)
	appIdx := strings.Index(src, "app.js")
	modelsIdx := strings.Index(src, "models.js")
	if appIdx == -1 {
		t.Fatal("index.html does not load app.js")
	}
	if modelsIdx == -1 {
		t.Fatal("index.html does not load models.js")
	}
	if appIdx > modelsIdx {
		t.Error("index.html loads app.js after models.js — models.js's authToken()/apiFetch() calls need app.js already loaded")
	}
}

func TestModelsScreenJSHasScopeSwitcherWithUserProjectSession(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "models.js")
	if err != nil {
		t.Fatalf("ReadFile(models.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, `createElement("select")`) {
		t.Error("models.js does not create a <select> element for the scope switcher")
	}
	for _, scope := range []string{"user", "project", "session"} {
		if !strings.Contains(src, `"`+scope+`"`) {
			t.Errorf("models.js does not offer scope %q", scope)
		}
	}
}

func TestModelsScreenJSPutsBindingOnSave(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "models.js")
	if err != nil {
		t.Fatalf("ReadFile(models.js): %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "/api/models/") {
		t.Error("models.js does not target a per-role /api/models/{role} path")
	}
	if !strings.Contains(src, `"PUT"`) {
		t.Error("models.js does not issue a PUT request to save a binding")
	}
	if !strings.Contains(src, "scope=") {
		t.Error("models.js does not pass ?scope= on the PUT request")
	}
	if !strings.Contains(src, "Authorization") {
		t.Error("models.js does not authenticate its PUT request")
	}
}

func TestModelsScreenJSReloadsAfterSave(t *testing.T) {
	data, err := fs.ReadFile(webUIFS(), "models.js")
	if err != nil {
		t.Fatalf("ReadFile(models.js): %v", err)
	}
	src := string(data)
	// loadModels() must appear more than once: once as the initial page load
	// call, once inside the per-role save handler's success path
	// re-rendering with the newly written binding.
	if strings.Count(src, "loadModels()") < 2 {
		t.Error("models.js's loadModels() is only called once — the save handler must call it again to re-render")
	}
}
