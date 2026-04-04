package commands

import "testing"

func TestMCPCommand_Name(t *testing.T) {
	cmd := &MCPCommand{}
	if got := cmd.Name(); got != "mcp" {
		t.Errorf("Name() = %q, want %q", got, "mcp")
	}
}

func TestMCPCommand_Description(t *testing.T) {
	cmd := &MCPCommand{}
	if got := cmd.Description(); got == "" {
		t.Error("Description() should not be empty")
	}
}
