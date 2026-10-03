package redact

import (
	"strings"
	"testing"
)

// TestRedact covers every secret class from the issue, one table row per
// case. want is the exact redacted output (asserted on the whole string so a
// regression that masks too little OR too much both fail); wantCount is the
// number of matches Redact reports.
func TestRedact(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantCount int
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name:      "openai sk- key",
			in:        "export OPENAI_API_KEY=sk-abcdefghijklmnop1234567890abcdef",
			want:      "export OPENAI_API_KEY=" + marker(KindProviderKey),
			wantCount: 1, // the sk- key is consumed by the provider-key pattern before the assignment class can see it
		},
		{
			name:      "openrouter sk-or- key",
			in:        "key=sk-or-v1-0123456789abcdef0123456789abcdef",
			want:      "key=" + marker(KindProviderKey),
			wantCount: 1,
		},
		{
			name:      "github PAT ghp_",
			in:        "token: ghp_1234567890abcdefghijklmnop",
			want:      "token: " + marker(KindProviderKey),
			wantCount: 1, // the ghp_ key is consumed by the provider-key pattern before the assignment class can see it
		},
		{
			name:      "github fine-grained pat",
			in:        "token: github_pat_11ABCDEFG1234567890abc",
			want:      "token: " + marker(KindProviderKey),
			wantCount: 1,
		},
		{
			name:      "AWS access key id",
			in:        "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
			want:      "aws_access_key_id = " + marker(KindAWSKey),
			wantCount: 1,
		}, {
			name:      "AWS secret access key (named assignment)",
			in:        "AWS_SECRET_ACCESS_KEY=JrVvM0l8fC5n7KxT4wU9dR2pQ3sE5fG6hA1bC2dE",
			want:      "AWS_SECRET_ACCESS_KEY=" + marker(KindAWSKey),
			wantCount: 1,
		},
		{
			name:      "quoted assignment, value masked (quotes dropped)",
			in:        "export MY_TOKEN=\"topsecret123\"",
			want:      "export MY_TOKEN=" + marker(KindAssignment),
			wantCount: 1,
		},
		{
			name: "bare assignment, letter-led value masked (issue #103's .env case)",
			in:   "GITHUB_TOKEN=abcdefgh12345678",
			want: "GITHUB_TOKEN=" + marker(KindAssignment),
			// the 16-char bare value starts with a letter; the assignment
			// class must claim it (a prefixed key like sk-… would already be
			// a marker by the time this class runs, so letters are safe to
			// admit here)
			wantCount: 1,
		},
		{
			name:      "bare assignment, letter-led hyphenated value masked",
			in:        "SECRET_KEY=django-insecure-xyz",
			want:      "SECRET_KEY=" + marker(KindAssignment),
			wantCount: 1,
		},
		{
			name:      "PEM private key block",
			in:        "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7\n-----END RSA PRIVATE KEY-----",
			want:      marker(KindPEM),
			wantCount: 1,
		},
		{
			name:      "PEM ec private key block",
			in:        "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEEIBc\n-----END EC PRIVATE KEY-----",
			want:      marker(KindPEM),
			wantCount: 1,
		},
		{
			name:      "PEM pkcs8 private key block",
			in:        "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----",
			want:      marker(KindPEM),
			wantCount: 1,
		},
		{
			name: "two secrets in one line",
			in:   "ghp_1234567890abcdefghijklmnop and sk-abcdefghijklmnopqrstuvwxyz0123456789",
			want: marker(KindProviderKey) + " and " + marker(KindProviderKey),
			// the two provider keys are consumed first; the surrounding
			// "and" is not an assignment (no =), so no extra match
			wantCount: 2,
		},
		{
			name:      "secret-free text untouched",
			in:        "the quick brown fox jumps over the lazy dog",
			want:      "the quick brown fox jumps over the lazy dog",
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Redact(tc.in)
			if got != tc.want {
				t.Errorf("Redact() = %q\nwant       %q", got, tc.want)
			}
			if n != tc.wantCount {
				t.Errorf("Redact() count = %d, want %d", n, tc.wantCount)
			}
		})
	}
}

// TestRedact_AWSKey is the false-positive guard for the bare 40-char base64
// class: it must ONLY match where an assignment names the value as a secret.
func TestRedact_AWSKey(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantCount int
	}{
		{
			name: "bare 40-char run is NOT a key",
			in:   "digest: a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			want: "digest: a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		},
		{
			name: "non-secret-named 40-char value is NOT masked",
			in:   "session_id=a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			want: "session_id=a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		},
		{
			name:      "named secret value IS masked, name kept",
			in:        "AWS_SECRET_ACCESS_KEY=JrVvM0l8fC5n7KxT4wU9dR2pQ3sE5fG6hA1bC2dE",
			want:      "AWS_SECRET_ACCESS_KEY=" + marker(KindAWSKey),
			wantCount: 1,
		},
		{
			name:      "secret-named quoted value masked (quotes dropped)",
			in:        `aws_secret = 'JrVvM0l8fC5n7KxT4wU9dR2pQ3sE5fG6hA1bC2dE'`,
			want:      "aws_secret = " + marker(KindAWSKey),
			wantCount: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Redact(tc.in)
			if got != tc.want {
				t.Errorf("Redact() = %q\nwant       %q", got, tc.want)
			}
			if n != tc.wantCount {
				t.Errorf("Redact() count = %d, want %d", n, tc.wantCount)
			}
		})
	}
}

// TestRedact_Assignment_FalsePositives guards the KEY=/TOKEN=/SECRET= class
// against over-masking: ordinary non-secret assignments must pass through
// untouched, and the marker must never be produced where no secret was.
func TestRedact_Assignment_FalsePositives(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantCount int
	}{
		{
			name: "PATH untouched",
			in:   "PATH=/usr/local/bin:/usr/bin:/bin",
			want: "PATH=/usr/local/bin:/usr/bin:/bin",
		},
		{
			name: "HOST untouched",
			in:   "HOST=localhost",
			want: "HOST=localhost",
		},
		{
			name: "NAME untouched",
			in:   "NAME=example",
			want: "NAME=example",
		},
		{
			name: "USER untouched",
			in:   "USER=admin",
			want: "USER=admin",
		},
		{
			name: "short bare value under the length floor untouched",
			in:   "API_MODE=fast",
			want: "API_MODE=fast", // "api" names the class but the 4-char value is under the bare-value length floor
		},
		{
			name: "word containing 'key' in prose, no assignment",
			in:   "the monkey jumped over the fence",
			want: "the monkey jumped over the fence",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Redact(tc.in)
			if got != tc.want {
				t.Errorf("Redact() = %q\nwant       %q", got, tc.want)
			}
			if n != tc.wantCount {
				t.Errorf("Redact() count = %d, want %d", n, tc.wantCount)
			}
			if strings.Contains(got, RedactMarker) {
				t.Errorf("Redact() produced a marker for non-secret input %q", tc.in)
			}
		})
	}
}

// TestRedact_Idempotent verifies masking is stable: re-running Redact on an
// already-redacted string changes nothing and reports 0 — so the persistence
// seam can call it repeatedly without compounding the count.
func TestRedact_Idempotent(t *testing.T) {
	in := "KEY=sk-abcdefghijklmnopqrstuvwxyz0123456789\nexport GITHUB_TOKEN=ghp_1234567890abcdefghijklmnop"
	once, n1 := Redact(in)
	if n1 == 0 {
		t.Fatalf("first pass masked 0, want > 0")
	}
	twice, n2 := Redact(once)
	if twice != once {
		t.Errorf("second pass changed output:\nfirst  %q\nsecond %q", once, twice)
	}
	if n2 != 0 {
		t.Errorf("second pass reported %d redactions, want 0 (idempotent)", n2)
	}
}

// TestRedact_MultiLine ensures a PEM block spanning lines and an assignment
// on its own line are each handled, and that a redacted line still reads.
func TestRedact_MultiLine(t *testing.T) {
	in := "before\nexport DB_PASSWORD=hunter2secret\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\nafter"
	got, n := Redact(in)
	// The PEM block is masked as a whole; DB_PASSWORD's 13-char bare value
	// starts with a letter and IS masked by the assignment class — a bare
	// letter-led value is exactly the .env secret issue #103 describes.
	want := "before\nexport DB_PASSWORD=" + marker(KindAssignment) + "\n" + marker(KindPEM) + "\nafter"
	if got != want {
		t.Errorf("Redact() = %q\nwant       %q", got, want)
	}
	if n != 2 {
		t.Errorf("Redact() count = %d, want 2", n)
	}
}
