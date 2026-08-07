// Command polyglotbench scores the cortex coding agent on the Go slice of
// the Exercism polyglot benchmark set (the exercise corpus Aider's polyglot
// benchmark uses).
//
// It is a benchmark driver, not part of the cortex binary — nothing here is
// imported by cmd/cortex. Drive it through scripts/bench/polyglot/run.sh,
// which pins the exercise checkout and builds the cortex binary first.
//
// Scoring is deterministic end to end. Each exercise ships unit tests, so the
// verdict is `go test ./...`'s exit code in a fresh copy of the exercise —
// there is no model judging anything, at any point.
//
// Per exercise:
//
//	stage a pristine copy (minus .meta/, which holds the reference solution)
//	give it a fresh .cortex/ — new session, empty memory, pinned model config
//	`cortex turn --json "<instructions + task frame>"`
//	`go test ./...` for the verdict
//	classify, then append the row to results.jsonl and fsync it
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// defaults describe the local fleet this benchmark was built against
// (chatterbox, a LiteLLM proxy in front of local llama.cpp servers). Every
// one is overridable; none is guessed at run time.
const (
	defaultEndpoint    = "http://chatterbox:4000"
	defaultBackendType = "litellm"
	defaultModel       = "qwen3-coder-q3"
	defaultStudyModel  = "study"
	defaultWindow      = 131072
	polyglotRepoURL    = "https://github.com/Aider-AI/polyglot-benchmark.git"
)

type options struct {
	srcRoot     string
	outRoot     string
	cortexBin   string
	runID       string
	exercises   string
	only        int
	model       string
	studyModel  string
	window      int
	temperature float64
	backendType string
	endpoint    string
	keyEnv      string
	keyService  string
	turnTimeout time.Duration
	testTimeout time.Duration
	list        bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "polyglotbench: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() *options {
	o := &options{}
	flag.StringVar(&o.srcRoot, "src", "", "pinned polyglot-benchmark checkout (required)")
	flag.StringVar(&o.outRoot, "out", "", "bench output root, one dir per run (required)")
	flag.StringVar(&o.cortexBin, "cortex", "", "path to the cortex binary under test (required)")
	flag.StringVar(&o.runID, "run-id", "", "run directory name (default: UTC timestamp)")
	flag.StringVar(&o.exercises, "exercise", "", "comma-separated exercise names to run (overrides --only)")
	flag.IntVar(&o.only, "only", 0, "run only the first N exercises in name order (0 = all)")
	flag.StringVar(&o.model, "model", defaultModel, "model id for the `code` role")
	flag.StringVar(&o.studyModel, "study-model", defaultStudyModel, "model id for the `study` role")
	flag.IntVar(&o.window, "window", defaultWindow, "context window, tokens")
	flag.Float64Var(&o.temperature, "temperature", 0, "sampling temperature")
	flag.StringVar(&o.backendType, "backend", defaultBackendType, "cortex backend.type")
	flag.StringVar(&o.endpoint, "endpoint", defaultEndpoint, "backend endpoint")
	flag.StringVar(&o.keyEnv, "key-env", "", "env var holding the backend API key (remote backends)")
	flag.StringVar(&o.keyService, "key-service", "", "keychain service holding the backend API key (remote backends)")
	flag.DurationVar(&o.turnTimeout, "timeout", 10*time.Minute, "per-exercise wall-clock budget for the cortex turn")
	flag.DurationVar(&o.testTimeout, "test-timeout", 2*time.Minute, "per-exercise budget for `go test ./...`")
	flag.BoolVar(&o.list, "list", false, "list the discovered exercises and exit")
	flag.Parse()
	return o
}

func run() error {
	o := parseFlags()

	all, err := DiscoverExercises(o.srcRoot)
	if err != nil {
		return err
	}
	if o.list {
		for _, e := range all {
			fmt.Println(e.Name)
		}
		return nil
	}
	for _, req := range []struct{ name, val string }{
		{"--src", o.srcRoot}, {"--out", o.outRoot}, {"--cortex", o.cortexBin},
	} {
		if req.val == "" {
			return fmt.Errorf("%s is required", req.name)
		}
	}

	var names []string
	if s := strings.TrimSpace(o.exercises); s != "" {
		for _, n := range strings.Split(s, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	selected, err := SelectExercises(all, names, o.only)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return errors.New("no exercises selected")
	}

	runID := o.runID
	if runID == "" {
		runID = time.Now().UTC().Format("20060102-150405")
	}
	runDir := filepath.Join(o.outRoot, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("failed to create run dir %s: %w", runDir, err)
	}
	transcripts := filepath.Join(runDir, "transcripts")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		return fmt.Errorf("failed to create transcripts dir: %w", err)
	}
	// One benchmark-owned CORTEX_HOME for the whole run: the user's real
	// ~/.cortex (registry, loops, memory, scan roots) must not influence the
	// score, and the run must not write into it.
	benchHome := filepath.Join(runDir, "cortex-home")
	if err := os.MkdirAll(benchHome, 0o755); err != nil {
		return fmt.Errorf("failed to create bench home: %w", err)
	}

	cortexBin, err := filepath.Abs(o.cortexBin)
	if err != nil {
		return fmt.Errorf("failed to resolve cortex binary: %w", err)
	}

	host, _ := os.Hostname()
	commit, dirty := gitHead(".")
	polyCommit, _ := gitHead(o.srcRoot)
	meta := RunMeta{
		RunID:           runID,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
		Model:           o.model,
		StudyModel:      o.studyModel,
		Window:          o.window,
		Temperature:     o.temperature,
		BackendType:     o.backendType,
		Endpoint:        o.endpoint,
		ToolGates:       []string{"enable_web=false", "enable_scan=false"},
		Auth:            authLabel(o),
		CortexCommit:    commit,
		CortexDirty:     dirty,
		CortexBin:       cortexBin,
		PolyglotRepo:    polyglotRepoURL,
		PolyglotCommit:  polyCommit,
		Language:        "go",
		Host:            host,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GoVersion:       runtime.Version(),
		TurnTimeout:     o.turnTimeout.String(),
		TestTimeout:     o.testTimeout.String(),
		Exercises:       exerciseNames(selected),
		ResultsPath:     filepath.Join(runDir, "results.jsonl"),
		TranscriptsPath: transcripts,
	}
	metaPath := filepath.Join(runDir, "run.json")
	if err := WriteRunMeta(metaPath, meta); err != nil {
		return err
	}

	sink, err := NewSink(meta.ResultsPath)
	if err != nil {
		return err
	}
	defer sink.Close()

	fmt.Printf("run %s | %d exercise(s) | model %s @ %s | polyglot %s\n",
		runID, len(selected), o.model, o.endpoint, shortSHA(polyCommit))

	rows := make([]Row, 0, len(selected))
	for i, ex := range selected {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(selected), ex.Name)
		row := runExercise(o, ex, runDir, benchHome, cortexBin)
		if err := sink.Append(row); err != nil {
			return err
		}
		rows = append(rows, row)
		fmt.Printf("  -> %s%s (%d tools, %d files changed, %.1fs)\n",
			verdictWord(row.Pass), classSuffix(row.FailureClass),
			row.ToolCalls, row.FilesChanged, float64(row.WallMs)/1000)
	}

	meta.EndedAt = time.Now().UTC().Format(time.RFC3339)
	if err := WriteRunMeta(metaPath, meta); err != nil {
		return err
	}
	PrintSummary(os.Stdout, meta, rows)
	return nil
}

// runExercise executes one exercise end to end. It never returns an error:
// every failure mode is a classified row, because a benchmark that aborts on
// the first harness hiccup produces no data at all.
func runExercise(o *options, ex Exercise, runDir, benchHome, cortexBin string) Row {
	start := time.Now()
	work := filepath.Join(runDir, "work", ex.Name)
	row := Row{
		Exercise:       ex.Name,
		WorkDir:        work,
		TranscriptPath: filepath.Join(runDir, "transcripts", ex.Name+".jsonl"),
	}

	before, err := PrepareWorkdir(ex, work)
	if err != nil {
		return harnessFailure(row, start, err)
	}
	// A fresh .cortex/ created BEFORE the turn is what makes the session
	// isolated: cortex resolves its workspace by walking up for the nearest
	// .cortex dir, so without this it would find the cortex repo's own and
	// inherit that project's config, sessions and memory.
	contextDir := filepath.Join(work, ".cortex")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		return harnessFailure(row, start, fmt.Errorf("failed to create workspace .cortex: %w", err))
	}
	if err := writeWorkspaceConfig(filepath.Join(contextDir, "config.json"), o); err != nil {
		return harnessFailure(row, start, err)
	}
	prompt, err := BuildPrompt(ex)
	if err != nil {
		return harnessFailure(row, start, err)
	}

	turn := runCortexTurn(o, cortexBin, work, benchHome, prompt)
	row.WallMs = turn.wall.Milliseconds()
	row.SessionID = turn.sessionID
	row.Error = turn.errText
	writeFileBestEffort(filepath.Join(runDir, "transcripts", ex.Name+".log"), turn.output)

	// Copy the session transcript out of the workdir so the row's reference
	// survives even if the work tree is later cleaned. With no session there
	// is no transcript, and the row must not carry a dangling reference.
	if turn.sessionID == "" {
		row.TranscriptPath = ""
	} else {
		src := filepath.Join(contextDir, "sessions", turn.sessionID+".jsonl")
		if err := copyFile(src, row.TranscriptPath); err != nil {
			row.TranscriptPath = src
		}
	}
	if st, err := ScanTranscript(row.TranscriptPath); err == nil {
		row.ToolCalls = st.ToolCalls
		row.MutatingCalls = st.MutatingCalls
	}
	if m, err := ReadCellMetrics(contextDir, turn.sessionID); err == nil {
		row.TokensIn = m.TokensIn
		row.TokensOut = m.TokensOut
		row.AgentTurns = m.AgentTurns
	}

	after, err := HashSolutionFiles(ex, work)
	if err == nil {
		row.FilesChanged = CountChanged(before, after)
	}

	verifyStart := time.Now()
	pass := goTestPasses(work, o.testTimeout)
	row.VerifyMs = time.Since(verifyStart).Milliseconds()

	row.Pass = pass && !turn.timedOut && !turn.errored
	row.FailureClass = Classify(Signals{
		Pass:         pass,
		TimedOut:     turn.timedOut,
		Errored:      turn.errored,
		ToolCalls:    row.ToolCalls,
		FilesChanged: row.FilesChanged,
	})
	return row
}

// harnessFailure records a row for an exercise the harness could not even
// attempt (staging or config failure) — always failure_class "error".
func harnessFailure(row Row, start time.Time, err error) Row {
	row.WallMs = time.Since(start).Milliseconds()
	row.Error = err.Error()
	row.FailureClass = ClassError
	return row
}

// turnResult is one `cortex turn --json` invocation.
type turnResult struct {
	sessionID string
	wall      time.Duration
	timedOut  bool
	errored   bool
	errText   string
	output    []byte
}

// turnEnvelope is the shape `cortex turn --json` prints on stdout.
type turnEnvelope struct {
	Session string `json:"session"`
	Reply   string `json:"reply"`
	Error   string `json:"error"`
}

func runCortexTurn(o *options, cortexBin, work, benchHome, prompt string) turnResult {
	ctx, cancel := context.WithTimeout(context.Background(), o.turnTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cortexBin, "turn", "--json", prompt)
	cmd.Dir = work
	cmd.Env = benchEnv(benchHome, o.temperature)
	// Give cortex a moment to unwind (closing the transcript, flushing the
	// metrics row) after the context kill before we reap it.
	cmd.WaitDelay = 10 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	res := turnResult{wall: time.Since(start)}

	var combined bytes.Buffer
	combined.WriteString("--- stdout ---\n")
	combined.Write(stdout.Bytes())
	combined.WriteString("\n--- stderr ---\n")
	combined.Write(stderr.Bytes())
	res.output = combined.Bytes()

	env, ok := parseTurnEnvelope(stdout.Bytes())
	if ok {
		res.sessionID = env.Session
		if env.Error != "" {
			res.errored = true
			res.errText = env.Error
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.timedOut = true
		res.errText = fmt.Sprintf("turn exceeded %s", o.turnTimeout)
		// The transcript is still on disk under the workspace even though we
		// never saw the JSON envelope; recover the session id from it.
		if res.sessionID == "" {
			res.sessionID = latestSessionID(filepath.Join(work, ".cortex", "sessions"))
		}
		return res
	}
	if runErr != nil {
		res.errored = true
		if res.errText == "" {
			res.errText = runErr.Error()
		}
		if res.sessionID == "" {
			res.sessionID = latestSessionID(filepath.Join(work, ".cortex", "sessions"))
		}
	}
	return res
}

// parseTurnEnvelope finds the JSON envelope on stdout. It scans lines rather
// than decoding the whole buffer: a stray banner line before the envelope
// must not lose the session id.
func parseTurnEnvelope(stdout []byte) (turnEnvelope, bool) {
	var out turnEnvelope
	found := false
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var env turnEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Session == "" {
			continue
		}
		out, found = env, true
	}
	return out, found
}

// benchEnv is the environment every cortex turn and `go test` runs under.
// It is deliberately narrow and hermetic: a benchmark-owned CORTEX_HOME, a
// pinned temperature, and a Go toolchain that cannot reach the network (the
// exercises are stdlib-only, so any module fetch is a bug, not a dependency).
func benchEnv(benchHome string, temperature float64) []string {
	env := append([]string(nil), os.Environ()...)
	drop := map[string]bool{
		"CORTEX_HOME": true, "CORTEX_BACKEND": true, "CORTEX_TEMPERATURE": true,
		"CORTEX_REPL_MODEL": true, "GOFLAGS": true, "GOWORK": true, "GOPROXY": true,
	}
	kept := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			kept = append(kept, kv)
		}
	}
	return append(kept,
		"CORTEX_HOME="+benchHome,
		fmt.Sprintf("CORTEX_TEMPERATURE=%g", temperature),
		"GOFLAGS=-mod=mod",
		"GOWORK=off",
		"GOPROXY=off",
	)
}

// writeWorkspaceConfig pins the model binding for one exercise workspace.
// This is the project-layer config (docs/configuration.md): it wins over both
// the user config and CORTEX_BACKEND, so the run is reproducible regardless
// of what the host machine has configured.
func writeWorkspaceConfig(path string, o *options) error {
	backend := map[string]any{"type": o.backendType, "endpoint": o.endpoint}
	// Auth is named, never inlined: the config file lands in the run
	// directory alongside the transcripts, so it must never hold a secret.
	if o.keyEnv != "" {
		backend["key_env"] = o.keyEnv
	}
	if o.keyService != "" {
		backend["key_service"] = o.keyService
	}
	cfg := map[string]any{
		"backend": backend,
		"models": map[string]any{
			"code":  map[string]any{"model": o.model, "window": o.window},
			"study": map[string]any{"model": o.studyModel, "window": o.window},
		},
		"temperature": o.temperature,
		// The exercises are self-contained; web access and the home-scoped
		// landscape survey are confounds, not capabilities under test.
		"tools": map[string]any{"enable_web": false, "enable_scan": false},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode workspace config: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write workspace config %s: %w", path, err)
	}
	return nil
}

// goTestPasses is the verdict: `go test ./...` in the exercise workdir.
func goTestPasses(work string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	writeFileBestEffort(filepath.Join(work, "go-test.out"), out)
	return err == nil
}

// latestSessionID returns the newest session transcript's id in dir, or "".
func latestSessionID(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestMod := "", time.Time{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestMod) {
			best, bestMod = strings.TrimSuffix(e.Name(), ".jsonl"), info.ModTime()
		}
	}
	return best
}

func writeFileBestEffort(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// gitHead reports dir's HEAD sha and whether its work tree is dirty.
func gitHead(dir string) (string, bool) {
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	return strings.TrimSpace(string(sha)), len(bytes.TrimSpace(status)) > 0
}

// authLabel records WHERE the backend key came from, never the key.
func authLabel(o *options) string {
	switch {
	case o.keyService != "":
		return "key_service=" + o.keyService
	case o.keyEnv != "":
		return "key_env=" + o.keyEnv
	default:
		return "none"
	}
}

func exerciseNames(exs []Exercise) []string {
	out := make([]string, len(exs))
	for i, e := range exs {
		out[i] = e.Name
	}
	return out
}

func verdictWord(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func classSuffix(class string) string {
	if class == "" {
		return ""
	}
	return " [" + class + "]"
}
