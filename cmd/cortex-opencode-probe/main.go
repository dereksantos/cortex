// Throwaway probe: invokes `opencode run` against a free OpenRouter model
// to lock down the event-stream shape (event types, token/cost keys, file-
// edit reporting, exit reason) that OpenCodeHarness needs to parse.
//
// Usage:
//
//	go run ./cmd/cortex-opencode-probe
//
// Output: docs/opencode-probe.json — a JSON envelope holding the full
// stdout (the event stream itself), stderr, exit code, command, env-key
// names that were forwarded, and a snapshot of the scratch dir after the
// run so we can tell whether opencode edited the stub.
//
// Safe to delete once OpenCodeHarness has internalized the shape (TODO 2
// of docs/eval-harness-phase7-prompt.md).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	model        = "openrouter/openai/gpt-oss-20b:free"
	probePrompt  = "Implement the Greet function in hello.go so it returns 'Hello, ' + name + '!'. Edit the file."
	timeoutSecs  = 240
	stubFileName = "hello.go"
	stubContent  = `package main

import "fmt"

// Greet returns a greeting message. TODO: implement.
func Greet(name string) string {
	return "" // TODO
}

func main() {
	fmt.Println(Greet("world"))
}
`
)

type fileSnapshot struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Content string `json:"content"`
}

type envelope struct {
	Probe struct {
		Timestamp string   `json:"timestamp"`
		Model     string   `json:"model"`
		Prompt    string   `json:"prompt"`
		Command   []string `json:"command"`
	} `json:"probe"`
	ScratchDir     string         `json:"scratch_dir"`
	FilesBefore    []fileSnapshot `json:"files_before"`
	FilesAfter     []fileSnapshot `json:"files_after"`
	EnvForwarded   []string       `json:"env_forwarded"`
	ExitCode       int            `json:"exit_code"`
	DurationMs     int64          `json:"duration_ms"`
	TimedOut       bool           `json:"timed_out"`
	Stdout         string         `json:"stdout"`
	Stderr         string         `json:"stderr"`
	StdoutLineKind []string       `json:"stdout_line_kind,omitempty"`
}

func main() {
	if os.Getenv("OPEN_ROUTER_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "OPEN_ROUTER_API_KEY not set")
		os.Exit(2)
	}

	scratch, err := os.MkdirTemp("", "opencode-probe-")
	if err != nil {
		fatal("mkdir scratch", err)
	}
	defer os.RemoveAll(scratch)

	stubPath := filepath.Join(scratch, stubFileName)
	if err := os.WriteFile(stubPath, []byte(stubContent), 0o644); err != nil {
		fatal("write stub", err)
	}

	args := []string{
		"run",
		"--model", model,
		"--format", "json",
		"--dir", scratch,
		probePrompt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "opencode", args...)

	// opencode (via models.dev / SDKs) is expected to consume OPENROUTER_API_KEY.
	// Project env exports the underscore form OPEN_ROUTER_API_KEY — re-export.
	env := append(os.Environ(),
		"OPENROUTER_API_KEY="+os.Getenv("OPEN_ROUTER_API_KEY"),
	)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start).Milliseconds()

	out := envelope{}
	out.Probe.Timestamp = time.Now().UTC().Format(time.RFC3339)
	out.Probe.Model = model
	out.Probe.Prompt = probePrompt
	out.Probe.Command = append([]string{"opencode"}, args...)
	out.ScratchDir = scratch
	out.FilesBefore = []fileSnapshot{snapshotFile(stubPath)}
	out.FilesAfter = snapshotDir(scratch)
	out.EnvForwarded = []string{"OPENROUTER_API_KEY (from OPEN_ROUTER_API_KEY)"}
	out.DurationMs = elapsed
	out.Stdout = stdout.String()
	out.Stderr = stderr.String()
	out.TimedOut = ctx.Err() == context.DeadlineExceeded

	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			out.ExitCode = ee.ExitCode()
		} else {
			out.ExitCode = -1
		}
	}

	out.StdoutLineKind = summarizeLineKinds(stdout.String())

	if err := writeEnvelope("docs/opencode-probe.json", &out); err != nil {
		fatal("write envelope", err)
	}

	fmt.Fprintf(os.Stderr, "probe done: exit=%d duration=%dms stdout_bytes=%d stderr_bytes=%d\n",
		out.ExitCode, out.DurationMs, len(out.Stdout), len(out.Stderr))
	if runErr != nil && !out.TimedOut {
		fmt.Fprintf(os.Stderr, "run error: %v\n", runErr)
	}
}

func snapshotFile(p string) fileSnapshot {
	info, err := os.Stat(p)
	if err != nil {
		return fileSnapshot{Path: p}
	}
	b, _ := os.ReadFile(p)
	return fileSnapshot{Path: p, Bytes: int(info.Size()), Content: string(b)}
}

func snapshotDir(dir string) []fileSnapshot {
	var out []fileSnapshot
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		out = append(out, snapshotFile(p))
		return nil
	})
	return out
}

// summarizeLineKinds peeks at each stdout line and reports the first-level
// JSON "type" field (or "<non-json>") so the documentation step can see
// the distribution of event kinds without re-parsing the full stream.
func summarizeLineKinds(s string) []string {
	var kinds []string
	for _, line := range bytes.Split([]byte(s), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			kinds = append(kinds, "<non-json>")
			continue
		}
		if t, ok := obj["type"].(string); ok {
			kinds = append(kinds, t)
		} else {
			kinds = append(kinds, "<no-type>")
		}
	}
	return kinds
}

func writeEnvelope(path string, out *envelope) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func fatal(msg string, err error) {
	fmt.Fprintln(os.Stderr, msg+":", err)
	os.Exit(1)
}
