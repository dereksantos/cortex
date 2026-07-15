package loops

import (
	"errors"
	"testing"
)

func TestFileStoreCRUDRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Create: an empty store lists nothing.
	got, err := st.List()
	if err != nil {
		t.Fatalf("List() on empty store error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() on empty store = %v, want empty", got)
	}

	nightly := Spec{Name: "nightly", Project: "blog", Prompt: "tidy TODOs", IntervalMinutes: 1440, Enabled: true}
	adhoc := Spec{Name: "adhoc", Project: "api", Prompt: "run lint fixups", Enabled: false}
	if err := st.Save(nightly); err != nil {
		t.Fatalf("Save(nightly) error = %v", err)
	}
	if err := st.Save(adhoc); err != nil {
		t.Fatalf("Save(adhoc) error = %v", err)
	}

	// Read: a fresh Store instance over the same path sees both saves —
	// proves the round-trip goes through the file, not in-memory state.
	st2, err := New()
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	list, err := st2.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() = %v, want 2 entries", list)
	}
	// List is sorted by name: adhoc, nightly.
	if list[0] != adhoc || list[1] != nightly {
		t.Errorf("List() = %+v, want [%+v %+v]", list, adhoc, nightly)
	}

	got1, err := st2.Lookup("nightly")
	if err != nil {
		t.Fatalf("Lookup(nightly) error = %v", err)
	}
	if got1 != nightly {
		t.Errorf("Lookup(nightly) = %+v, want %+v", got1, nightly)
	}

	// Update: Save with an existing name overwrites in place, not append.
	nightlyUpdated := Spec{Name: "nightly", Project: "blog", Prompt: "tidy TODOs v2", IntervalMinutes: 720, Enabled: true}
	if err := st2.Save(nightlyUpdated); err != nil {
		t.Fatalf("Save(nightlyUpdated) error = %v", err)
	}
	st3, err := New()
	if err != nil {
		t.Fatalf("third New() error = %v", err)
	}
	list, err = st3.List()
	if err != nil {
		t.Fatalf("List() after update error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() after update = %v, want still 2 entries (update, not append)", list)
	}
	got2, err := st3.Lookup("nightly")
	if err != nil {
		t.Fatalf("Lookup(nightly) after update error = %v", err)
	}
	if got2 != nightlyUpdated {
		t.Errorf("Lookup(nightly) after update = %+v, want %+v", got2, nightlyUpdated)
	}

	// Delete: Remove drops the entry; a fresh instance confirms it's gone
	// from disk.
	if err := st3.Remove("adhoc"); err != nil {
		t.Fatalf("Remove(adhoc) error = %v", err)
	}
	st4, err := New()
	if err != nil {
		t.Fatalf("fourth New() error = %v", err)
	}
	list, err = st4.List()
	if err != nil {
		t.Fatalf("List() after remove error = %v", err)
	}
	if len(list) != 1 || list[0].Name != "nightly" {
		t.Fatalf("List() after Remove(adhoc) = %v, want only nightly", list)
	}
}

func TestFileStoreLookupUnknownNameReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := st.Save(Spec{Name: "nightly", Project: "blog", Prompt: "tidy"}); err != nil {
		t.Fatalf("Save(nightly) error = %v", err)
	}

	_, err = st.Lookup("does-not-exist")
	if err == nil {
		t.Fatal("Lookup(does-not-exist) error = nil, want ErrLoopNotFound")
	}
	if !errors.Is(err, ErrLoopNotFound) {
		t.Errorf("Lookup(does-not-exist) error = %v, want errors.Is(err, ErrLoopNotFound)", err)
	}
}

func TestFileStoreRemoveUnknownNameReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = st.Remove("does-not-exist")
	if err == nil {
		t.Fatal("Remove(does-not-exist) error = nil, want ErrLoopNotFound")
	}
	if !errors.Is(err, ErrLoopNotFound) {
		t.Errorf("Remove(does-not-exist) error = %v, want errors.Is(err, ErrLoopNotFound)", err)
	}
}

func TestFileStoreLookupOnMissingFileReturnsTypedError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	// No loops.json has ever been written under this temp home.
	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = st.Lookup("nightly")
	if !errors.Is(err, ErrLoopNotFound) {
		t.Errorf("Lookup(nightly) on missing file error = %v, want errors.Is(err, ErrLoopNotFound)", err)
	}
}

func TestFileStoreSaveRejectsCadenceBelowFiveMinuteFloor(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	tooFast := Spec{Name: "tooFast", Project: "blog", Prompt: "spam it", IntervalMinutes: 3, Enabled: true}
	err = st.Save(tooFast)
	if err == nil {
		t.Fatal("Save(tooFast) error = nil, want ErrCadenceTooLow")
	}
	if !errors.Is(err, ErrCadenceTooLow) {
		t.Errorf("Save(tooFast) error = %v, want errors.Is(err, ErrCadenceTooLow)", err)
	}

	// The rejected spec must not have been persisted.
	if _, lookupErr := st.Lookup("tooFast"); !errors.Is(lookupErr, ErrLoopNotFound) {
		t.Errorf("Lookup(tooFast) after rejected Save error = %v, want ErrLoopNotFound (never persisted)", lookupErr)
	}
}

func TestFileStoreSaveAllowsExactlyFiveMinuteFloor(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	atFloor := Spec{Name: "atFloor", Project: "blog", Prompt: "just fast enough", IntervalMinutes: CadenceFloorMinutes, Enabled: true}
	if err := st.Save(atFloor); err != nil {
		t.Fatalf("Save(atFloor) at the exact 15-minute floor error = %v, want nil", err)
	}
}

func TestFileStoreSaveAllowsManualOnlyZeroInterval(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORTEX_HOME", tmp)

	st, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	manual := Spec{Name: "manual", Project: "blog", Prompt: "run-now only", IntervalMinutes: 0, Enabled: false}
	if err := st.Save(manual); err != nil {
		t.Fatalf("Save(manual) with zero interval (manual-only) error = %v, want nil", err)
	}
}

// Store (the interface) is satisfied by *FileStore — pinned so a future
// signature drift is caught at compile time, not by a runtime test failure
// elsewhere.
var _ Store = (*FileStore)(nil)
