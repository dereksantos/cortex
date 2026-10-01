package tools

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestScrubEnv(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		deny map[string]bool
		want []string
	}{
		{"drops named key", []string{"A=1", "SECRET=x", "B=2"}, map[string]bool{"SECRET": true}, []string{"A=1", "B=2"}},
		{"value with equals kept intact", []string{"A=x=y", "SECRET=a=b"}, map[string]bool{"SECRET": true}, []string{"A=x=y"}},
		{"prefix match is not a match", []string{"SECRET_2=x", "SECRET=y"}, map[string]bool{"SECRET": true}, []string{"SECRET_2=x"}},
		{"empty deny keeps all", []string{"A=1"}, map[string]bool{}, []string{"A=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrubEnv(tt.env, tt.deny); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scrubEnv=%v want %v", got, tt.want)
			}
		})
	}
}

// The configured secret must not be visible to the model's shell, while the
// rest of the environment still is.
func TestBashDoesNotSeeSecretEnv(t *testing.T) {
	const secret = "CORTEX_TEST_SHELL_SECRET"
	const visible = "CORTEX_TEST_SHELL_VISIBLE"
	t.Setenv(secret, "s3cr3t-value")
	t.Setenv(visible, "plain-value")
	defer SetShellSecretEnv(nil)

	tests := []struct {
		name       string
		deny       []string
		wantSecret bool
	}{
		{"unconfigured inherits everything", nil, true},
		{"configured secret is stripped", []string{secret, " "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetShellSecretEnv(tt.deny)
			env := shellEnv()
			if env == nil {
				env = os.Environ()
			}
			joined := strings.Join(env, "\n")
			if got := strings.Contains(joined, "s3cr3t-value"); got != tt.wantSecret {
				t.Errorf("secret visible=%v want %v", got, tt.wantSecret)
			}
			if !strings.Contains(joined, "plain-value") {
				t.Error("non-secret env var must stay visible to the shell")
			}
		})
	}
}
