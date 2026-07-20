// webui_projects_view_test.go — the projects screen's cards/list view
// switcher. Same structural-source-content testing convention as the other
// webui_*_screen_test.go files (Decisions Log, set at M5.3b): this
// stdlib-only suite has no JS engine, so assertions are over the embedded
// JS/CSS source text, not executed DOM. No new Go endpoint: both views
// render off the existing dashboardViewModel (webui_dashboard.go).
package main

import (
	"io/fs"
	"strings"
	"testing"
)

func readWebUIAsset(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(webUIFS(), name)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", name, err)
	}
	return string(data)
}

// TestDashboardJSHasViewSwitcherPersistedToLocalStorage pins the
// cortex.projectsView localStorage key (the "same pattern as any other
// cortex.<setting> key" persistence) and that both view names are offered.
func TestDashboardJSHasViewSwitcherPersistedToLocalStorage(t *testing.T) {
	src := readWebUIAsset(t, "dashboard.js")
	if !strings.Contains(src, `"cortex.projectsView"`) {
		t.Error("dashboard.js does not persist the view choice under a cortex.projectsView localStorage key")
	}
	if !strings.Contains(src, "localStorage.getItem") || !strings.Contains(src, "localStorage.setItem") {
		t.Error("dashboard.js does not read/write localStorage for the view switcher")
	}
	for _, view := range []string{"cards", "list"} {
		if !strings.Contains(src, `"`+view+`"`) {
			t.Errorf("dashboard.js does not offer view %q", view)
		}
	}
}

// TestDashboardJSViewSwitcherReusesSegStyle pins that the switcher is built
// with the .seg class the models screen's design system already defines
// (app.css), not a bespoke control.
func TestDashboardJSViewSwitcherReusesSegStyle(t *testing.T) {
	src := readWebUIAsset(t, "dashboard.js")
	if !strings.Contains(src, `className: "seg"`) {
		t.Error("dashboard.js's view switcher does not reuse the .seg segmented-control style")
	}
}

// TestDashboardJSHasListViewRenderer pins that a list-view renderer exists
// and reuses the table.loops styling family, per app.css's shared
// table.projects/table.loops rule.
func TestDashboardJSHasListViewRenderer(t *testing.T) {
	src := readWebUIAsset(t, "dashboard.js")
	if !strings.Contains(src, "function renderProjectsList(") {
		t.Error("dashboard.js does not define a list-view renderer (renderProjectsList)")
	}
	if !strings.Contains(src, `className: "loops"`) {
		t.Error("dashboard.js's list view does not reuse the table.loops styling family")
	}
}

// TestDashboardJSCardsGridIsMultiProjectOnly pins that the responsive grid
// class is applied conditionally (multi-project only) rather than always,
// and that the session cap tightens to 4 in that layout.
func TestDashboardJSCardsGridIsMultiProjectOnly(t *testing.T) {
	src := readWebUIAsset(t, "dashboard.js")
	if !strings.Contains(src, `"cards" + (multi ? " grid" : "")`) {
		t.Error("dashboard.js does not conditionally apply the grid class only when there are multiple projects")
	}
	if !strings.Contains(src, "multi ? 4 : 8") {
		t.Error("dashboard.js does not cap the per-card session list at 4 rows in the multi-project grid (8 single-project)")
	}
}

// TestDashboardCSSHasResponsiveProjectsGrid pins the .cards.grid rule
// (responsive min-column-width grid) app.css needs for the multi-project
// cards layout.
func TestDashboardCSSHasResponsiveProjectsGrid(t *testing.T) {
	src := readWebUIAsset(t, "app.css")
	if !strings.Contains(src, ".cards.grid") {
		t.Error("app.css does not define a .cards.grid rule for the multi-project responsive grid")
	}
	if !strings.Contains(src, "minmax(340px") {
		t.Error("app.css's .cards.grid does not use a ~340px minimum column width")
	}
}

// TestDashboardListRowOpensNewestSessionOrCreates pins the shared
// openOrCreateSession logic the list view's row click and Open session
// button both use: navigate straight to the newest non-empty session when
// one exists, else fall back to the same POST-create flow the cards view's
// button uses.
func TestDashboardListRowOpensNewestSessionOrCreates(t *testing.T) {
	src := readWebUIAsset(t, "dashboard.js")
	if !strings.Contains(src, "function openOrCreateSession(") {
		t.Error("dashboard.js does not define a shared openOrCreateSession helper")
	}
	if !strings.Contains(src, "function newestNonEmpty(") {
		t.Error("dashboard.js does not resolve a project's newest non-empty session")
	}
	if strings.Count(src, "openOrCreateSession(") < 3 {
		t.Error("openOrCreateSession is not shared between the cards button, the list row click, and the list button")
	}
}
