// Command decide runs the parallel decider (internal/decide) offline and
// records every answer in the durable bench store:
//
//	go run ./scripts/bench/decide eval   [flags]   # the decider eval (hand-built cases)
//	go run ./scripts/bench/decide replay --source <batch dir> [flags]
//	go run ./scripts/bench/decide analyze <replay run dir>
//
// eval scores the micro-prompt + model pair on internal/decide.EvalCases
// (bench "decide-eval"; a row passes when every question is answered right).
//
// replay walks stored cortex session transcripts — a SWE-bench batch dir whose
// cortex-<instance>/ run dirs each hold results.jsonl + the session JSONL —
// cuts each into the decision points a live decider would have seen, asks the
// decider at a sample of them, and records (bench "decide-replay"):
//
//   - decisions.jsonl: one row per question asked, with what the agent did next;
//   - results.jsonl:   one row per task, carrying the task's ORIGINAL outcome
//     (resolved / failure_class) as the label plus the decider's summary and
//     the cheap mechanical signals it must beat (docs/decide.md).
//
// The decider never changes anything here: it reads finished transcripts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/benchstore"
	"github.com/dereksantos/cortex/internal/decide"
	"github.com/dereksantos/cortex/pkg/secret"
)

type options struct {
	mode       string
	model      string
	endpoint   string
	keyService string
	keyEnv     string
	runID      string
	outRoot    string
	ephemeral  bool
	labels     benchstore.LabelFlag
	timeout    time.Duration
	rpm        int
	retries    int
	reps       int
	source     string
	maxTasks   int
	points     int
	only       string
	dump       bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "decide:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[0] == "analyze" {
		return runAnalyze(args[1], os.Stdout)
	}
	if len(args) == 0 || (args[0] != "eval" && args[0] != "replay") {
		return errors.New("usage: decide eval|replay [flags] | decide analyze <replay run dir>")
	}
	o := &options{mode: args[0], labels: benchstore.LabelFlag{}}
	fs := flag.NewFlagSet("decide "+o.mode, flag.ExitOnError)
	fs.StringVar(&o.model, "model", "nvidia/nemotron-3-super-120b-a12b:free", "decider model id")
	fs.StringVar(&o.endpoint, "endpoint", "https://openrouter.ai/api", "OpenAI-compatible endpoint (\"/v1\" appended)")
	fs.StringVar(&o.keyService, "key-service", "cortex-openrouter", "keychain service holding the endpoint's API key")
	fs.StringVar(&o.keyEnv, "key-env", "", "env var holding the API key (overrides --key-service)")
	fs.StringVar(&o.runID, "run-id", "", "run directory name (default: <mode>-<UTC timestamp>)")
	fs.StringVar(&o.outRoot, "out", "", "parent of the run dir (default: the durable store)")
	fs.BoolVar(&o.ephemeral, "ephemeral", false, "allow --out inside a git working tree (throwaway runs only)")
	fs.Var(o.labels, "label", "tag the run, key=value (repeatable), e.g. --label arm=decider-shadow")
	fs.DurationVar(&o.timeout, "timeout", 60*time.Second, "per-decision timeout")
	fs.IntVar(&o.rpm, "rpm", 16, "max decider calls per minute (free tiers cap ~20)")
	fs.IntVar(&o.retries, "retries", 2, "re-asks after a rate-limit or timeout (offline only; live never retries)")
	fs.IntVar(&o.reps, "reps", 1, "eval: repetitions of each case")
	fs.StringVar(&o.source, "source", "", "replay: batch dir holding cortex-<instance>/ run dirs")
	fs.IntVar(&o.maxTasks, "tasks", 0, "replay: at most N tasks (0 = all)")
	fs.IntVar(&o.points, "points", 12, "replay: at most N mid-turn decision points per task (plus the finalize point)")
	fs.StringVar(&o.only, "only", "", "comma-separated case ids (eval) or instance ids (replay)")
	fs.BoolVar(&o.dump, "dump", false, "replay: print the rendered prompts and exit (no model calls, no run recorded)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if o.dump {
		return dumpPrompts(o)
	}
	key, err := apiKey(o)
	if err != nil {
		return err
	}
	d := decide.NewChat(decide.ChatConfig{Endpoint: o.endpoint, APIKey: key, Model: o.model, Timeout: o.timeout})
	p := &pacer{gap: time.Minute / time.Duration(max(o.rpm, 1))}
	if o.mode == "eval" {
		return runEval(o, d, p)
	}
	return runReplay(o, d, p)
}

func apiKey(o *options) (string, error) {
	if o.keyEnv != "" {
		if v := os.Getenv(o.keyEnv); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("%s is empty", o.keyEnv)
	}
	v, err := secret.NewStore().Get(o.keyService)
	if err != nil || v == "" {
		return "", fmt.Errorf("no API key in keychain service %q: %v", o.keyService, err)
	}
	return v, nil
}

// pacer spaces calls at least gap apart.
type pacer struct {
	gap  time.Duration
	last time.Time
}

func (p *pacer) wait() {
	if d := time.Until(p.last.Add(p.gap)); d > 0 {
		time.Sleep(d)
	}
	p.last = time.Now()
}

// ask runs one decision with offline retries on transient statuses.
func ask(o *options, d decide.Decider, p *pacer, s decide.Snapshot, asks []decide.Ask) (decide.Decision, int) {
	var dec decide.Decision
	attempts := 0
	for attempts <= o.retries {
		p.wait()
		attempts++
		dec = d.Decide(context.Background(), s, asks)
		if dec.Status != decide.StatusRateLimited && dec.Status != decide.StatusTimeout {
			break
		}
		if attempts <= o.retries {
			time.Sleep(time.Duration(attempts) * 15 * time.Second)
		}
	}
	return dec, attempts
}

// begin opens the run in the durable store.
func begin(o *options, bench string) (*benchstore.Run, error) {
	id := o.runID
	if id == "" {
		id = o.mode + "-" + time.Now().UTC().Format("20060102-150405")
	}
	labels := map[string]string{"decider_model": o.model, "prompt_version": decide.PromptVersion()}
	for k, v := range o.labels {
		labels[k] = v
	}
	return benchstore.Begin(benchstore.BeginOptions{
		Bench: bench, RunID: id, OutRoot: o.outRoot, Ephemeral: o.ephemeral,
		SourceRepo: ".", Model: o.model, Labels: labels,
	})
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// jsonl appends rows to a file, fsyncing each, so a crash keeps every row so far.
type jsonl struct{ f *os.File }

func openJSONL(path string) (*jsonl, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	return &jsonl{f: f}, nil
}

func (j *jsonl) add(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

func (j *jsonl) close() { _ = j.f.Close() }

// finishRun closes the run in the store and syncs it off-machine.
func finishRun(r *benchstore.Run, note string) error {
	s, err := r.Finish(note)
	if err != nil {
		return fmt.Errorf("failed to record the run in the benchmark index: %w", err)
	}
	fmt.Printf("recorded: %s (pass %d / fail %d / skipped %d)\n", r.Dir, s.Pass, s.Fail, s.Skipped)
	benchstore.SyncBestEffort(os.Stdout)
	return nil
}

func runMeta(o *options, extra map[string]any) map[string]any {
	host, _ := os.Hostname()
	m := map[string]any{
		"harness":        "cortex-decide",
		"started_at":     time.Now().UTC().Format(time.RFC3339),
		"model":          o.model,
		"endpoint":       o.endpoint,
		"backend":        "chat",
		"host":           host,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"prompt_version": decide.PromptVersion(),
		"rpm":            o.rpm,
		"timeout":        o.timeout.String(),
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func pickMap(d decide.Decision) map[string][]string {
	out := map[string][]string{}
	for _, a := range d.Answers {
		out[string(a.Q)] = a.Picks
	}
	return out
}

func onlySet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	m := map[string]bool{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return m
}

// ---- eval ----

func runEval(o *options, d decide.Decider, p *pacer) error {
	cases := decide.EvalCases()
	if only := onlySet(o.only); only != nil {
		cases = slices.DeleteFunc(cases, func(c decide.EvalCase) bool { return !only[c.ID] })
	}
	r, err := begin(o, "decide-eval")
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	meta := runMeta(o, map[string]any{"ids": ids, "reps": o.reps, "dataset": "internal/decide.EvalCases"})
	if err := writeJSON(filepath.Join(r.Dir, "run.json"), meta); err != nil {
		return err
	}
	sink, err := openJSONL(filepath.Join(r.Dir, "results.jsonl"))
	if err != nil {
		return err
	}
	defer sink.close()
	fmt.Printf("decide eval | %d case(s) x %d | %s | prompt %s\n", len(cases), o.reps, o.model, decide.PromptVersion())
	for rep := 1; rep <= o.reps; rep++ {
		for _, c := range cases {
			dec, attempts := ask(o, d, p, c.Snapshot, c.Asks)
			row := evalRow(c, dec, rep, attempts)
			if err := sink.add(row); err != nil {
				return err
			}
			fmt.Printf("  %-26s %-12s %5.1fs  %v\n", c.ID, verdict(row), dec.Latency.Seconds(), pickMap(dec))
		}
	}
	meta["ended_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := writeJSON(filepath.Join(r.Dir, "run.json"), meta); err != nil {
		return err
	}
	return finishRun(r, "")
}

func verdict(row map[string]any) string {
	if row["resolved"] == true {
		return "pass"
	}
	return fmt.Sprint(row["failure_class"])
}

// evalRow is one native results row. A transport failure is an infra error
// (excluded from the pass rate); an off-menu or unparseable answer is the
// model's failure (parse_error); a wrong pick is wrong_answer.
func evalRow(c decide.EvalCase, dec decide.Decision, rep, attempts int) map[string]any {
	row := map[string]any{
		"task_id":        c.ID,
		"rep":            rep,
		"decide_status":  dec.Status,
		"latency_ms":     dec.Latency.Milliseconds(),
		"wall_ms":        dec.Latency.Milliseconds(),
		"attempts":       attempts,
		"picks":          pickMap(dec),
		"prompt_version": dec.PromptVersion,
	}
	if dec.Detail != "" {
		row["detail"] = dec.Detail
	}
	switch dec.Status {
	case decide.StatusOK:
		scores := c.Score(dec)
		all := true
		perQ := map[string]bool{}
		for q, ok := range scores {
			perQ[string(q)] = ok
			all = all && ok
		}
		row["correct"] = perQ
		row["resolved"] = all
		if !all {
			row["failure_class"] = "wrong_answer"
		}
	case decide.StatusParseError:
		row["resolved"] = false
		row["failure_class"] = "parse_error"
	default:
		row["resolved"] = false
		row["failure_class"] = "env_error"
	}
	return row
}

// ---- replay ----

// taskSource is one stored cortex run of one instance.
type taskSource struct {
	ID         string
	Dir        string
	Session    string
	Native     map[string]any // the source results.jsonl row (the label)
	Superseded []string       // earlier attempts of the same instance, skipped
}

// findTasks lists the cortex-<instance>[.rN] dirs under a batch dir, keeping
// the latest attempt per instance and skipping runs labeled invalid.
func findTasks(batch string) ([]taskSource, error) {
	dirs, err := filepath.Glob(filepath.Join(batch, "cortex-*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	byID := map[string]taskSource{}
	for _, dir := range dirs {
		base := filepath.Base(dir)
		id, attempt := instanceOf(base)
		if invalid(dir) {
			continue
		}
		sess, _ := filepath.Glob(filepath.Join(dir, "instances", "*", "cortex", "sessions", "*.jsonl"))
		rows, _ := readRows(filepath.Join(dir, "results.jsonl"))
		if len(sess) == 0 || len(rows) == 0 {
			continue
		}
		t := taskSource{ID: id, Dir: dir, Session: largest(sess), Native: rows[0]}
		if prev, ok := byID[id]; ok {
			_, prevAttempt := instanceOf(filepath.Base(prev.Dir))
			if prevAttempt > attempt {
				prev.Superseded = append(prev.Superseded, base)
				byID[id] = prev
				continue
			}
			t.Superseded = append(prev.Superseded, filepath.Base(prev.Dir))
		}
		byID[id] = t
	}
	out := make([]taskSource, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// instanceOf splits "cortex-<id>[.rN]" into the id and attempt number (1 for
// the first attempt).
func instanceOf(base string) (string, int) {
	id := strings.TrimPrefix(base, "cortex-")
	if i := strings.LastIndex(id, ".r"); i > 0 {
		var n int
		if _, err := fmt.Sscanf(id[i+2:], "%d", &n); err == nil && n > 0 {
			return id[:i], n
		}
	}
	return id, 1
}

// invalid reports whether a run dir is labeled invalid in its run.v1.json.
func invalid(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, benchstore.RunFileV1))
	if err != nil {
		return false
	}
	var r benchstore.RunV1
	if json.Unmarshal(b, &r) != nil {
		return false
	}
	_, bad := r.Labels["invalid"]
	return bad
}

func largest(paths []string) string {
	best, size := paths[0], int64(-1)
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.Size() > size {
			best, size = p, fi.Size()
		}
	}
	return best
}

func readRows(path string) ([]map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, ln := range strings.Split(string(b), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) == nil {
			rows = append(rows, m)
		}
	}
	return rows, nil
}

// samplePoints keeps at most k mid-turn points, evenly spaced and always
// including the last one, plus every finalize point.
func samplePoints(pts []decide.Point, k int) []decide.Point {
	var mid, out []decide.Point
	for _, p := range pts {
		if !p.Final {
			mid = append(mid, p)
		}
	}
	keep := map[int]bool{}
	if k > 0 && len(mid) > k {
		for i := 0; i < k; i++ {
			keep[(i+1)*len(mid)/k-1] = true
		}
	} else {
		for i := range mid {
			keep[i] = true
		}
	}
	i := 0
	for _, p := range pts {
		if p.Final {
			out = append(out, p)
			continue
		}
		if keep[i] {
			out = append(out, p)
		}
		i++
	}
	return out
}

// asksFor is the question set at a point: next_move + looping mid-turn,
// claim_check at finalize. recall_target is not asked on replay — a stored
// single-turn run has no demoted outline to offer as choices.
func asksFor(p decide.Point) []decide.Ask {
	if p.Final {
		return []decide.Ask{{Q: decide.ClaimCheck, Choices: decide.ClaimChoices}}
	}
	ids := make([]string, 0, len(p.Snapshot.Steps)+1)
	for _, s := range p.Snapshot.Steps {
		ids = append(ids, s.ID)
	}
	return []decide.Ask{
		{Q: decide.NextMove, Choices: decide.Moves},
		{Q: decide.Looping, Choices: append(ids, decide.None), Multi: true},
	}
}

// dumpPrompts prints what the decider would be asked at each sampled point.
func dumpPrompts(o *options) error {
	tasks, err := findTasks(o.source)
	if err != nil {
		return err
	}
	only := onlySet(o.only)
	for _, t := range tasks {
		if only != nil && !only[t.ID] {
			continue
		}
		turns, err := decide.ReadTranscript(t.Session)
		if err != nil {
			return err
		}
		for _, tr := range turns {
			for _, pt := range samplePoints(decide.Points(tr), o.points) {
				fmt.Printf("===== %s turn %d round %d final=%v actual=%v\n%s\n", t.ID, pt.Turn, pt.Round, pt.Final, pt.Actual, decide.RenderPrompt(pt.Snapshot, asksFor(pt)))
			}
		}
	}
	return nil
}

func runReplay(o *options, d decide.Decider, p *pacer) error {
	if o.source == "" {
		return errors.New("replay needs --source <batch dir>")
	}
	tasks, err := findTasks(o.source)
	if err != nil {
		return err
	}
	if only := onlySet(o.only); only != nil {
		tasks = slices.DeleteFunc(tasks, func(t taskSource) bool { return !only[t.ID] })
	}
	if o.maxTasks > 0 && len(tasks) > o.maxTasks {
		tasks = tasks[:o.maxTasks]
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no cortex task dirs with a session transcript under %s", o.source)
	}
	r, err := begin(o, "decide-replay")
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			if _, ferr := r.Finish("run ended early"); ferr != nil {
				fmt.Fprintf(os.Stderr, "benchstore: failed to record the partial run: %v\n", ferr)
			}
			benchstore.SyncBestEffort(os.Stderr)
		}
	}()
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.ID)
	}
	meta := runMeta(o, map[string]any{
		"ids":              ids,
		"source":           o.source,
		"points_per_task":  o.points,
		"dataset":          "replay:" + filepath.Base(o.source),
		"status_semantics": "status is the SOURCE task's outcome (the label); the decider's answers are in decisions.jsonl",
	})
	if err := writeJSON(filepath.Join(r.Dir, "run.json"), meta); err != nil {
		return err
	}
	results, err := openJSONL(filepath.Join(r.Dir, "results.jsonl"))
	if err != nil {
		return err
	}
	defer results.close()
	decisions, err := openJSONL(filepath.Join(r.Dir, "decisions.jsonl"))
	if err != nil {
		return err
	}
	defer decisions.close()

	fmt.Printf("decide replay | %d task(s) from %s | %s | prompt %s\n", len(tasks), o.source, o.model, decide.PromptVersion())
	for i, t := range tasks {
		row, err := replayTask(o, d, p, t, decisions)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", t.ID, err)
			continue
		}
		if err := results.add(row); err != nil {
			return err
		}
		fmt.Printf("[%d/%d] %-40s label=%-5v points=%v ok=%v loop_flags=%v claim=%v agree=%v\n", i+1, len(tasks), t.ID,
			row["resolved"], row["decide_points"], row["decide_ok"], row["looping_flags"], row["claim_check"], row["next_move_agree"])
	}
	meta["ended_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := writeJSON(filepath.Join(r.Dir, "run.json"), meta); err != nil {
		return err
	}
	finished = true
	return finishRun(r, "")
}

func replayTask(o *options, d decide.Decider, p *pacer, t taskSource, decisions *jsonl) (map[string]any, error) {
	turns, err := decide.ReadTranscript(t.Session)
	if err != nil {
		return nil, err
	}
	// The label: the source row, under the keys benchstore normalizes.
	row := map[string]any{
		"instance_id":   t.ID,
		"resolved":      t.Native["resolved"] == true,
		"failure_class": t.Native["failure_class"],
		"eval_status":   t.Native["eval_status"],
		"source_dir":    filepath.Base(t.Dir),
		"session":       filepath.Base(t.Session),
	}
	if len(t.Superseded) > 0 {
		row["superseded"] = t.Superseded
	}
	var (
		points, ok, agree, agreeOf, loopFlags int
		firstLoop                             = -1
		latencies                             []int64
		claim                                 = ""
		rounds, failed                        int
	)
	for _, tr := range turns {
		all := decide.Points(tr)
		rounds += len(tr.Batches)
		for _, b := range tr.Batches {
			for _, c := range b.Calls {
				if decide.IsFailure(c.Result) {
					failed++
				}
			}
		}
		for _, pt := range samplePoints(all, o.points) {
			asks := asksFor(pt)
			dec, attempts := ask(o, d, p, pt.Snapshot, asks)
			points++
			if dec.Status == decide.StatusOK {
				ok++
				latencies = append(latencies, dec.Latency.Milliseconds())
			}
			for _, a := range asks {
				drow := map[string]any{
					"task_id":        t.ID,
					"label_resolved": row["resolved"],
					"label_class":    row["failure_class"],
					"turn":           pt.Turn,
					"round":          pt.Round,
					"rounds_total":   len(tr.Batches),
					"final":          pt.Final,
					"question":       a.Q,
					"choices":        a.Choices,
					"status":         dec.Status,
					"latency_ms":     dec.Latency.Milliseconds(),
					"attempts":       attempts,
					"model":          dec.Model,
					"prompt_version": dec.PromptVersion,
					"actual":         pt.Actual,
					"actual_tools":   pt.ActualTools,
				}
				if pt.Window > 0 {
					drow["window_ms"] = pt.Window.Milliseconds()
					if dec.Status == decide.StatusOK {
						drow["ready_next_round"] = dec.Latency <= pt.Window
					}
				}
				if dec.Detail != "" {
					drow["detail"] = dec.Detail
				}
				if ans, has := dec.Pick(a.Q); has {
					drow["picks"] = ans.Picks
					switch a.Q {
					case decide.NextMove:
						if len(pt.Actual) > 0 {
							hit := slices.Contains(pt.Actual, ans.Picks[0])
							drow["agree"] = hit
							agreeOf++
							if hit {
								agree++
							}
						}
					case decide.Looping:
						if !slices.Equal(ans.Picks, []string{decide.None}) {
							loopFlags++
							if firstLoop < 0 {
								firstLoop = pt.Round
							}
						}
					case decide.ClaimCheck:
						claim = ans.Picks[0]
					}
				}
				if err := decisions.add(drow); err != nil {
					return nil, err
				}
			}
		}
	}
	row["decide_points"] = points
	row["decide_ok"] = ok
	row["looping_flags"] = loopFlags
	if firstLoop >= 0 {
		row["first_loop_round"] = firstLoop
	}
	row["claim_check"] = claim
	if agreeOf > 0 {
		row["next_move_agree"] = float64(agree) / float64(agreeOf)
	}
	if len(latencies) > 0 {
		slices.Sort(latencies)
		row["latency_ms_p50"] = latencies[len(latencies)/2]
		row["latency_ms_p90"] = latencies[len(latencies)*9/10]
	}
	// The cheap mechanical signals the decider must beat.
	row["rounds"] = rounds
	row["failed_steps"] = failed
	row["tool_calls"] = t.Native["tool_calls"]
	return row, nil
}
