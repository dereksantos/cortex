// Command swebenchbench runs the cortex coding agent on SWE-bench (Verified by
// default) and scores its patches with the OFFICIAL SWE-bench evaluation
// harness. Drive it through scripts/bench/swebench/run.sh, which pins the
// dataset revision and the harness version and builds the binaries.
//
// Per instance:
//
//	pull the instance's prebuilt image (linux/amd64; repo at the base commit,
//	  dependencies installed in the `testbed` conda env)
//	start a container, copy a static linux cortex binary + a pinned config in
//	`cortex turn --json "<issue + task statement>"` inside /testbed
//	  (no hints, no test patch, no test names — the issue text only)
//	`git diff` against the image HEAD (base commit + one SWE-bench setup commit) → the prediction; copy the session
//	  transcript (the trajectory) and the whole .cortex/ out
//	score the prediction with `python -m swebench.harness.run_evaluation`
//	classify, then append to predictions.jsonl + results.jsonl (fsynced)
//
// Spend is bounded by an independent guard on the OpenRouter key's usage
// (budget.go), checked between instances and on a timer during each turn.
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

type options struct {
	datasetFile  string
	datasetName  string
	datasetRev   string
	python       string
	swebenchVer  string
	cortexLinux  string
	outRoot      string
	runID        string
	seed         string
	n            int
	instances    string
	model        string
	studyModel   string
	providerTags string
	allowFallbck bool
	requireParms bool
	window       int
	temperature  float64
	endpoint     string
	keyService   string
	budget       float64
	instanceCap  float64
	estFirst     float64
	turnTimeout  time.Duration
	evalTimeout  time.Duration
	pollEvery    time.Duration
	keepImages   bool
	noScore      bool
	dryRun       bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "swebenchbench: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() *options {
	o := &options{}
	flag.StringVar(&o.datasetFile, "dataset-file", "", "dataset export from dataset.py (required)")
	flag.StringVar(&o.datasetName, "dataset-name", "SWE-bench/SWE-bench_Verified", "dataset the export came from (recorded)")
	flag.StringVar(&o.datasetRev, "dataset-revision", "", "HF revision of the export (recorded)")
	flag.StringVar(&o.python, "python", "", "python with the official swebench package installed (required unless --no-score)")
	flag.StringVar(&o.swebenchVer, "swebench-version", "", "installed swebench version (recorded)")
	flag.StringVar(&o.cortexLinux, "cortex-linux", "", "static linux/amd64 cortex binary to run inside the container (required)")
	flag.StringVar(&o.outRoot, "out", "", "output root; one dir per run (required)")
	flag.StringVar(&o.runID, "run-id", "", "run directory name (default: UTC timestamp)")
	flag.StringVar(&o.seed, "seed", "106", "selection seed: instances ranked by sha256(seed:instance_id)")
	flag.IntVar(&o.n, "n", 5, "number of instances (0 = all)")
	flag.StringVar(&o.instances, "instance", "", "comma-separated instance ids (overrides --seed/--n)")
	flag.StringVar(&o.model, "model", "qwen/qwen3-coder", "OpenRouter model id for the code role")
	flag.StringVar(&o.studyModel, "study-model", "", "model id for the study role (default: --model, so the system is one model)")
	flag.StringVar(&o.providerTags, "provider", "novita/fp8", "OpenRouter provider order (comma-separated slugs/tags); empty = no pin")
	flag.BoolVar(&o.allowFallbck, "allow-fallbacks", false, "let OpenRouter fall back to other providers")
	flag.BoolVar(&o.requireParms, "require-parameters", true, "route only to providers supporting every request parameter")
	flag.IntVar(&o.window, "window", 131072, "cortex context window (tokens)")
	flag.Float64Var(&o.temperature, "temperature", 0, "sampling temperature")
	flag.StringVar(&o.endpoint, "endpoint", "https://openrouter.ai/api/v1", "OpenRouter API root")
	flag.StringVar(&o.keyService, "key-service", "cortex-openrouter", "macOS keychain service holding the OpenRouter key")
	flag.Float64Var(&o.budget, "budget", 5, "hard spend cap for the whole run, USD (OpenRouter key usage delta)")
	flag.Float64Var(&o.instanceCap, "instance-cap", 1.0, "spend cap per instance, USD (the turn is interrupted at it)")
	flag.Float64Var(&o.estFirst, "est-first", 1.0, "assumed cost of an instance before any is observed, USD")
	flag.DurationVar(&o.turnTimeout, "timeout", 40*time.Minute, "per-instance wall-clock budget for the cortex turn")
	flag.DurationVar(&o.evalTimeout, "eval-timeout", 30*time.Minute, "per-instance test timeout for the official harness")
	flag.DurationVar(&o.pollEvery, "poll", 20*time.Second, "spend-guard polling interval during a turn")
	flag.BoolVar(&o.keepImages, "keep-images", false, "keep instance images this run pulled (default: remove after scoring, to save disk)")
	flag.BoolVar(&o.noScore, "no-score", false, "skip the official harness (predictions only)")
	flag.BoolVar(&o.dryRun, "dry-run", false, "print the selection and exit (no docker, no spend)")
	flag.Parse()
	if o.studyModel == "" {
		o.studyModel = o.model
	}
	return o
}

func run() error {
	o := parseFlags()
	if o.datasetFile == "" {
		return errors.New("--dataset-file is required")
	}
	all, err := LoadInstances(o.datasetFile)
	if err != nil {
		return err
	}
	selected, err := SelectInstances(all, splitList(o.instances), o.n, o.seed)
	if err != nil {
		return err
	}
	if o.dryRun {
		for _, in := range selected {
			fmt.Printf("%s\t%s\t%s\n", in.InstanceID, in.Difficulty, in.Image)
		}
		return nil
	}
	for _, req := range []struct{ name, val string }{
		{"--out", o.outRoot}, {"--cortex-linux", o.cortexLinux},
	} {
		if req.val == "" {
			return fmt.Errorf("%s is required", req.name)
		}
	}
	if !o.noScore && o.python == "" {
		return errors.New("--python is required unless --no-score")
	}

	key, err := readKeychain(o.keyService)
	if err != nil {
		return err
	}
	usage := newOpenRouterUsage(o.endpoint, key)
	ctx := context.Background()
	usage0, err := usage.Usage(ctx)
	if err != nil {
		return fmt.Errorf("spend guard unavailable, refusing to run: %w", err)
	}

	runID := o.runID
	if runID == "" {
		runID = time.Now().UTC().Format("20060102-150405")
	}
	runDir, err := filepath.Abs(filepath.Join(o.outRoot, runID))
	if err != nil {
		return err
	}
	for _, d := range []string{"trajs", "logs", "instances", "eval"} {
		if err := os.MkdirAll(filepath.Join(runDir, d), 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}
	cortexLinux, err := filepath.Abs(o.cortexLinux)
	if err != nil {
		return err
	}
	datasetFile, err := filepath.Abs(o.datasetFile)
	if err != nil {
		return err
	}

	commit, dirty := gitHead(".")
	modelName := predictionModelName(commit, o.model)
	cfg := workspaceConfig(o)
	host, _ := os.Hostname()
	server := dockerServerArch(ctx)
	meta := RunMeta{
		RunID:             runID,
		StartedAt:         time.Now().UTC().Format(time.RFC3339),
		ModelNameOrPath:   modelName,
		Model:             o.model,
		StudyModel:        o.studyModel,
		Provider:          providerRouting(o),
		Window:            o.window,
		Temperature:       o.temperature,
		Endpoint:          o.endpoint,
		WorkspaceConfig:   cfg,
		Auth:              "host keychain service=" + o.keyService + " -> container env OPENROUTER_API_KEY (config key_env; stripped from the agent's shell)",
		CortexCommit:      commit,
		CortexDirty:       dirty,
		CortexBin:         cortexLinux,
		SwebenchVersion:   o.swebenchVer,
		Dataset:           o.datasetName,
		DatasetRevision:   o.datasetRev,
		DatasetFile:       datasetFile,
		Split:             "test",
		Seed:              o.seed,
		Instances:         ids(selected),
		BudgetUSD:         o.budget,
		InstanceCapUSD:    o.instanceCap,
		AccountUsageStart: usage0,
		TurnTimeout:       o.turnTimeout.String(),
		EvalTimeout:       o.evalTimeout.String(),
		Host:              host,
		HostOS:            runtime.GOOS,
		HostArch:          runtime.GOARCH,
		DockerServer:      server,
		Emulated:          !strings.HasSuffix(server, "/amd64"),
		ResultsPath:       filepath.Join(runDir, "results.jsonl"),
		PredictionsPath:   filepath.Join(runDir, "predictions.jsonl"),
	}
	metaPath := filepath.Join(runDir, "run.json")
	if err := WriteRunMeta(metaPath, meta); err != nil {
		return err
	}
	results, err := NewJSONLSink(meta.ResultsPath)
	if err != nil {
		return err
	}
	defer results.Close()
	preds, err := NewJSONLSink(meta.PredictionsPath)
	if err != nil {
		return err
	}
	defer preds.Close()

	guard := &BudgetGuard{Budget: o.budget, InstanceCap: o.instanceCap, EstFirst: o.estFirst}
	fmt.Printf("run %s | %d instance(s) | %s via %v | budget $%.2f (cap $%.2f/instance) | docker %s\n",
		runID, len(selected), o.model, meta.Provider["order"], o.budget, o.instanceCap, server)

	var rows []Row
	for i, in := range selected {
		now, err := usage.Usage(ctx)
		if err != nil {
			fmt.Printf("spend guard unavailable (%v) — stopping before %s\n", err, in.InstanceID)
			meta.SkippedForBudget = append(meta.SkippedForBudget, ids(selected[i:])...)
			break
		}
		spent := now - usage0
		if !guard.CanStart(spent) {
			fmt.Printf("budget: spent $%.4f, next instance estimated $%.4f > $%.2f cap — stopping\n",
				spent, guard.NextEstimate(), o.budget)
			meta.SkippedForBudget = append(meta.SkippedForBudget, ids(selected[i:])...)
			break
		}
		fmt.Printf("\n[%d/%d] %s (spent so far $%.4f)\n", i+1, len(selected), in.InstanceID, spent)
		r := &runner{o: o, runID: runID, runDir: runDir, cortexLinux: cortexLinux, datasetFile: datasetFile,
			modelName: modelName, cfg: cfg, key: key, usage: usage,
			limit: guard.InstanceLimit(spent)}
		row, pred := r.instance(ctx, in, now)
		if pred != nil {
			if err := preds.Append(pred); err != nil {
				return err
			}
		}
		if err := results.Append(row); err != nil {
			return err
		}
		guard.Observe(row.AccountCostUSD)
		rows = append(rows, row)
		fmt.Printf("  -> resolved=%v class=%s end=%s eval=%s tools=%d $%.4f (cortex $%.4f) %.0fs\n",
			row.Resolved, dash(row.FailureClass), row.TurnEnd, row.EvalStatus, row.ToolCalls,
			row.AccountCostUSD, row.CostUSD, float64(row.WallMs)/1000)
	}

	if end, err := usage.Usage(ctx); err == nil {
		meta.AccountUsageEnd = end
	}
	meta.EndedAt = time.Now().UTC().Format(time.RFC3339)
	if err := WriteRunMeta(metaPath, meta); err != nil {
		return err
	}
	PrintSummary(os.Stdout, meta, rows)
	return nil
}

// runner carries one instance attempt's context.
type runner struct {
	o           *options
	runID       string
	runDir      string
	cortexLinux string
	datasetFile string
	modelName   string
	cfg         map[string]any
	key         string
	usage       UsageReader
	limit       float64 // USD this instance may spend
}

// instance runs one instance end to end. It never returns an error: every
// failure is a classified row, because a benchmark that aborts on the first
// hiccup produces no data.
func (r *runner) instance(ctx context.Context, in Instance, usageStart float64) (Row, *Prediction) {
	start := time.Now()
	logDir := filepath.Join(r.runDir, "logs", in.InstanceID)
	instDir := filepath.Join(r.runDir, "instances", in.InstanceID)
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.MkdirAll(instDir, 0o755)
	row := Row{InstanceID: in.InstanceID, LogDir: logDir, TurnEnd: EndNotStarted, EvalStatus: EvalNotRun}
	name := containerName(r.runID, in.InstanceID)
	var setupLog bytes.Buffer

	finish := func(envFailed bool) (Row, *Prediction) {
		row.WallMs = time.Since(start).Milliseconds()
		if u, err := r.usage.Usage(ctx); err == nil {
			row.AccountCostUSD = u - usageStart
		}
		row.FailureClass = Classify(Signals{EnvFailed: envFailed, TurnEnd: row.TurnEnd,
			EvalStatus: row.EvalStatus, ToolCalls: row.ToolCalls, PatchBytes: row.PatchBytes})
		_ = os.WriteFile(filepath.Join(logDir, "setup.log"), setupLog.Bytes(), 0o644)
		for _, dir := range []string{logDir, instDir, filepath.Join(r.runDir, "trajs"),
			harnessLogDir(filepath.Join(r.runDir, "eval"), r.runID, r.modelName, in.InstanceID)} {
			if found, _ := redactTree(dir, r.key); found {
				row.SecretRedacted = true
			}
		}
		return row, nil
	}

	// --- 1. environment -------------------------------------------------
	pulled, pullDur, err := ensureImage(ctx, in.Image)
	row.ImagePulledMs = pullDur.Milliseconds()
	if err != nil {
		row.Error = "image: " + err.Error()
		return finish(true)
	}
	if pulled && !r.o.keepImages {
		defer func() {
			rctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_, _ = dockerRun(rctx, "rmi", in.Image)
		}()
	}
	if err := startContainer(ctx, name, in.Image); err != nil {
		row.Error = "container: " + err.Error()
		return finish(true)
	}
	defer removeContainer(name)

	cfgPath := filepath.Join(instDir, "config.json")
	if err := writeJSON(cfgPath, r.cfg); err != nil {
		row.Error = err.Error()
		return finish(true)
	}
	imageHead := ""
	setup := []struct {
		label string
		fn    func() (string, error)
	}{
		{"mkdir", func() (string, error) {
			// .cortex/ is excluded from git so the prediction never carries
			// cortex's own state.
			return execIn(ctx, name, "mkdir -p /testbed/.cortex /tmp/cortex-home && echo '.cortex/' >> /testbed/.git/info/exclude")
		}},
		{"cp cortex", func() (string, error) { return dockerRun(ctx, "cp", r.cortexLinux, name+":/usr/local/bin/cortex") }},
		{"cp config", func() (string, error) { return dockerRun(ctx, "cp", cfgPath, name+":/testbed/.cortex/config.json") }},
		// The published images carry one "SWE-bench" setup commit on top of
		// base_commit; the harness applies predictions to that same HEAD, so
		// the requirement is "base_commit is an ancestor of HEAD", and the
		// prediction is diffed against HEAD (imageHead), not base_commit.
		{"head", func() (string, error) {
			return execIn(ctx, name, "cd /testbed && git merge-base --is-ancestor "+in.BaseCommit+" HEAD && git rev-parse HEAD")
		}},
		{"base..HEAD", func() (string, error) {
			return execIn(ctx, name, "cd /testbed && git log --oneline "+in.BaseCommit+"..HEAD && git diff --stat "+in.BaseCommit+" HEAD | tail -5")
		}},
		{"status", func() (string, error) { return execIn(ctx, name, "git -C /testbed status --porcelain | head -20") }},
		{"cortex version", func() (string, error) { return execIn(ctx, name, "cortex version 2>&1 | head -3") }},
	}
	for _, s := range setup {
		out, err := s.fn()
		fmt.Fprintf(&setupLog, "== %s\n%s\n", s.label, out)
		if err != nil {
			fmt.Fprintf(&setupLog, "ERROR: %v\n", err)
			row.Error = s.label + ": " + err.Error()
			return finish(true)
		}
		if s.label == "head" {
			imageHead = strings.TrimSpace(out)
			row.BaseCommitOK = imageHead != ""
		}
	}
	row.SetupMs = time.Since(start).Milliseconds()

	// --- 2. the agent -----------------------------------------------------
	agentStart := time.Now()
	turn := r.runTurn(ctx, name, BuildPrompt(in), usageStart)
	row.AgentMs = time.Since(agentStart).Milliseconds()
	row.TurnEnd = turn.end
	row.SessionID = turn.sessionID
	if turn.errText != "" {
		row.Error = turn.errText
	}
	_ = os.WriteFile(filepath.Join(logDir, "cortex.log"), turn.output, 0o644)

	// --- 3. artifacts: prediction, trajectory, cortex state ---------------
	patch, perr := execRaw(ctx, name, fmt.Sprintf(
		"cd /testbed && git add -A >/dev/null 2>&1; git -c core.fileMode=false diff --cached %s", imageHead))
	if perr != nil {
		row.Error = strings.TrimSpace(row.Error + "; diff: " + perr.Error())
	}
	row.PatchBytes = len(strings.TrimSpace(patch))
	row.PatchFiles = PatchFiles(patch)
	row.PatchPath = filepath.Join(logDir, "patch.diff")
	_ = os.WriteFile(row.PatchPath, []byte(patch), 0o644)

	_, _ = dockerRun(ctx, "cp", name+":/testbed/.cortex", filepath.Join(instDir, "cortex"))
	_, _ = dockerRun(ctx, "cp", name+":/tmp/cortex-home", filepath.Join(instDir, "cortex-home"))
	if row.SessionID == "" {
		row.SessionID = latestSessionID(filepath.Join(instDir, "cortex", "sessions"))
	}
	if row.SessionID != "" {
		src := filepath.Join(instDir, "cortex", "sessions", row.SessionID+".jsonl")
		dst := filepath.Join(r.runDir, "trajs", in.InstanceID+".jsonl")
		if err := copyFile(src, dst); err == nil {
			row.TrajPath = dst
			if st, err := ScanTranscript(dst); err == nil {
				row.ToolCalls, row.MutatingCalls, row.ToolCounts = st.ToolCalls, st.MutatingCalls, st.ToolCounts
			}
		}
	}
	if m, err := ReadCellMetrics(filepath.Join(instDir, "cortex"), row.SessionID); err == nil {
		row.TokensIn, row.TokensOut, row.ReasoningTokens = m.TokensIn, m.TokensOut, m.ReasoningTokens
		row.CostUSD, row.AgentTurns, row.MetricsFound = m.CostUSD, m.AgentTurns, m.Found
	}
	// The inference container is done; free its memory before the harness
	// starts its own container from the same image.
	removeContainer(name)

	pred := &Prediction{InstanceID: in.InstanceID, ModelNameOrPath: r.modelName, ModelPatch: patch}

	// --- 4. official scoring ----------------------------------------------
	if !r.o.noScore {
		evalStart := time.Now()
		sr := ScoreOne(ctx, r.o.python, r.datasetFile, filepath.Join(r.runDir, "eval"), r.runID, *pred, r.o.evalTimeout)
		row.EvalMs = time.Since(evalStart).Milliseconds()
		row.EvalStatus, row.Resolved, row.EvalReport = sr.Status, sr.Resolved, sr.ReportPath
		if sr.Err != "" {
			row.Error = strings.TrimSpace(row.Error + "; eval: " + sr.Err)
		}
	}
	out, _ := finish(false)
	return out, pred
}

type turnResult struct {
	sessionID string
	end       string
	errText   string
	output    []byte
}

type turnEnvelope struct {
	Session string `json:"session"`
	Reply   string `json:"reply"`
	Error   string `json:"error"`
}

// runTurn runs `cortex turn --json` inside the container under the wall
// clock and spend guards. The key reaches the docker CLI only through its
// process environment (`-e OPENROUTER_API_KEY` with no value makes docker
// read it from there) — never argv, where `ps` would show it.
func (r *runner) runTurn(ctx context.Context, name, prompt string, usageStart float64) turnResult {
	const script = `source /opt/miniconda3/bin/activate && conda activate testbed && exec cortex turn --json "$1"`
	cmd := exec.Command("docker", "exec", "-w", "/testbed",
		"-e", "OPENROUTER_API_KEY",
		"-e", "CORTEX_HOME=/tmp/cortex-home",
		"-e", fmt.Sprintf("CORTEX_TEMPERATURE=%g", r.o.temperature),
		"-e", "NO_COLOR=1",
		"-e", "CORTEX_LOOP_RENDER=0",
		name, "bash", "-c", script, "cortex-turn", prompt)
	cmd.Env = append(os.Environ(), "OPENROUTER_API_KEY="+r.key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	res := turnResult{end: EndCompleted}
	if err := cmd.Start(); err != nil {
		res.end, res.errText = EndError, "start: "+err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.NewTimer(r.o.turnTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(r.o.pollEvery)
	defer tick.Stop()

	var runErr error
	stopReason := ""
wait:
	for {
		select {
		case runErr = <-done:
			break wait
		case <-deadline.C:
			stopReason = EndTimeout
		case <-tick.C:
			if u, err := r.usage.Usage(ctx); err == nil && u-usageStart >= r.limit {
				stopReason = EndBudgetStop
				res.errText = fmt.Sprintf("spend guard: instance spent $%.4f >= limit $%.4f", u-usageStart, r.limit)
			}
		}
		if stopReason != "" {
			runErr = interruptCortex(name, done)
			break wait
		}
	}

	var combined bytes.Buffer
	combined.WriteString("--- stdout ---\n")
	combined.Write(stdout.Bytes())
	combined.WriteString("\n--- stderr ---\n")
	combined.Write(stderr.Bytes())
	res.output = combined.Bytes()

	if env, ok := parseTurnEnvelope(stdout.Bytes()); ok {
		res.sessionID = env.Session
		if env.Error != "" && stopReason == "" {
			res.end, res.errText = EndError, env.Error
		}
	}
	switch {
	case stopReason == EndTimeout:
		res.end, res.errText = EndTimeout, fmt.Sprintf("turn exceeded %s", r.o.turnTimeout)
	case stopReason == EndBudgetStop:
		res.end = EndBudgetStop
	case runErr != nil && res.end == EndCompleted:
		res.end, res.errText = EndError, runErr.Error()
	}
	return res
}

// interruptCortex asks the in-container cortex to stop (SIGINT lets `cortex
// turn` close its transcript cleanly), then kills it if it does not.
func interruptCortex(name string, done <-chan error) error {
	ictx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = dockerRun(ictx, "exec", name, "pkill", "-INT", "-x", "cortex")
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(45 * time.Second):
	}
	kctx, kcancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = dockerRun(kctx, "exec", name, "pkill", "-KILL", "-x", "cortex")
	kcancel()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		return errors.New("cortex did not exit after SIGKILL")
	}
}

// execRaw runs a script in the container and returns stdout untrimmed — a
// patch must keep its trailing newline or `git apply` rejects it.
func execRaw(ctx context.Context, name, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", name, "bash", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(lastLines(stderr.String(), 4)))
	}
	return stdout.String(), nil
}

// parseTurnEnvelope finds the JSON envelope on stdout (the last line that
// decodes with a session id).
func parseTurnEnvelope(stdout []byte) (turnEnvelope, bool) {
	var out turnEnvelope
	found := false
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var env turnEnvelope
		if json.Unmarshal(line, &env) != nil || env.Session == "" {
			continue
		}
		out, found = env, true
	}
	return out, found
}

// providerRouting is the OpenRouter `provider` object this run pins.
func providerRouting(o *options) map[string]any {
	p := map[string]any{
		"allow_fallbacks":    o.allowFallbck,
		"require_parameters": o.requireParms,
	}
	if tags := splitList(o.providerTags); len(tags) > 0 {
		p["order"] = tags
	}
	return p
}

// workspaceConfig is the project-layer cortex config written into the
// container's /testbed/.cortex/config.json. It wins over any user config and
// CORTEX_BACKEND, so the run is one pinned system.
func workspaceConfig(o *options) map[string]any {
	return map[string]any{
		"backend": map[string]any{
			"type":     "openrouter",
			"endpoint": o.endpoint,
			// Inside the linux container there is no keychain: the key is
			// supplied by env, and cortex strips key_env vars from the
			// agent's shell (internal/tools/shellenv.go).
			"key_env":  "OPENROUTER_API_KEY",
			"provider": providerRouting(o),
		},
		"models": map[string]any{
			"code":  map[string]any{"model": o.model, "window": o.window},
			"study": map[string]any{"model": o.studyModel, "window": o.window},
		},
		"temperature": o.temperature,
		// No web (checklist: no solution lookup), no landscape scan.
		"tools": map[string]any{"enable_web": false, "enable_scan": false},
		// No model substitution: a pinned model must fail loudly, not be
		// silently swapped for a :free fallback.
		"network": map[string]any{"self_heal": false},
	}
}

// predictionModelName is model_name_or_path: harness + commit + model.
func predictionModelName(commit, model string) string {
	sha := commit
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return "cortex-" + sha + "__" + strings.ReplaceAll(model, "/", "--")
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
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

func gitHead(dir string) (string, bool) {
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	return strings.TrimSpace(string(sha)), len(bytes.TrimSpace(status)) > 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ids(ins []Instance) []string {
	out := make([]string, len(ins))
	for i, in := range ins {
		out[i] = in.InstanceID
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
