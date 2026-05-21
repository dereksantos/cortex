package projectscan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IgnoreSet bundles the filtering state for a single project root:
// gitignore rules, compiled sensitive-name and crypto-extension regexes,
// and the root used to compute relative paths.
//
// Construct via LoadIgnoreSet. The struct is read-only after that —
// callers may share one IgnoreSet across goroutines safely.
type IgnoreSet struct {
	Root           string
	GitignoreRules []GitignoreRule

	cryptoExtRE  *regexp.Regexp
	secretNameRE *regexp.Regexp
}

// LoadIgnoreSet builds an IgnoreSet for the given project root. The
// root is used to resolve relative paths for .gitignore matching and
// to compute "rel-path" string keys. A missing .gitignore is fine —
// callers may still rely on hard-exclusion and extension filtering.
func LoadIgnoreSet(root string) *IgnoreSet {
	return &IgnoreSet{
		Root:           root,
		GitignoreRules: loadGitignore(root),
		cryptoExtRE:    regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|kdbx|gpg|asc|pgp|enc|jks|keystore|p8)$`),
		secretNameRE:   regexp.MustCompile(`(?i)(secret|credential|token)`),
	}
}

// IsDirExcluded returns true if a directory should never be entered.
// Combines hard-excluded dir names with .gitignore.
func (s *IgnoreSet) IsDirExcluded(absPath, dirName string) bool {
	if s.IsHardExcludedDir(dirName) {
		return true
	}
	return s.IsGitignored(absPath, true)
}

// IsFileExcluded returns true if a file should never be sampled.
// Combines sensitive-name layer 1, extension layer 2, low-signal
// extension blacklist, and .gitignore. Does NOT include the magic-byte
// sniff — call IsSensitiveByMagicBytes separately when you want
// defense-in-depth.
func (s *IgnoreSet) IsFileExcluded(absPath string) bool {
	if s.IsHardExcludedFile(absPath) {
		return true
	}
	if s.ShouldSkipByExtension(absPath) {
		return true
	}
	return s.IsGitignored(absPath, false)
}

// IsHardExcludedDir returns true for directories that must never be
// entered regardless of .gitignore: .git, vendor caches, build output,
// editor metadata.
func (s *IgnoreSet) IsHardExcludedDir(name string) bool {
	if name == ".git" {
		return true
	}
	switch name {
	case "node_modules",
		"vendor",
		".venv", "venv",
		"__pycache__", ".mypy_cache", ".pytest_cache",
		"dist", "build", "target",
		".next", ".nuxt", ".output",
		".idea":
		return true
	}
	return false
}

// IsHardExcludedFile combines layer-1 (name-based) and layer-2
// (crypto-extension regex) sensitive filters with the original
// hard-exclude policy from the Dream sources. Cortex's own state
// (.cortex/) is excluded except .cortex/knowledge/, which is committed
// team context.
//
// Layer-3 (magic-byte sniff) is NOT applied here — it requires opening
// the file. Call IsSensitiveByMagicBytes separately for that layer.
func (s *IgnoreSet) IsHardExcludedFile(absPath string) bool {
	rel, _ := filepath.Rel(s.Root, absPath)
	rel = filepath.ToSlash(rel)
	base := filepath.Base(absPath)
	lower := strings.ToLower(base)
	lowerRel := strings.ToLower(rel)

	// === Layer 1a — .env files (allow .example / .sample / .template)
	if strings.HasPrefix(lower, ".env") {
		if lower == ".env.example" || lower == ".env.sample" || lower == ".env.template" {
			return false
		}
		return true
	}

	// === Layer 2 — crypto extension regex
	if s.cryptoExtRE.MatchString(lower) {
		return true
	}

	// === Layer 1b — explicit named secret files
	switch lower {
	case "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		"credentials.json", "service-account.json",
		"secrets.yaml", "secrets.yml", "secrets.json",
		".npmrc", ".pypirc", ".netrc",
		"htpasswd", ".htpasswd":
		return true
	}
	switch rel {
	case ".docker/config.json":
		return true
	}

	// === Layer 1c — sensitive prefixes (credential directories)
	sensitivePrefixes := []string{
		".aws/credentials",
		".aws/config",
		".kube/config",
		".gnupg/",
		".ssh/",
		".config/gcloud/",
	}
	for _, p := range sensitivePrefixes {
		if lowerRel == p || strings.HasPrefix(lowerRel, p) {
			// Allow ssh public keys (.pub) — they're not secrets.
			if strings.HasSuffix(lower, ".pub") {
				return false
			}
			return true
		}
	}

	// === Layer 1d — secret/credential/token substring (with allow-list
	// for template-style names)
	if s.secretNameRE.MatchString(lower) {
		if strings.HasSuffix(lower, ".example") ||
			strings.HasSuffix(lower, ".template") ||
			strings.HasSuffix(lower, ".sample") {
			return false
		}
		return true
	}

	// === Layer 1e — service-account / GCP key JSON
	if strings.HasSuffix(lower, ".json") {
		if strings.Contains(lower, "service-account") ||
			strings.Contains(lower, "gcp-key") ||
			strings.Contains(lower, "gcp_key") {
			return true
		}
	}

	// === Layer 1f — Cortex's own state (except knowledge/)
	if strings.HasPrefix(rel, ".cortex/") || strings.HasPrefix(rel, ".cortex\\") {
		if strings.HasPrefix(rel, ".cortex/knowledge/") || strings.HasPrefix(rel, ".cortex\\knowledge\\") {
			return false
		}
		return true
	}

	// === Layer 1g — OS junk
	switch lower {
	case ".ds_store", "thumbs.db", "desktop.ini":
		return true
	}

	return false
}

// ShouldSkipByExtension returns true for binary, generated, or
// low-signal files (images, archives, lock files, minified bundles,
// databases, source maps).
func (s *IgnoreSet) ShouldSkipByExtension(absPath string) bool {
	ext := strings.ToLower(filepath.Ext(absPath))

	switch ext {
	case ".exe", ".dll", ".so", ".dylib",
		".o", ".obj", ".a", ".lib",
		".class", ".pyc", ".pyo",
		".png", ".jpg", ".jpeg", ".gif",
		".ico", ".svg", ".webp", ".bmp",
		".mp3", ".mp4", ".wav", ".avi",
		".mov", ".webm", ".ttf", ".woff",
		".woff2", ".eot", ".otf",
		".zip", ".tar", ".gz", ".bz2",
		".rar", ".7z", ".xz",
		".pdf", ".doc", ".docx", ".xls",
		".xlsx", ".pptx",
		".lock", ".sum",
		".db", ".sqlite", ".sqlite3",
		".map":
		return true
	}

	base := strings.ToLower(filepath.Base(absPath))
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}

	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"go.sum", "gemfile.lock", "poetry.lock",
		"composer.lock", "cargo.lock", "flake.lock":
		return true
	}
	return false
}

// IsSensitiveByMagicBytes is the layer-3 defense: open the file, read
// the first 200 bytes, and report true if the content begins with
// (or contains in those first bytes) "-----BEGIN".
//
// Returns false on any read error — best effort, never blocks the
// scan. Callers wanting strict refusal should treat IO errors as
// excluded themselves.
//
// The sniff catches PGP private keys, RSA/EC/DSA private keys, X.509
// certificates, and encrypted PEM blobs whose extension or name was
// renamed innocuously.
func (s *IgnoreSet) IsSensitiveByMagicBytes(absPath string) bool {
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer f.Close()

	var buf [200]byte
	n, _ := f.Read(buf[:])
	if n == 0 {
		return false
	}
	// Case-insensitive contains: -----BEGIN is the universal preamble.
	// We compare against the uppercase form since the marker is
	// canonically upper-case ASCII.
	upper := strings.ToUpper(string(buf[:n]))
	return strings.Contains(upper, "-----BEGIN")
}
