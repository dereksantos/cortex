package journal

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PruneOptions configures one prune pass. The active (highest-
// numbered) segment is never pruned — only closed segments are
// eligible. Pruning operates per writer-class directory.
type PruneOptions struct {
	// MaxAge drops a closed segment when its newest entry is older
	// than this. Zero means "no age limit".
	MaxAge time.Duration

	// MaxBytes drops the oldest closed segments until total class-
	// directory bytes (excluding the active segment) drop below this.
	// Zero means "no byte limit". Age-based pruning runs first; byte-
	// based pruning takes over if the class is still over the limit.
	MaxBytes int64

	// DryRun reports what would be removed without touching disk.
	DryRun bool

	// Now is injectable for tests. Zero defaults to time.Now().
	Now time.Time
}

// PruneReport summarizes one prune pass over one class directory.
// Removed lists the segment numbers that were (or would be) deleted,
// in numeric order. Reasons[i] explains why Removed[i] was selected.
type PruneReport struct {
	ClassDir       string
	SegmentsBefore int
	BytesBefore    int64
	Removed        []int
	Reasons        []string
	BytesFreed     int64
	DryRun         bool
}

// Prune deletes closed segments from one writer-class directory per
// the policy. Returns a structured report; on error the report
// describes what completed before the failure.
//
// Age-based selection: a segment is eligible when its newest entry's
// timestamp is older than now-MaxAge. The active (highest-numbered)
// segment is always retained; without it the next Append would lose
// monotonic-offset invariants.
//
// Byte-based selection: after age-based pruning, if total directory
// bytes (active segment excluded) still exceed MaxBytes, the oldest
// closed segments are removed in order until the budget fits.
func Prune(classDir string, opts PruneOptions) (PruneReport, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	report := PruneReport{ClassDir: classDir, DryRun: opts.DryRun}

	nums, err := listSegments(classDir)
	if err != nil {
		return report, err
	}
	report.SegmentsBefore = len(nums)
	if len(nums) < 2 {
		// Only the active segment (or none) — nothing closed to prune.
		return report, nil
	}
	closed := nums[:len(nums)-1] // exclude active tail

	// Pre-compute closed-segment sizes for the byte-budget pass.
	sizes := make(map[int]int64, len(closed))
	var totalClosed int64
	for _, n := range closed {
		path, _ := resolveSegmentPath(classDir, n)
		if path == "" {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		sizes[n] = fi.Size()
		totalClosed += fi.Size()
	}
	report.BytesBefore = totalClosed

	dropped := make(map[int]bool)

	// Pass 1: age-based.
	if opts.MaxAge > 0 {
		horizon := opts.Now.Add(-opts.MaxAge)
		for _, n := range closed {
			newest, err := newestSegmentTS(classDir, n)
			if err != nil || newest.IsZero() {
				// Can't determine age — skip rather than guess. The
				// segment will either get pruned next pass when it's
				// readable, or surface a parse error elsewhere.
				continue
			}
			if newest.Before(horizon) {
				dropped[n] = true
				report.Removed = append(report.Removed, n)
				report.Reasons = append(report.Reasons,
					fmt.Sprintf("age: newest %s < horizon %s", newest.UTC().Format(time.RFC3339), horizon.UTC().Format(time.RFC3339)))
			}
		}
	}

	// Pass 2: byte-budget. Operates on the survivors in oldest-first
	// order so we evict the lowest-value segments first.
	if opts.MaxBytes > 0 {
		var keptBytes int64
		for _, n := range closed {
			if !dropped[n] {
				keptBytes += sizes[n]
			}
		}
		for _, n := range closed {
			if keptBytes <= opts.MaxBytes {
				break
			}
			if dropped[n] {
				continue
			}
			dropped[n] = true
			report.Removed = append(report.Removed, n)
			report.Reasons = append(report.Reasons,
				fmt.Sprintf("bytes: dir was %d, budget %d", keptBytes, opts.MaxBytes))
			keptBytes -= sizes[n]
		}
	}

	// Apply deletes (or skip on DryRun).
	for _, n := range report.Removed {
		report.BytesFreed += sizes[n]
		if opts.DryRun {
			continue
		}
		if err := removeSegment(classDir, n); err != nil {
			return report, fmt.Errorf("remove segment %d: %w", n, err)
		}
	}
	return report, nil
}

// PruneAll walks every writer-class directory under journalRoot and
// applies opts to each. Mirrors CompactClassDirAll's contract.
func PruneAll(journalRoot string, opts PruneOptions) ([]PruneReport, error) {
	entries, err := os.ReadDir(journalRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var reports []PruneReport
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := Prune(filepath.Join(journalRoot, e.Name()), opts)
		if err != nil {
			return reports, err
		}
		reports = append(reports, r)
	}
	return reports, nil
}

// newestSegmentTS streams the segment and returns its highest entry
// timestamp. Cheap on small segments (the common case at default
// MaxSegmentEntries=1000) and bounded on large ones by segment size.
// Returns the zero time if the segment is empty or unreadable.
func newestSegmentTS(classDir string, n int) (time.Time, error) {
	path, err := resolveSegmentPath(classDir, n)
	if err != nil {
		return time.Time{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	var rd io.Reader = f
	if strings.HasSuffix(path, segmentExtGZ) {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return time.Time{}, err
		}
		defer gz.Close()
		rd = gz
	}
	br := bufio.NewReader(rd)
	var newest time.Time
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 1 {
			// Strip newline. Parse just the ts field — full Entry
			// unmarshal is wasteful here.
			var probe struct {
				TS time.Time `json:"ts"`
			}
			if jerr := json.Unmarshal(line[:len(line)-1], &probe); jerr == nil {
				if probe.TS.After(newest) {
					newest = probe.TS
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return newest, err
		}
	}
	return newest, nil
}

// removeSegment unlinks both possible on-disk forms (uncompressed +
// gzipped). Idempotent — a missing form is not an error.
func removeSegment(classDir string, n int) error {
	for _, path := range []string{segmentPath(classDir, n), segmentPathGZ(classDir, n)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
