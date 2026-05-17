package cliout

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvocationFinishOkShape pins the success-row schema. Field names
// are the public contract analysis consumers parse.
func TestInvocationFinishOkShape(t *testing.T) {
	inv := NewInvocation("search", "Attend", "")
	row := inv.FinishOk()
	if row.Source != "cli" {
		t.Errorf("Source = %q, want cli", row.Source)
	}
	if row.Command != "search" {
		t.Errorf("Command = %q, want search", row.Command)
	}
	if row.CortexFunction != "Attend" {
		t.Errorf("CortexFunction = %q, want Attend", row.CortexFunction)
	}
	if row.TraceID == "" {
		t.Errorf("TraceID empty")
	}
	if !row.Ok {
		t.Errorf("Ok = false")
	}
	if row.ErrorCode != "" {
		t.Errorf("ErrorCode = %q, want empty", row.ErrorCode)
	}
	if row.Timestamp == "" {
		t.Errorf("Timestamp empty")
	}
}

// TestInvocationFinishErrCarriesCode pins that failures stamp the
// envelope error code on the row so consumers can pivot on it.
func TestInvocationFinishErrCarriesCode(t *testing.T) {
	inv := NewInvocation("embed", "Sense", "")
	row := inv.FinishErr(ErrCodeInvalidArgs)
	if row.Ok {
		t.Errorf("Ok = true on failure")
	}
	if row.ErrorCode != ErrCodeInvalidArgs {
		t.Errorf("ErrorCode = %q, want %q", row.ErrorCode, ErrCodeInvalidArgs)
	}
}

// TestWriteRowAppendsToWorkdir verifies that telemetry lands in
// <workdir>/.cortex/db/cell_results.jsonl when --workdir is set, even
// if .cortex doesn't exist yet (benchmarks point at fresh tempdirs).
func TestWriteRowAppendsToWorkdir(t *testing.T) {
	workdir := t.TempDir()
	inv := NewInvocation("search", "Attend", workdir)
	row := inv.FinishOk()
	if err := inv.WriteRow(row); err != nil {
		t.Fatalf("WriteRow: %v", err)
	}

	path := filepath.Join(workdir, ".cortex", "db", "cell_results.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open written file: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("no row written")
	}
	var got TelemetryRow
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	if got.Command != "search" {
		t.Errorf("Command = %q", got.Command)
	}
	if got.Source != "cli" {
		t.Errorf("Source = %q", got.Source)
	}
}

// TestWriteRowAppends — two rows from the same invocation land as two
// lines, in order. Ad-hoc loops (a script firing 10 searches) need
// reliable append semantics.
func TestWriteRowAppends(t *testing.T) {
	workdir := t.TempDir()
	for i := 0; i < 3; i++ {
		inv := NewInvocation("search", "Attend", workdir)
		if err := inv.WriteRow(inv.FinishOk()); err != nil {
			t.Fatalf("WriteRow %d: %v", i, err)
		}
	}
	path := filepath.Join(workdir, ".cortex", "db", "cell_results.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3", len(lines))
	}
}

// TestWriteRowSuppressedByEnv verifies CORTEX_NO_TELEMETRY=1 turns the
// write into a no-op without erroring.
func TestWriteRowSuppressedByEnv(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv(TelemetryDisableEnv, "1")
	inv := NewInvocation("search", "Attend", workdir)
	if err := inv.WriteRow(inv.FinishOk()); err != nil {
		t.Errorf("WriteRow returned error on opt-out: %v", err)
	}
	path := filepath.Join(workdir, ".cortex", "db", "cell_results.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("telemetry file exists despite opt-out: %v", err)
	}
}

// TestWriteRowSkipsUninitializedCwd verifies the cwd path (no --workdir)
// silently skips when the cwd doesn't have a .cortex/ tree — prevents
// littering a non-cortex user's home dir with stray telemetry.
func TestWriteRowSkipsUninitializedCwd(t *testing.T) {
	wd := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	inv := NewInvocation("search", "Attend", "")
	if err := inv.WriteRow(inv.FinishOk()); err != nil {
		t.Errorf("WriteRow returned error on uninitialized cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, ".cortex")); !os.IsNotExist(err) {
		t.Errorf(".cortex/ created in uninitialized dir: %v", err)
	}
}

// TestHasNoTelemetryFlag covers both forms (`--no-telemetry` standalone
// and `--no-telemetry=true`).
func TestHasNoTelemetryFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"absent", []string{"search", "auth"}, false},
		{"standalone", []string{"search", "--no-telemetry", "auth"}, true},
		{"with value", []string{"search", "--no-telemetry=true"}, true},
		{"unrelated", []string{"search", "--workdir", "/tmp"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasNoTelemetryFlag(c.args); got != c.want {
				t.Errorf("HasNoTelemetryFlag(%v) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}

// TestStripNoTelemetry verifies the flag is removed cleanly (so the
// downstream command's flag parser doesn't reject it as unknown).
func TestStripNoTelemetry(t *testing.T) {
	args := []string{"search", "--no-telemetry", "--workdir", "/tmp", "auth", "--no-telemetry=true"}
	got := StripNoTelemetry(args)
	want := []string{"search", "--workdir", "/tmp", "auth"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWorkdirFromArgs covers the three flag forms.
func TestWorkdirFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"long form spaced", []string{"--workdir", "/tmp/a"}, "/tmp/a"},
		{"short form", []string{"-w", "/tmp/b"}, "/tmp/b"},
		{"equals", []string{"--workdir=/tmp/c"}, "/tmp/c"},
		{"absent", []string{"--limit", "5"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WorkdirFromArgs(c.args); got != c.want {
				t.Errorf("WorkdirFromArgs(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

// TestCortexFunctionFor covers the documented mapping. New commands
// added without updating CortexFunctionFor will return "" which the
// row serializer omits — silent miss is intentional, so this test is
// a regression gate for the listed entries only.
func TestCortexFunctionFor(t *testing.T) {
	cases := map[string]string{
		"search":   "Attend",
		"capture":  "Sense",
		"code":     "Decide",
		"journal":  "Maintain",
		"unknown":  "",
	}
	for cmd, want := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got := CortexFunctionFor(cmd); got != want {
				t.Errorf("CortexFunctionFor(%q) = %q, want %q", cmd, got, want)
			}
		})
	}
}
