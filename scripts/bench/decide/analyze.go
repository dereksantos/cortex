package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/dereksantos/cortex/internal/decide"
)

// analyze reads a finished decide-replay run and asks the shadow question:
// does any decider signal separate failed tasks from resolved ones better
// than the cheap mechanical signals already in the transcript? Each signal is
// scored by AUROC against the label "task failed" (0.5 = no signal; a signal
// that rises with failure scores above 0.5). It prints the table and writes
// analysis.json beside the run's results — added once, never overwritten.

// signal is one per-task feature, higher = predicted more likely to fail.
type signal struct {
	Name   string
	Kind   string // "decider" | "mechanical"
	Values func(row map[string]any) (float64, bool)
}

var signals = []signal{
	{"looping_flags", "decider", numField("looping_flags")},
	{"any_looping_flag", "decider", func(r map[string]any) (float64, bool) {
		v, ok := numField("looping_flags")(r)
		return b2f(v > 0), ok
	}},
	{"claim_unsupported", "decider", func(r map[string]any) (float64, bool) {
		c, _ := r["claim_check"].(string)
		if c == "" {
			return 0, false
		}
		return b2f(c == "unsupported"), true
	}},
	{"next_move_disagree", "decider", func(r map[string]any) (float64, bool) {
		v, ok := numField("next_move_agree")(r)
		return 1 - v, ok
	}},
	{"rounds", "mechanical", numField("rounds")},
	{"failed_steps", "mechanical", numField("failed_steps")},
	{"tool_calls", "mechanical", numField("tool_calls")},
}

func numField(k string) func(map[string]any) (float64, bool) {
	return func(r map[string]any) (float64, bool) {
		v, ok := r[k].(float64)
		return v, ok
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// auroc is the Mann-Whitney probability that a random failed task scores
// higher than a random resolved one (ties count half).
func auroc(failed, resolved []float64) float64 {
	if len(failed) == 0 || len(resolved) == 0 {
		return 0.5
	}
	var wins float64
	for _, f := range failed {
		for _, r := range resolved {
			switch {
			case f > r:
				wins++
			case f == r:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(failed)*len(resolved))
}

type signalResult struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	AUROC    float64 `json:"auroc"`
	NFailed  int     `json:"n_failed"`
	NPassed  int     `json:"n_resolved"`
	MeanFail float64 `json:"mean_failed"`
	MeanPass float64 `json:"mean_resolved"`
}

type moveAgree struct {
	Label  string         `json:"label"`
	N      int            `json:"n"`
	Agree  float64        `json:"agree"`
	ByPick map[string]int `json:"by_pick"`
}

type analysis struct {
	Run         string         `json:"run"`
	Tasks       int            `json:"tasks"`
	Failed      int            `json:"failed"`
	Signals     []signalResult `json:"signals"`
	Decisions   int            `json:"decisions"`
	StatusCount map[string]int `json:"status_count"`
	LatencyP50  int64          `json:"latency_ms_p50"`
	LatencyP90  int64          `json:"latency_ms_p90"`
	NextMove    []moveAgree    `json:"next_move_by_label"`
	// Readiness: of the ok mid-turn decisions with a known window, the share
	// whose latency fit inside one agent round — i.e. that a live decider
	// would have had ready for the send after the next one (it can never make
	// the very next send; see decide.Point.Window).
	ReadyN     int     `json:"ready_n"`
	ReadyShare float64 `json:"ready_share"`
	WindowP50  int64   `json:"window_ms_p50"`
	Note       string  `json:"note"`
}

func runAnalyze(dir string, w io.Writer) error {
	rows, err := readRows(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return fmt.Errorf("failed to read results: %w", err)
	}
	decs, err := readRows(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		return fmt.Errorf("failed to read decisions: %w", err)
	}
	a := analysis{Run: filepath.Base(dir), Tasks: len(rows), StatusCount: map[string]int{},
		Note: "AUROC vs label task-failed; 0.5 = no signal. Small n: read as a screen, not a result."}
	for _, r := range rows {
		if r["resolved"] != true {
			a.Failed++
		}
	}
	for _, s := range signals {
		var f, p []float64
		for _, r := range rows {
			v, ok := s.Values(r)
			if !ok {
				continue
			}
			if r["resolved"] == true {
				p = append(p, v)
			} else {
				f = append(f, v)
			}
		}
		a.Signals = append(a.Signals, signalResult{Name: s.Name, Kind: s.Kind, AUROC: auroc(f, p),
			NFailed: len(f), NPassed: len(p), MeanFail: mean(f), MeanPass: mean(p)})
	}

	windows := sourceWindows(dir)
	var wins []int64
	ready := 0

	// Per-decision view: status, latency, and next_move agreement by label.
	seenCall := map[string]bool{}
	var lat []int64
	agree := map[string]*moveAgree{"resolved": {Label: "resolved", ByPick: map[string]int{}}, "failed": {Label: "failed", ByPick: map[string]int{}}}
	agreeHits := map[string]int{}
	for _, d := range decs {
		key := fmt.Sprint(d["task_id"], "/", d["turn"], "/", d["round"])
		if !seenCall[key] { // one call answers every question at a point
			seenCall[key] = true
			a.Decisions++
			st, _ := d["status"].(string)
			a.StatusCount[st]++
			if st == "ok" {
				if v, ok := d["latency_ms"].(float64); ok {
					lat = append(lat, int64(v))
					w, has := d["window_ms"].(float64)
					if !has {
						w, has = windows[key]
					}
					if has && w > 0 {
						a.ReadyN++
						wins = append(wins, int64(w))
						if v <= w {
							ready++
						}
					}
				}
			}
		}
		if d["question"] != "next_move" {
			continue
		}
		hit, has := d["agree"].(bool)
		if !has {
			continue
		}
		lbl := "failed"
		if d["label_resolved"] == true {
			lbl = "resolved"
		}
		m := agree[lbl]
		m.N++
		if hit {
			agreeHits[lbl]++
		}
		if picks, ok := d["picks"].([]any); ok && len(picks) > 0 {
			m.ByPick[fmt.Sprint(picks[0])]++
		}
	}
	for _, lbl := range []string{"resolved", "failed"} {
		m := agree[lbl]
		if m.N > 0 {
			m.Agree = float64(agreeHits[lbl]) / float64(m.N)
		}
		a.NextMove = append(a.NextMove, *m)
	}
	if len(lat) > 0 {
		slices.Sort(lat)
		a.LatencyP50, a.LatencyP90 = lat[len(lat)/2], lat[len(lat)*9/10]
	}
	if a.ReadyN > 0 {
		a.ReadyShare = float64(ready) / float64(a.ReadyN)
		slices.Sort(wins)
		a.WindowP50 = wins[len(wins)/2]
	}

	printAnalysis(w, a)
	out := filepath.Join(dir, "analysis.json")
	if _, err := os.Stat(out); err == nil {
		fmt.Fprintf(w, "(analysis.json already present — not overwritten)\n")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSON(out, a)
}

// sourceWindows recomputes each point's window from the replayed source
// transcripts (runs recorded before decisions carried window_ms), keyed
// task/turn/round as the decision rows are.
func sourceWindows(dir string) map[string]float64 {
	out := map[string]float64{}
	var meta map[string]any
	if b, err := os.ReadFile(filepath.Join(dir, "run.json")); err != nil || json.Unmarshal(b, &meta) != nil {
		return out
	}
	src, _ := meta["source"].(string)
	if src == "" {
		return out
	}
	tasks, err := findTasks(src)
	if err != nil {
		return out
	}
	for _, t := range tasks {
		turns, err := decide.ReadTranscript(t.Session)
		if err != nil {
			continue
		}
		for _, tr := range turns {
			for _, p := range decide.Points(tr) {
				if p.Window > 0 {
					out[fmt.Sprint(t.ID, "/", p.Turn, "/", p.Round)] = float64(p.Window.Milliseconds())
				}
			}
		}
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func printAnalysis(w io.Writer, a analysis) {
	fmt.Fprintf(w, "run %s: %d tasks (%d failed), %d decider calls %v, latency p50 %dms p90 %dms\n",
		a.Run, a.Tasks, a.Failed, a.Decisions, a.StatusCount, a.LatencyP50, a.LatencyP90)
	fmt.Fprintf(w, "readiness: %.0f%% of %d timed decisions fit inside one agent round (round p50 %dms) — never the very next send\n\n",
		100*a.ReadyShare, a.ReadyN, a.WindowP50)
	sigs := slices.Clone(a.Signals)
	sort.SliceStable(sigs, func(i, j int) bool { return sigs[i].AUROC > sigs[j].AUROC })
	fmt.Fprintf(w, "%-20s %-10s %6s %6s %10s %10s\n", "signal", "kind", "AUROC", "n", "mean fail", "mean pass")
	for _, s := range sigs {
		fmt.Fprintf(w, "%-20s %-10s %6.2f %6d %10.2f %10.2f\n", s.Name, s.Kind, s.AUROC, s.NFailed+s.NPassed, s.MeanFail, s.MeanPass)
	}
	fmt.Fprintln(w)
	for _, m := range a.NextMove {
		fmt.Fprintf(w, "next_move on %-8s tasks: %d points, agrees with the agent %.0f%%, picks %v\n", m.Label, m.N, 100*m.Agree, m.ByPick)
	}
}
