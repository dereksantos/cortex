package benchstore

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/fslock"
)

// EnvR2Bucket names the R2 bucket that holds the off-machine copy; "off"
// disables the upload (the local git commit still happens). Unset, the
// bucket comes from <root>/.sync/config.json ({"r2_bucket": "..."}) — the
// target is local configuration, never part of this (public) repo.
const EnvR2Bucket = "CORTEX_BENCH_R2_BUCKET"

// syncConfig is <root>/.sync/config.json.
type syncConfig struct {
	R2Bucket string `json:"r2_bucket"`
}

// storeGitignore keeps the store's git history to the small records — what
// ran and how it scored — and leaves the bulky evidence (trajectories, logs,
// workdirs, dataset caches) to the R2 bundles.
const storeGitignore = `# benchstore: git tracks the records; R2 bundles hold everything.
*
!*/
!.gitignore
!index.jsonl
!run.json
!results.jsonl
!*.v1.json
!*.v1.jsonl
!source.patch
!source.patch.err
!predictions.jsonl
!usage.jsonl
!/.sync/r2.jsonl
!/archive/**
`

// Uploader puts one local file at a key in the off-machine store.
type Uploader interface {
	Put(key, file string) error
	Name() string
}

// WranglerR2 uploads with `wrangler r2 object put --remote`. Dir is where
// wrangler runs: it writes a .wrangler/ cache into its working directory, so
// it runs in the store (whose .gitignore excludes it), never in a repo.
type WranglerR2 struct{ Bucket, Dir string }

// Put implements Uploader.
func (w WranglerR2) Put(key, file string) error {
	cmd := exec.Command("wrangler", "r2", "object", "put", w.Bucket+"/"+key, "--file", file, "--remote")
	cmd.Dir = w.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrangler r2 put %s: %w\n%s", key, err, tail(string(out), 400))
	}
	return nil
}

// Name implements Uploader.
func (w WranglerR2) Name() string { return "r2://" + w.Bucket }

// DefaultUploader returns the configured R2 uploader, or nil when uploads are
// switched off or wrangler is not installed (the caller reports why).
func DefaultUploader(root string) (Uploader, string) {
	bucket := strings.TrimSpace(os.Getenv(EnvR2Bucket))
	if bucket == "off" {
		return nil, EnvR2Bucket + "=off"
	}
	if bucket == "" {
		var c syncConfig
		if b, err := os.ReadFile(filepath.Join(root, ".sync", "config.json")); err == nil {
			_ = json.Unmarshal(b, &c)
		}
		bucket = strings.TrimSpace(c.R2Bucket)
	}
	if bucket == "" {
		return nil, "no off-machine target configured: set r2_bucket in " + filepath.Join(root, ".sync", "config.json")
	}
	if _, err := exec.LookPath("wrangler"); err != nil {
		return nil, "wrangler not installed"
	}
	return WranglerR2{Bucket: bucket, Dir: root}, ""
}

// SyncReport says what one Sync did.
type SyncReport struct {
	Committed bool
	Uploaded  []string
	Unchanged int
	Offsite   string // uploader name, or why there is none
}

// UploadRecord is one line of <root>/.sync/r2.jsonl: a bundle known to be
// in the off-machine store.
type UploadRecord struct {
	Key  string `json:"key"`
	Unit string `json:"unit"`
	Hash string `json:"sha256"`
	At   string `json:"at"`
	Dest string `json:"dest"`
}

// Sync makes the store durable beyond this disk:
//
//  1. the store is a git repo (created on first use) and the records are
//     committed — local history of every run's metadata and results;
//  2. every unit — each top-level entry of runs/<bench>/ and of archive/ —
//     is bundled as a .tar.gz keyed by its content hash and uploaded once.
//     A changed unit gets a NEW key, so an upload never overwrites an
//     earlier one. The remote copy only accumulates: a unit that changes
//     after upload (a rescore, a run still writing) is uploaded whole
//     again, and nothing prunes superseded bundles.
//
// up may be nil (no off-machine copy); the commit still happens.
func Sync(root string, up Uploader) (SyncReport, error) {
	var rep SyncReport
	if err := ensureGit(root); err != nil {
		return rep, err
	}
	// One sync at a time: two runners finishing together would otherwise
	// race on git's index.lock and on the upload ledger.
	unlock, err := lockFile(filepath.Join(root, ".sync", "lock"))
	if err != nil {
		return rep, err
	}
	defer unlock()
	// Records first: a failed or slow upload must never leave the local
	// history uncommitted.
	if rep.Committed, err = commitRecords(root); err != nil {
		return rep, err
	}
	if up == nil {
		return rep, nil
	}
	rep.Offsite = up.Name()
	done, err := readUploads(root)
	if err != nil {
		return rep, err
	}
	units, err := syncUnits(root)
	if err != nil {
		return rep, err
	}
	var failed []string
	for _, u := range units {
		h, secret, err := hashTree(filepath.Join(root, u))
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", u, err))
			continue
		}
		if secret != "" {
			// Never ship a credential off-machine. The unit stays local
			// (and in the local git records); someone has to scrub it.
			failed = append(failed, fmt.Sprintf("%s: NOT uploaded — %s looks like it holds an API key; scrub it, then sync again", u, secret))
			continue
		}
		key := filepath.ToSlash(u) + "/" + h[:16] + ".tar.gz"
		if done[key] {
			rep.Unchanged++
			continue
		}
		if err := uploadBundle(root, u, key, h, up); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", u, err))
			continue
		}
		rep.Uploaded = append(rep.Uploaded, key)
	}
	// The index is a projection, but it is the one file a trend view
	// needs; keep its latest copy addressable without unpacking bundles.
	if idx := filepath.Join(root, "index.jsonl"); fileExists(idx) {
		if err := up.Put("index.jsonl", idx); err != nil {
			failed = append(failed, fmt.Sprintf("index.jsonl: %v", err))
		}
	}
	// The upload ledger changed: commit it too.
	if c, err := commitRecords(root); err != nil {
		failed = append(failed, fmt.Sprintf("commit ledger: %v", err))
	} else {
		rep.Committed = rep.Committed || c
	}
	if len(failed) > 0 {
		return rep, fmt.Errorf("%d unit(s) not synced:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return rep, nil
}

// SyncBestEffort runs Sync with the default uploader and reports to w. A
// sync failure never fails a run — the local record already exists — but it
// is printed loudly, with the command that retries it.
func SyncBestEffort(w io.Writer) {
	root, err := Root()
	if err != nil {
		fmt.Fprintf(w, "benchstore sync: %v\n", err)
		return
	}
	up, why := DefaultUploader(root)
	rep, err := Sync(root, up)
	switch {
	case err != nil:
		fmt.Fprintf(w, "⚠ benchstore sync FAILED — the run is recorded locally but NOT off-machine: %v\n  retry: go run ./scripts/bench/benchstore sync\n", err)
	case up == nil:
		fmt.Fprintf(w, "⚠ benchstore: recorded locally (git), NOT off-machine (%s)\n", why)
	default:
		fmt.Fprintf(w, "benchstore: synced to %s (%d uploaded, %d unchanged)\n", rep.Offsite, len(rep.Uploaded), rep.Unchanged)
	}
}

// syncUnits lists the bundle units: runs/<bench>/<entry> and archive/<entry>.
func syncUnits(root string) ([]string, error) {
	var units []string
	benches, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, b := range benches {
		if !b.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, "runs", b.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			units = append(units, filepath.Join("runs", b.Name(), e.Name()))
		}
	}
	arch, err := os.ReadDir(filepath.Join(root, "archive"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range arch {
		units = append(units, filepath.Join("archive", e.Name()))
	}
	sort.Strings(units)
	return units, nil
}

// secretMarkers are byte patterns of provider API keys. Sync refuses to
// upload a unit containing one: bundles leave the machine, and runner logs
// and agent trajectories can echo an environment.
var secretMarkers = [][]byte{
	[]byte("sk-or-v1-"),  // OpenRouter
	[]byte("sk-ant-api"), // Anthropic
	[]byte("sk-proj-"),   // OpenAI project keys
	[]byte("ghp_"),       // GitHub personal tokens
	[]byte("github_pat_"),
}

// scanWriter looks for secretMarkers across write boundaries.
type scanWriter struct {
	tail  []byte
	found string
}

func (w *scanWriter) Write(p []byte) (int, error) {
	if w.found == "" {
		buf := append(w.tail, p...)
		for _, m := range secretMarkers {
			if bytes.Contains(buf, m) {
				w.found = string(m) + "…"
				break
			}
		}
		keep := 32
		if len(buf) < keep {
			keep = len(buf)
		}
		w.tail = append([]byte(nil), buf[len(buf)-keep:]...)
	}
	return len(p), nil
}

// hashTree hashes a file or directory — every regular file's relative path,
// mode, and content, in path order; symlinks hash their target string — and
// reports the first file holding a secretMarker ("" for none).
func hashTree(path string) (sum, secretIn string, err error) {
	h := sha256.New()
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			fmt.Fprintf(h, "L %s %s\n", rel, target)
		case info.Mode().IsRegular():
			fmt.Fprintf(h, "F %s %o\n", rel, info.Mode().Perm())
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			sc := &scanWriter{}
			_, err = io.Copy(io.MultiWriter(h, sc), f)
			f.Close()
			if err != nil {
				return err
			}
			if sc.found != "" && secretIn == "" {
				secretIn = filepath.Join(filepath.Base(path), rel) + " (" + sc.found + ")"
			}
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), secretIn, err
}

func uploadBundle(root, unit, key, hash string, up Uploader) error {
	tmp, err := os.CreateTemp("", "benchstore-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := writeTarGz(tmp, root, unit); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to bundle %s: %w", unit, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := up.Put(key, tmp.Name()); err != nil {
		return err
	}
	return appendUpload(root, UploadRecord{Key: key, Unit: filepath.ToSlash(unit), Hash: hash, At: now(), Dest: up.Name()})
}

func writeTarGz(w io.Writer, root, unit string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(filepath.Join(root, unit), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			// Exactly the size the header promised: a file a live run is
			// still appending to is bundled as it was when its header was
			// written (the next sync picks up the rest under a new key).
			n, err := io.CopyN(tw, f, hdr.Size)
			f.Close()
			if err != nil && (!errors.Is(err, io.EOF) || n >= hdr.Size) {
				return err
			}
			if n < hdr.Size { // shrank meanwhile: pad so the archive stays valid
				if _, err := tw.Write(make([]byte, hdr.Size-n)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func uploadsPath(root string) string { return filepath.Join(root, ".sync", "r2.jsonl") }

func readUploads(root string) (map[string]bool, error) {
	done := map[string]bool{}
	f, err := os.Open(uploadsPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r UploadRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Key != "" {
			done[r.Key] = true
		}
	}
	return done, sc.Err()
}

func appendUpload(root string, r UploadRecord) error {
	if err := os.MkdirAll(filepath.Dir(uploadsPath(root)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(uploadsPath(root), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(r)
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func storeGit(root string, args ...string) ([]byte, error) {
	full := append([]string{"-C", root, "-c", "user.name=benchstore", "-c", "user.email=benchstore@localhost", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s in %s: %w\n%s", strings.Join(args, " "), root, err, tail(string(out), 400))
	}
	return out, nil
}

func ensureGit(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if !fileExists(filepath.Join(root, ".git")) {
		if _, err := storeGit(root, "init", "-q"); err != nil {
			return err
		}
	}
	gi := filepath.Join(root, ".gitignore")
	if b, err := os.ReadFile(gi); err != nil || string(b) != storeGitignore {
		return os.WriteFile(gi, []byte(storeGitignore), 0o644)
	}
	return nil
}

func commitRecords(root string) (bool, error) {
	if _, err := storeGit(root, "add", "-A"); err != nil {
		return false, err
	}
	st, err := storeGit(root, "status", "--porcelain")
	if err != nil || len(strings.TrimSpace(string(st))) == 0 {
		return false, err
	}
	_, err = storeGit(root, "commit", "-q", "-m", "benchstore sync "+time.Now().UTC().Format(time.RFC3339))
	return err == nil, err
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// lockFile takes an exclusive flock on path (created if missing) and returns
// its release.
func lockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock %s: %w", path, err)
	}
	if err := fslock.Lock(int(f.Fd())); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to lock %s: %w", path, err)
	}
	// Closing the file releases the lock.
	return func() { f.Close() }, nil
}
