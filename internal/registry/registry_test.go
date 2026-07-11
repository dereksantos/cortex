package registry

import (
	"errors"
	"testing"
)

func TestFileRegistryCRUDRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	reg, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Create: an empty registry lists nothing.
	got, err := reg.List()
	if err != nil {
		t.Fatalf("List() on empty registry error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() on empty registry = %v, want empty", got)
	}

	blog := Project{Name: "blog", Root: "/home/derek/blog", Notes: "personal site"}
	api := Project{Name: "api", Root: "/home/derek/api"}
	if err := reg.Save(blog); err != nil {
		t.Fatalf("Save(blog) error = %v", err)
	}
	if err := reg.Save(api); err != nil {
		t.Fatalf("Save(api) error = %v", err)
	}

	// Read: a fresh Registry instance over the same path sees both saves —
	// proves the round-trip goes through the file, not in-memory state.
	reg2, err := New()
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	list, err := reg2.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() = %v, want 2 entries", list)
	}
	// List is sorted by name: api, blog.
	if list[0] != api || list[1] != blog {
		t.Errorf("List() = %+v, want [%+v %+v]", list, api, blog)
	}

	got1, err := reg2.Lookup("blog")
	if err != nil {
		t.Fatalf("Lookup(blog) error = %v", err)
	}
	if got1 != blog {
		t.Errorf("Lookup(blog) = %+v, want %+v", got1, blog)
	}

	// Update: Save with an existing name overwrites in place, not append.
	blogUpdated := Project{Name: "blog", Root: "/home/derek/blog", LastSession: "sess-1", Notes: "personal site v2"}
	if err := reg2.Save(blogUpdated); err != nil {
		t.Fatalf("Save(blogUpdated) error = %v", err)
	}
	reg3, err := New()
	if err != nil {
		t.Fatalf("third New() error = %v", err)
	}
	list, err = reg3.List()
	if err != nil {
		t.Fatalf("List() after update error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() after update = %v, want still 2 entries (update, not append)", list)
	}
	got2, err := reg3.Lookup("blog")
	if err != nil {
		t.Fatalf("Lookup(blog) after update error = %v", err)
	}
	if got2 != blogUpdated {
		t.Errorf("Lookup(blog) after update = %+v, want %+v", got2, blogUpdated)
	}

	// Delete: Remove drops the entry; a fresh instance confirms it's gone
	// from disk.
	if err := reg3.Remove("api"); err != nil {
		t.Fatalf("Remove(api) error = %v", err)
	}
	reg4, err := New()
	if err != nil {
		t.Fatalf("fourth New() error = %v", err)
	}
	list, err = reg4.List()
	if err != nil {
		t.Fatalf("List() after remove error = %v", err)
	}
	if len(list) != 1 || list[0].Name != "blog" {
		t.Fatalf("List() after Remove(api) = %v, want only blog", list)
	}
}

func TestFileRegistryLookupUnknownNameReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	reg, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := reg.Save(Project{Name: "blog", Root: "/home/derek/blog"}); err != nil {
		t.Fatalf("Save(blog) error = %v", err)
	}

	_, err = reg.Lookup("does-not-exist")
	if err == nil {
		t.Fatal("Lookup(does-not-exist) error = nil, want ErrProjectNotFound")
	}
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Lookup(does-not-exist) error = %v, want errors.Is(err, ErrProjectNotFound)", err)
	}
}

func TestFileRegistryRemoveUnknownNameReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	reg, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = reg.Remove("does-not-exist")
	if err == nil {
		t.Fatal("Remove(does-not-exist) error = nil, want ErrProjectNotFound")
	}
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Remove(does-not-exist) error = %v, want errors.Is(err, ErrProjectNotFound)", err)
	}
}

func TestFileRegistryLookupOnMissingFileReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	// No projects.json has ever been written under this temp home.
	reg, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = reg.Lookup("blog")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Lookup(blog) on missing file error = %v, want errors.Is(err, ErrProjectNotFound)", err)
	}
}

// Registry (the interface) is satisfied by *FileRegistry — pinned so a
// future signature drift is caught at compile time, not by a runtime test
// failure elsewhere.
var _ Registry = (*FileRegistry)(nil)
