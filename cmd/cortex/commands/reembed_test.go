package commands

import "testing"

func TestReembedCommand_Name(t *testing.T) {
	cmd := &ReembedCommand{}
	if got := cmd.Name(); got != "reembed" {
		t.Errorf("Name() = %q, want %q", got, "reembed")
	}
}

func TestReembedCommand_Description(t *testing.T) {
	cmd := &ReembedCommand{}
	if got := cmd.Description(); got == "" {
		t.Error("Description() should not be empty")
	}
}
