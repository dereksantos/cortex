package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The retired daemon-era registry (deleted May 2026) also lived at
// <userhome>/projects.json with a different schema (id/path/git_remote —
// no "root"). A machine that ran that code still has the file, so the
// new registry must treat root-less entries as not-projects rather than
// surfacing hundreds of ghosts with empty roots (which the dashboard
// would then resolve against the process CWD).
func TestListSkipsLegacySchemaEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "projects.json")
	legacy := `[
  {"id": "old-1", "path": "/tmp/gone-1", "name": "old-1", "git_remote": "git@example.com:x.git"},
  {"name": "real", "root": "/Users/someone/eng/real"},
  {"id": "old-2", "path": "/tmp/gone-2", "name": "old-2"}
]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewAt(path)
	got, err := reg.List()
	if err != nil {
		t.Fatalf("List over a legacy-mixed file must not error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("List should keep only new-schema entries, got %+v", got)
	}

	if _, err := reg.Lookup("old-1"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("legacy entries must not be lookup-able, got %v", err)
	}
	if _, err := reg.Lookup("real"); err != nil {
		t.Errorf("the valid entry must survive: %v", err)
	}
}
