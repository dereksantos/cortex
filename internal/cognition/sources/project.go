// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"bufio"
	"context"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// ProjectSource samples files from the project directory.
//
// Files are picked via weighted reservoir sampling: each file's weight
// combines recency (mtime), size class, and git churn over the last two
// weeks. For each chosen file ProjectSource emits one or more region
// windows (offset+length) rather than the whole file, so Dream can
// explore arbitrary points inside large files.
type ProjectSource struct {
	projectRoot    string
	rng            *rand.Rand
	gitignoreRules []gitignoreRule
	observer       *Observer

	churnMu     sync.Mutex
	churnCache  map[string]int
	churnLoaded time.Time
}

// SetObserver wires the source to emit observation.project_file journal
// entries per file that contributes to a Sample. Pass nil to disable.
// Observations carry the URI + content_hash + size only — the file's
// bytes never enter the journal (principle 3).
func (p *ProjectSource) SetObserver(o *Observer) { p.observer = o }

// observeFile emits one observation.project_file entry for fc. The
// content hash is computed over the file's bytes (capped at 5 MiB by
// walkCandidates), so it's a one-shot read per observed file. Best
// effort: errors are swallowed by the observer; an unreadable file
// produces no entry.
func (p *ProjectSource) observeFile(fc fileCandidate) {
	if p.observer == nil {
		return
	}
	data, err := os.ReadFile(fc.path)
	if err != nil {
		return
	}
	p.observer.Observe(
		journal.TypeObservationProjectFile,
		"project",
		"file://"+fc.path,
		data,
		fc.mtime,
	)
}

// gitignoreRule represents a single .gitignore pattern.
type gitignoreRule struct {
	pattern  string
	negation bool // lines starting with !
	dirOnly  bool // lines ending with /
}

// NewProjectSource creates a new ProjectSource.
func NewProjectSource(projectRoot string) *ProjectSource {
	ps := &ProjectSource{
		projectRoot: projectRoot,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	ps.gitignoreRules = ps.loadGitignore()
	return ps
}

// Name returns the source identifier.
func (p *ProjectSource) Name() string {
	return "project"
}

// fileCandidate carries the per-file inputs to weighted sampling.
type fileCandidate struct {
	path    string
	relPath string
	size    int64
	mtime   time.Time
	churn   int
}

// priorityFileSet — kept for the optional "always-seed" lane. We pick a
// small number of these per cycle so the head of the project still gets
// coverage, but they no longer dominate the sample.
var priorityFileSet = []string{
	"README.md", "CLAUDE.md", "LICENSE", "go.mod",
	"package.json", "Makefile", "Dockerfile",
	".env.example", "CONTRIBUTING.md", "CHANGELOG.md",
}

// Sample picks `n` regions from the project, weighted by file freshness
// (mtime), size class, and git churn, then breaks each chosen file into
// region windows.
func (p *ProjectSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	if n <= 0 {
		return nil, nil
	}

	candidates, err := p.walkCandidates()
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Pull churn map (cached, refreshed every 5 minutes).
	churn := p.gitChurn()
	for i := range candidates {
		candidates[i].churn = churn[candidates[i].relPath]
	}

	// Optional priority seed: ~10% of n (at least 0, at most 2) from the
	// priority set. Seed slots are subtracted from the weighted draw.
	seedSlots := n / 10
	if seedSlots > 2 {
		seedSlots = 2
	}
	priorityIdx := indexCandidates(candidates)
	seeds := p.pickPrioritySeeds(seedSlots, priorityIdx)

	// Weighted reservoir over the rest.
	picked := weightedSample(candidates, n-len(seeds), p.rng)

	// Stitch and emit regions. Observe each unique file once per Sample
	// so the journal volume is proportional to selected files, not to
	// regions or to the candidate pool.
	all := append(seeds, picked...)
	items := make([]cognition.DreamItem, 0, n)
	observed := make(map[string]struct{}, len(all))
	for _, fc := range all {
		regs := p.regionsFor(fc)
		if p.observer != nil {
			if _, seen := observed[fc.path]; !seen {
				observed[fc.path] = struct{}{}
				p.observeFile(fc)
			}
		}
		for _, r := range regs {
			content, rerr := fractal.ReadRegion(fc.path, r.Offset, r.Length)
			if rerr != nil || content == "" {
				continue
			}
			items = append(items, cognition.DreamItem{
				ID:      regionItemID(fc.relPath, r.Offset),
				Source:  "project",
				Content: content,
				Path:    fc.relPath,
				Metadata: map[string]any{
					"full_path":     fc.path,
					"rel_path":      fc.relPath,
					"ext":           filepath.Ext(fc.path),
					"region_offset": r.Offset,
					"region_len":    r.Length,
					"file_size":     fc.size,
					"mtime":         fc.mtime,
					"git_churn":     fc.churn,
				},
			})
		}
	}
	return items, nil
}

// walkCandidates returns every non-excluded file under projectRoot,
// capped at 5000 to bound the work for huge repos.
func (p *ProjectSource) walkCandidates() ([]fileCandidate, error) {
	const sanityCap = 5000
	const maxFileBytes int64 = 5 * 1024 * 1024 // 5 MiB

	out := make([]fileCandidate, 0, 256)
	err := filepath.WalkDir(p.projectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if p.isHardExcludedDir(name) {
				return filepath.SkipDir
			}
			if p.isGitignored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if p.isExcluded(path, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > maxFileBytes || info.Size() == 0 {
			return nil
		}
		rel, _ := filepath.Rel(p.projectRoot, path)
		out = append(out, fileCandidate{
			path:    path,
			relPath: rel,
			size:    info.Size(),
			mtime:   info.ModTime(),
		})
		if len(out) >= sanityCap {
			return filepath.SkipAll
		}
		return nil
	})
	return out, err
}

// indexCandidates builds a relPath -> *fileCandidate map for quick
// lookup by name (used for priority-seed picking).
func indexCandidates(candidates []fileCandidate) map[string]*fileCandidate {
	m := make(map[string]*fileCandidate, len(candidates))
	for i := range candidates {
		m[candidates[i].relPath] = &candidates[i]
	}
	return m
}

// pickPrioritySeeds chooses up to `slots` priority files (in random
// order) that exist in the candidate set.
func (p *ProjectSource) pickPrioritySeeds(slots int, idx map[string]*fileCandidate) []fileCandidate {
	if slots <= 0 {
		return nil
	}
	pool := make([]string, 0, len(priorityFileSet))
	for _, name := range priorityFileSet {
		if _, ok := idx[name]; ok {
			pool = append(pool, name)
		}
	}
	p.rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if len(pool) > slots {
		pool = pool[:slots]
	}
	out := make([]fileCandidate, 0, len(pool))
	for _, name := range pool {
		out = append(out, *idx[name])
	}
	return out
}

// regionsFor decides how many windows to carve from a file: 2 windows
// for files > 32 KiB, otherwise 1.
func (p *ProjectSource) regionsFor(fc fileCandidate) []fractal.Region {
	count := 1
	if fc.size > 32*1024 {
		count = 2
	}
	regs := fractal.PickRegions(fc.size, count, p.rng)
	for i := range regs {
		regs[i].Path = fc.path
	}
	return regs
}

// weightedSample applies the Efraimidis–Spirakis weighted reservoir
// algorithm: each candidate gets a random key = -log(rand)/weight; we
// keep the n candidates with the largest keys.
func weightedSample(candidates []fileCandidate, n int, rng *rand.Rand) []fileCandidate {
	if n <= 0 || len(candidates) == 0 {
		return nil
	}
	type keyed struct {
		key float64
		fc  fileCandidate
	}
	keys := make([]keyed, 0, len(candidates))
	now := time.Now()
	for _, c := range candidates {
		w := candidateWeight(c, now)
		if w <= 0 {
			continue
		}
		u := rng.Float64()
		if u <= 0 {
			u = 1e-9
		}
		k := -math.Log(u) / w
		keys = append(keys, keyed{key: -k, fc: c}) // negate so we sort desc
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].key > keys[j].key })
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]fileCandidate, len(keys))
	for i, k := range keys {
		out[i] = k.fc
	}
	return out
}

// candidateWeight combines recency, size class, and git churn into a
// single sampling weight.
func candidateWeight(c fileCandidate, now time.Time) float64 {
	ageHours := now.Sub(c.mtime).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	recency := math.Exp(-ageHours / 72.0) // half-life ≈ 72h
	size := sizeFactor(c.size)
	churn := 1.0 + math.Log(1.0+float64(c.churn))
	return recency*size*churn + 1e-3 // floor so brand-new files still get drawn
}

func sizeFactor(s int64) float64 {
	switch {
	case s < 2*1024:
		return 0.3
	case s > 200*1024:
		return 0.3
	default:
		return 1.0
	}
}

// gitChurn returns a relPath -> commit-count map for the last 14 days,
// cached for 5 minutes.
func (p *ProjectSource) gitChurn() map[string]int {
	p.churnMu.Lock()
	if time.Since(p.churnLoaded) < 5*time.Minute && p.churnCache != nil {
		c := p.churnCache
		p.churnMu.Unlock()
		return c
	}
	p.churnMu.Unlock()

	cmd := exec.Command("git", "-C", p.projectRoot,
		"log", "--name-only", "--format=", "-n", "50",
		"--since=14 days ago")
	out, err := cmd.Output()
	churn := make(map[string]int)
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			churn[line]++
		}
	}
	p.churnMu.Lock()
	p.churnCache = churn
	p.churnLoaded = time.Now()
	p.churnMu.Unlock()
	return churn
}

// regionItemID encodes path + offset into a stable DreamItem ID.
func regionItemID(relPath string, offset int64) string {
	if offset == 0 {
		return "project:" + relPath
	}
	return "project:" + relPath + "#offset=" + strconv.FormatInt(offset, 10)
}

// isExcluded returns true if a file should never be sampled.
// Checks hard exclusions first, then gitignore rules.
func (p *ProjectSource) isExcluded(path string, isDir bool) bool {
	if p.isHardExcludedFile(path) {
		return true
	}
	if p.shouldSkipByExtension(path) {
		return true
	}
	return p.isGitignored(path, isDir)
}

// isHardExcludedDir returns true for directories that must never be entered.
func (p *ProjectSource) isHardExcludedDir(name string) bool {
	// Git internals
	if name == ".git" {
		return true
	}

	// Dependencies
	hardExcludedDirs := map[string]bool{
		"node_modules":  true,
		"vendor":        true,
		".venv":         true,
		"venv":          true,
		"__pycache__":   true,
		".mypy_cache":   true,
		".pytest_cache": true,

		// Build artifacts
		"dist":    true,
		"build":   true,
		"target":  true,
		".next":   true,
		".nuxt":   true,
		".output": true,

		// IDE/editor
		".idea": true,

		// Cortex's own state (but NOT knowledge/)
		// Handled separately in isHardExcludedFile for finer control
	}

	return hardExcludedDirs[name]
}

// isHardExcludedFile returns true for files that must never be sampled,
// regardless of gitignore. These are security-sensitive or noise files.
func (p *ProjectSource) isHardExcludedFile(path string) bool {
	rel, _ := filepath.Rel(p.projectRoot, path)
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	// === SECRETS / CREDENTIALS ===
	// .env files (but allow .env.example, .env.sample, .env.template)
	if strings.HasPrefix(lower, ".env") {
		if lower == ".env.example" || lower == ".env.sample" || lower == ".env.template" {
			return false
		}
		return true
	}

	// Key/certificate files
	secretExts := map[string]bool{
		".key": true, ".pem": true, ".p12": true, ".pfx": true,
		".keystore": true, ".jks": true, ".p8": true,
	}
	if secretExts[strings.ToLower(filepath.Ext(path))] {
		return true
	}

	// Named secret files
	secretFiles := map[string]bool{
		"id_rsa": true, "id_ed25519": true, "id_ecdsa": true, "id_dsa": true,
		"credentials.json": true, "service-account.json": true,
		"secrets.yaml": true, "secrets.yml": true, "secrets.json": true,
		".npmrc": true, ".pypirc": true, ".netrc": true,
		".docker/config.json": true,
		"htpasswd":            true, ".htpasswd": true,
	}
	if secretFiles[lower] || secretFiles[rel] {
		return true
	}

	// === CORTEX OWN STATE ===
	// Never read our own queue, db, logs, or runtime state
	if strings.HasPrefix(rel, ".cortex/") || strings.HasPrefix(rel, ".cortex\\") {
		// Allow knowledge/ (that's committed team context)
		if strings.HasPrefix(rel, ".cortex/knowledge/") || strings.HasPrefix(rel, ".cortex\\knowledge\\") {
			return false
		}
		return true
	}

	// === OS JUNK ===
	osJunk := map[string]bool{
		".ds_store": true, "thumbs.db": true, "desktop.ini": true,
	}
	return osJunk[lower]
}

// shouldSkipByExtension returns true for binary, generated, and low-signal files.
func (p *ProjectSource) shouldSkipByExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))

	skipExts := map[string]bool{
		// Binary/compiled
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".o": true, ".obj": true, ".a": true, ".lib": true,
		".class": true, ".pyc": true, ".pyo": true,

		// Images/media
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".ico": true, ".svg": true, ".webp": true, ".bmp": true,
		".mp3": true, ".mp4": true, ".wav": true, ".avi": true,
		".mov": true, ".webm": true, ".ttf": true, ".woff": true,
		".woff2": true, ".eot": true, ".otf": true,

		// Archives
		".zip": true, ".tar": true, ".gz": true, ".bz2": true,
		".rar": true, ".7z": true, ".xz": true,

		// Documents (low signal for code context)
		".pdf": true, ".doc": true, ".docx": true, ".xls": true,
		".xlsx": true, ".pptx": true,

		// Lock files (huge, low signal)
		".lock": true, ".sum": true,

		// Minified
		".min.js": true, ".min.css": true,

		// Database files
		".db": true, ".sqlite": true, ".sqlite3": true,

		// Map files
		".map": true,
	}

	if skipExts[ext] {
		return true
	}

	// Check compound extensions
	base := filepath.Base(path)
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}

	// Lock files by name
	lockFiles := map[string]bool{
		"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
		"go.sum": true, "gemfile.lock": true, "poetry.lock": true,
		"composer.lock": true, "cargo.lock": true, "flake.lock": true,
	}
	return lockFiles[strings.ToLower(base)]
}

// loadGitignore parses .gitignore from the project root.
func (p *ProjectSource) loadGitignore() []gitignoreRule {
	gitignorePath := filepath.Join(p.projectRoot, ".gitignore")
	f, err := os.Open(gitignorePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var rules []gitignoreRule
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule := gitignoreRule{}

		// Negation
		if strings.HasPrefix(line, "!") {
			rule.negation = true
			line = line[1:]
		}

		// Directory-only pattern
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		rule.pattern = line
		rules = append(rules, rule)
	}

	return rules
}

// isGitignored checks if a path matches any gitignore rule.
func (p *ProjectSource) isGitignored(path string, isDir bool) bool {
	if len(p.gitignoreRules) == 0 {
		return false
	}

	rel, err := filepath.Rel(p.projectRoot, path)
	if err != nil {
		return false
	}

	// Normalize to forward slashes for matching
	rel = filepath.ToSlash(rel)

	ignored := false
	for _, rule := range p.gitignoreRules {
		if rule.dirOnly && !isDir {
			continue
		}

		if matchGitignore(rule.pattern, rel) {
			if rule.negation {
				ignored = false
			} else {
				ignored = true
			}
		}
	}

	return ignored
}

// matchGitignore performs simplified gitignore pattern matching.
// Supports: *, **, ?, and path-based matching.
func matchGitignore(pattern, path string) bool {
	// If pattern contains no slash, match against basename only
	if !strings.Contains(pattern, "/") {
		base := filepath.Base(path)
		return matchGlob(pattern, base)
	}

	// Pattern with slash — match against full relative path
	pattern = strings.TrimPrefix(pattern, "/")

	// Handle ** (match any number of directories)
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimSuffix(parts[0], "/")
		suffix := strings.TrimPrefix(parts[1], "/")

		if prefix == "" && suffix == "" {
			return true
		}

		if prefix != "" && !strings.HasPrefix(path, prefix) {
			return false
		}

		if suffix == "" {
			return true
		}

		// Check if any suffix of path matches the pattern suffix
		pathParts := strings.Split(path, "/")
		for i := range pathParts {
			subpath := strings.Join(pathParts[i:], "/")
			if matchGlob(suffix, subpath) {
				return true
			}
		}
		return false
	}

	return matchGlob(pattern, path)
}

// matchGlob performs simple glob matching with * and ? support.
func matchGlob(pattern, name string) bool {
	// Use filepath.Match for simple glob patterns
	matched, err := filepath.Match(pattern, name)
	if err == nil && matched {
		return true
	}

	// Also check if pattern matches any path component
	// e.g., pattern "build" should match "src/build" and "build/output"
	if !strings.Contains(pattern, "/") {
		parts := strings.Split(name, "/")
		for _, part := range parts {
			if m, _ := filepath.Match(pattern, part); m {
				return true
			}
		}
	}

	return false
}
