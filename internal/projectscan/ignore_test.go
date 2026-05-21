package projectscan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSet returns an IgnoreSet rooted at a fresh temp dir. Caller is
// responsible for any teardown beyond what t.TempDir already does.
func newSet(t *testing.T) (*IgnoreSet, string) {
	t.Helper()
	root := t.TempDir()
	return LoadIgnoreSet(root), root
}

// touch writes a file at root/rel with the given content. Creates
// parent dirs as needed.
func touch(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

func TestHardExcludedDir(t *testing.T) {
	s, _ := newSet(t)
	cases := []struct {
		name string
		want bool
	}{
		{".git", true},
		{"node_modules", true},
		{"vendor", true},
		{"venv", true},
		{".venv", true},
		{"__pycache__", true},
		{".mypy_cache", true},
		{".pytest_cache", true},
		{"dist", true},
		{"build", true},
		{"target", true},
		{".next", true},
		{".idea", true},
		{"src", false},
		{"internal", false},
		{"docs", false},
		{"tests", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.IsHardExcludedDir(tc.name); got != tc.want {
				t.Errorf("IsHardExcludedDir(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestSensitiveLayer1_DotEnv(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool // want excluded
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{".env.production.local", true},
		{".env.staging.local", true},
		{".env.example", false},
		{".env.sample", false},
		{".env.template", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x=1")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

func TestSensitiveLayer1_NamedFiles(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool
	}{
		{"id_rsa", true},
		{"id_ed25519", true},
		{"credentials.json", true},
		{"service-account.json", true},
		{"secrets.yaml", true},
		{".npmrc", true},
		{".netrc", true},
		{"README.md", false},
		{"main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// TestSensitiveLayer1_CredentialPrefixes verifies that files under
// known-sensitive directory prefixes are rejected. Per CLAUDE.md:
// "Explicit prefixes: .aws/credentials, .kube/config, .gnupg/, .ssh/".
func TestSensitiveLayer1_CredentialPrefixes(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool
	}{
		{".aws/credentials", true},
		{".aws/config", true},
		{".kube/config", true},
		{".gnupg/private-keys-v1.d/abc.key", true},
		{".ssh/id_rsa", true},
		{".ssh/known_hosts", true},
		{".ssh/id_rsa.pub", false}, // public keys allowed
		{".config/gcloud/credentials.json", true},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// TestSensitiveLayer1_NameSubstring verifies the case-insensitive
// secret/credential/token substring rule with the template-style
// exemption.
func TestSensitiveLayer1_NameSubstring(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool
	}{
		{"aws-credentials.txt", true},
		{"my-secret-key.yaml", true},
		{"oauth-token.json", true},
		{"PROD_SECRETS.md", true},
		{"secret.example", false},
		{"credentials.template", false},
		{"token-format.sample", false},
		{"okay.txt", false},
		{"main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// TestSensitiveLayer2_ExtensionRegex verifies the crypto-extension
// regex blacklist catches private keys / certificates / encrypted
// blobs regardless of basename.
func TestSensitiveLayer2_ExtensionRegex(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool
	}{
		{"mykey.pem", true},
		{"server.key", true},
		{"cert.p12", true},
		{"id.pfx", true},
		{"passwords.kdbx", true},
		{"backup.gpg", true},
		{"signed.asc", true},
		{"keys.pgp", true},
		{"blob.enc", true},
		{"store.jks", true},
		{"java.keystore", true},
		{"file.p8", true},
		{"NoTeS.PEM", true}, // case-insensitive
		{"README.md", false},
		{"main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// TestSensitiveLayer3_MagicBytes verifies the magic-byte sniff catches
// PEM-style cryptographic content regardless of name/extension.
func TestSensitiveLayer3_MagicBytes(t *testing.T) {
	s, root := newSet(t)

	rsaBody := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBA...rest of key\n-----END RSA PRIVATE KEY-----\n"
	pgpBody := "-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: GnuPG\n\n...\n-----END PGP PRIVATE KEY BLOCK-----\n"
	certBody := "-----BEGIN CERTIFICATE-----\nMIIDXTCCAk...\n-----END CERTIFICATE-----\n"
	innocuous := "this is just a notes file with no key material"

	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"notes_with_rsa_key.txt", rsaBody, true},
		{"backup.txt", pgpBody, true},
		{"cert_in_disguise.md", certBody, true},
		{"actual-notes.txt", innocuous, false},
		{"empty.txt", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := touch(t, root, tc.name, tc.content)
			if got := s.IsSensitiveByMagicBytes(p); got != tc.want {
				t.Errorf("IsSensitiveByMagicBytes(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCortexStateExclusion(t *testing.T) {
	s, root := newSet(t)
	cases := []struct {
		rel  string
		want bool
	}{
		{".cortex/queue/event.json", true},
		{".cortex/journal/dream/0001.jsonl", true},
		{".cortex/db/storage.db", true},
		{".cortex/knowledge/team.md", false}, // committed knowledge allowed
		{".cortex/knowledge/sub/note.md", false},
		{"src/main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := touch(t, root, tc.rel, "x")
			if got := s.IsHardExcludedFile(p); got != tc.want {
				t.Errorf("IsHardExcludedFile(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

func TestShouldSkipByExtension(t *testing.T) {
	s, _ := newSet(t)
	cases := []struct {
		path string
		want bool
	}{
		{"foo.exe", true},
		{"bar.dll", true},
		{"img.png", true},
		{"music.mp3", true},
		{"archive.zip", true},
		{"data.tar.gz", true}, // .gz triggers
		{"package-lock.json", true},
		{"yarn.lock", true},
		{"app.min.js", true},
		{"styles.min.css", true},
		{"store.db", true},
		{"index.html", false},
		{"main.go", false},
		{"README.md", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := s.ShouldSkipByExtension(tc.path); got != tc.want {
				t.Errorf("ShouldSkipByExtension(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestGitignore_BasicPatterns(t *testing.T) {
	root := t.TempDir()
	gitignore := strings.Join([]string{
		"# comment",
		"",
		"*.log",
		"tmp/",
		"build",
		"!build/keep.txt",
		"docs/**/draft.md",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	s := LoadIgnoreSet(root)

	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"sub/deep.log", false, true},
		{"tmp", true, true},
		{"src/main.go", false, false},
		{"build", true, true},
		{"build", false, true},
		{"build/keep.txt", false, false},     // negation
		{"docs/v1/draft.md", false, true},    // ** wildcard
		{"docs/v1/v2/draft.md", false, true}, // ** wildcard depth
		{"docs/v1/published.md", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := filepath.Join(root, tc.rel)
			if got := s.IsGitignored(p, tc.isDir); got != tc.want {
				t.Errorf("IsGitignored(%q,isDir=%v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
			}
		})
	}
}

func TestIsDirExcluded_Combined(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("tmp/\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := LoadIgnoreSet(root)

	cases := []struct {
		rel     string
		dirName string
		want    bool
	}{
		{"node_modules", "node_modules", true}, // hard
		{".git", ".git", true},                 // hard
		{"tmp", "tmp", true},                   // gitignore
		{"src", "src", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := filepath.Join(root, tc.rel)
			if got := s.IsDirExcluded(p, tc.dirName); got != tc.want {
				t.Errorf("IsDirExcluded(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

func TestIsFileExcluded_Combined(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := LoadIgnoreSet(root)

	cases := []struct {
		rel  string
		want bool
	}{
		{".env", true},        // layer 1
		{"key.pem", true},     // layer 2
		{"image.png", true},   // extension
		{"ignored.txt", true}, // gitignore
		{"README.md", false},
		{"src/main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.rel, func(t *testing.T) {
			p := filepath.Join(root, tc.rel)
			// Create parent dirs for the file path so realistic.
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("x"), 0o644)
			if got := s.IsFileExcluded(p); got != tc.want {
				t.Errorf("IsFileExcluded(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}
