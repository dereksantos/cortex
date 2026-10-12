// Command benchstore manages the durable benchmark store (internal/benchstore,
// docs/benchmarks.md): every benchmark/eval run's record, outside any git
// working tree, under $CORTEX_BENCH_HOME (default ~/.cortex/benchmarks).
//
//	benchstore root                                  print the store root
//	benchstore list [--bench b] [--json]             runs, oldest first, one line each
//	benchstore csv  [--bench b]                      the same as CSV, for graphing
//	benchstore reindex                               index runs found on disk but not finished in the index
//	benchstore begin --bench b --run-id id [--label k=v]   open a run dir (shell runners); prints its path
//	benchstore finish --bench b --run-id id [--note s]     record a shell runner's finish + summary
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/dereksantos/cortex/internal/benchstore"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "benchstore: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: benchstore <root|list|csv|reindex|begin|finish> [flags]")
	}
	root, err := benchstore.Root()
	if err != nil {
		return err
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	bench := fs.String("bench", "", "benchmark name (e.g. swebench, polyglot)")
	runID := fs.String("run-id", "", "run id")
	asJSON := fs.Bool("json", false, "print JSON lines")
	note := fs.String("note", "", "free-text note on the finish event")
	ephemeral := fs.Bool("ephemeral", false, "allow a run dir inside a git working tree")
	labels := benchstore.LabelFlag{}
	fs.Var(labels, "label", "key=value tag (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch cmd {
	case "root":
		fmt.Println(root)
	case "reindex":
		added, err := benchstore.Reindex(root)
		if err != nil {
			return err
		}
		for _, e := range added {
			fmt.Printf("indexed %s/%s\n", e.Bench, e.RunID)
		}
		fmt.Printf("%d run(s) added to %s/index.jsonl\n", len(added), root)
	case "begin":
		r, err := benchstore.Begin(benchstore.BeginOptions{
			Bench: *bench, RunID: *runID, Ephemeral: *ephemeral, SourceRepo: ".", Labels: labels,
		})
		if err != nil {
			return err
		}
		fmt.Println(r.Dir)
	case "finish":
		s, err := benchstore.FinishByID(root, *bench, *runID, *note)
		if err != nil {
			return err
		}
		fmt.Printf("recorded %s/%s: %d/%d passed\n", *bench, *runID, s.Pass, s.Pass+s.Fail)
	case "list", "csv":
		entries, err := benchstore.ReadIndex(root)
		if err != nil {
			return err
		}
		var rows []benchstore.Entry
		for _, e := range benchstore.Latest(entries) {
			if *bench == "" || e.Bench == *bench {
				rows = append(rows, e)
			}
		}
		switch {
		case cmd == "csv":
			return writeCSV(rows)
		case *asJSON:
			enc := json.NewEncoder(os.Stdout)
			for _, e := range rows {
				if err := enc.Encode(e); err != nil {
					return err
				}
			}
		default:
			writeTable(rows)
		}
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func writeTable(rows []benchstore.Entry) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STARTED\tBENCH\tRUN\tMODEL\tCOMMIT\tSTATE\tPASS\tRATE\tLABELS")
	for _, e := range rows {
		pass, rate := "-", "-"
		if s := e.Summary; s != nil && s.N > 0 {
			pass = fmt.Sprintf("%d/%d", s.Pass, s.Pass+s.Fail)
			rate = fmt.Sprintf("%.0f%%", 100*s.PassRate())
		}
		commit := e.Commit
		if len(commit) > 7 {
			commit = commit[:7]
		}
		if e.Dirty {
			commit += "+"
		}
		started := e.StartedAt
		if len(started) > 16 {
			started = started[:16]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			started, e.Bench, e.RunID, e.Model, commit, e.Event, pass, rate, benchstore.LabelFlag(e.Labels).String())
	}
	tw.Flush()
}

func writeCSV(rows []benchstore.Entry) error {
	w := csv.NewWriter(os.Stdout)
	_ = w.Write([]string{"started_at", "ended_at", "bench", "run_id", "model", "commit", "dirty", "state",
		"n", "pass", "fail", "skipped", "pass_rate", "tokens_in", "tokens_out", "cost_usd", "wall_sec", "labels", "dir"})
	for _, e := range rows {
		s := benchstore.Summary{}
		if e.Summary != nil {
			s = *e.Summary
		}
		keys := make([]string, 0, len(e.Labels))
		for k := range e.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lbl []string
		for _, k := range keys {
			lbl = append(lbl, k+"="+e.Labels[k])
		}
		_ = w.Write([]string{e.StartedAt, e.EndedAt, e.Bench, e.RunID, e.Model, e.Commit, strconv.FormatBool(e.Dirty), e.Event,
			strconv.Itoa(s.N), strconv.Itoa(s.Pass), strconv.Itoa(s.Fail), strconv.Itoa(s.Skipped),
			strconv.FormatFloat(s.PassRate(), 'f', 4, 64),
			strconv.FormatInt(s.TokensIn, 10), strconv.FormatInt(s.TokensOut, 10),
			strconv.FormatFloat(s.CostUSD, 'f', 4, 64), strconv.FormatFloat(s.WallSec, 'f', 1, 64),
			strings.Join(lbl, ";"), e.Dir})
	}
	w.Flush()
	return w.Error()
}
