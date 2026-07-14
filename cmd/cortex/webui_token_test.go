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

// Structural checks (the M5.3 screen-test pattern) for the token
// ergonomics: the page remembers a ?token= visit in localStorage so later
// plain localhost visits keep working, and a missing token renders one
// clear banner instead of four raw 401 failures.
func TestAppJSRemembersTokenInLocalStorage(t *testing.T) {
	src := readWebUIAsset(t, "app.js")
	for _, want := range []string{
		`localStorage.setItem("cortex.token"`,
		`localStorage.getItem("cortex.token"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js should persist and recall the token via %s", want)
		}
	}
}

func TestAppJSRendersNoTokenBanner(t *testing.T) {
	src := readWebUIAsset(t, "app.js")
	if !strings.Contains(src, "renderNoTokenBanner") {
		t.Errorf("app.js should render a no-token banner instead of raw 401s")
	}
	if !strings.Contains(src, "cortex serve") {
		t.Errorf("the banner should tell the user where the tokened URL comes from (`cortex serve`)")
	}
}
