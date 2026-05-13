package sources

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectSource_HardExcludedFiles asserts that the security-sensitive
// denylist in ProjectSource.isHardExcludedFile blocks every file shape
// that commonly contains credentials. Dream sources this method on every
// sample candidate — a regression here would leak secrets into the
// LLM-bound retrieval pipeline.
func TestProjectSource_HardExcludedFiles(t *testing.T) {
	tmp, err := os.MkdirTemp("", "cortex-project-source-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(tmp)

	ps := NewProjectSource(tmp)

	cases := []struct {
		name    string
		relPath string
		want    bool
	}{
		// === existing denylist (regression coverage) ===
		{"env file", ".env", true},
		{"env local", ".env.local", true},
		{"env production", ".env.production", true},
		{"env example allowed", ".env.example", false},
		{"env sample allowed", ".env.sample", false},
		{"env template allowed", ".env.template", false},
		{"pem cert", "certs/server.pem", true},
		{"private key", "keys/server.key", true},
		{"pkcs12 keystore", "secrets/store.p12", true},
		{"ssh id_rsa", "id_rsa", true},
		{"ssh ed25519", "id_ed25519", true},
		{"credentials.json", "credentials.json", true},
		{"service-account.json", "service-account.json", true},
		{"npm token file", ".npmrc", true},
		{"netrc", ".netrc", true},
		{"htpasswd", ".htpasswd", true},

		// === new denylist entries (these must be excluded; see below) ===
		{"aws credentials", ".aws/credentials", true},
		{"aws config", ".aws/config", true},
		{"kube config", ".kube/config", true},
		{"git credentials", ".git-credentials", true},
		{"pgpass", ".pgpass", true},
		{"ssh known_hosts", ".ssh/known_hosts", true},
		{"gcp adc", ".config/gcloud/application_default_credentials.json", true},

		// === regular files that must NOT be excluded ===
		{"go source", "main.go", false},
		{"readme", "README.md", false},
		{"package.json", "package.json", false},
		{"dockerfile", "Dockerfile", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			abs := filepath.Join(tmp, tc.relPath)
			got := ps.isHardExcludedFile(abs)
			if got != tc.want {
				t.Errorf("isHardExcludedFile(%q) = %v, want %v", tc.relPath, got, tc.want)
			}
		})
	}
}
