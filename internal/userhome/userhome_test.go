package userhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirCortexHomeRedirect(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if got != tmp {
		t.Errorf("Dir() = %q, want %q (CORTEX_HOME redirect)", got, tmp)
	}
}

func TestDirFallsBackToDotCortexUnderHome(t *testing.T) {
	t.Setenv("CORTEX_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available in this environment: %v", err)
	}

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	want := filepath.Join(home, ".cortex")
	if got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestPathJoinsUnderResolvedDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	got, err := Path("config.json")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(tmp, "config.json")
	if got != want {
		t.Errorf("Path(\"config.json\") = %q, want %q", got, want)
	}
}
