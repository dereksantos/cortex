package shellrisk

import (
	"context"
	"testing"
)

// TestClassify_SafePath_StreamMerge pins the one shell-control form the safe
// path tolerates: a standalone fd-to-fd stream merge (`2>&1`, `>&2`, `1>&2`)
// on an otherwise safe-path command. Observed 2026-10-11: `go test ./... 2>&1`
// went to the model classifier and, when the classifier's response was
// unreadable, was gated for approval (blocked outright headless).
func TestClassify_SafePath_StreamMerge(t *testing.T) {
	mustNotCall := func(_ context.Context, cmd, _ string) (Level, string, error) {
		t.Fatalf("classifier called for safe-path command %q", cmd)
		return Risky, "", nil
	}
	accept := []string{
		"go test ./... 2>&1",
		"go vet ./... 2>&1",
		"go build ./...\t2>&1", // tab-separated word
		"git status 2>&1",
		"ls -la 2>&1",
		"echo done >&2",
		"echo done 1>&2",
		"2>&1 ls", // a redirect may precede the command word
		"grep -rn TODO . 2>&1",
	}
	for _, cmd := range accept {
		t.Run("accept/"+cmd, func(t *testing.T) {
			v := Classify(context.Background(), cmd, "", mustNotCall)
			if v.Level != Safe || v.Tier != "safe-path" {
				t.Errorf("Classify(%q) = %s/%s (%q), want safe/safe-path", cmd, v.Level, v.Tier, v.Reason)
			}
		})
	}

	// Everything else that carries shell control still goes to the
	// classifier — including merges glued to other syntax, file redirects,
	// and an unsafe command that merely carries a merge.
	reject := []string{
		"go test ./... 2>&1 | tail -20", // pipe
		"go test ./... 2>&1 && go vet ./...",
		"go test ./... && go vet ./...",
		"go test ./... 2>&1; rm -rf x",
		"go test ./... 2>&1;rm -rf x", // merge glued to a chain
		"go test ./... 2>&1file",      // not an exact merge word
		"go test ./...2>&1",           // glued to the previous word
		"go test ./... 2>&1\nrm x",    // newline chain
		"go test ./... 2>&1 &",        // backgrounded
		"go test ./... &",
		"go test ./... 2>&1&",
		"go test ./... 2> out.txt", // stderr to a file
		"go test ./... 2>out.txt",
		"go test ./... > out.txt",  // stdout to a file
		"go test ./... >> out.txt", // append to a file
		"go test ./... &> out.txt",
		"go test ./... >&out.txt", // >&word with a file name writes the file
		"go test ./... 2>&3",      // other fds are not on the list
		"go test ./... 3>&1",
		"go test ./... 2>&-", // closing a stream
		"go test ./... < in.txt",
		"go test $(cat pkgs) 2>&1", // substitution
		"go test `cat pkgs` 2>&1",
		"echo \"2>&1\"",     // quoted: not an exact merge word
		"rm stale.txt 2>&1", // unsafe binary, merge or not
		"git push 2>&1",
		"go run ./main.go 2>&1",
		"FOO=bar ls 2>&1",
	}
	sawClassifier := false
	spy := func(_ context.Context, _, _ string) (Level, string, error) {
		sawClassifier = true
		return Risky, "spy", nil
	}
	for _, cmd := range reject {
		t.Run("reject/"+cmd, func(t *testing.T) {
			sawClassifier = false
			v := Classify(context.Background(), cmd, "", spy)
			if v.Tier == "safe-path" {
				t.Errorf("Classify(%q) took safe-path, want classifier", cmd)
			}
			if !sawClassifier {
				t.Errorf("Classify(%q) did not consult the classifier (tier %s)", cmd, v.Tier)
			}
		})
	}
}

// TestStreamMerge_EffectClassAndPushUnaffected checks that the stream-merge
// tolerance is confined to the safe path: the same-action effect classes and
// the taint floor's git-push detection still see a command carrying a merge.
func TestStreamMerge_EffectClassAndPushUnaffected(t *testing.T) {
	cases := []struct {
		cmd       string
		wantClass string
	}{
		{"git commit -m wip 2>&1", EffectGitHistoryWrite},
		{"git commit --no-verify -m wip 2>&1", EffectHookDisabling},
		{"git stash pop 2>&1", EffectGitStash},
		{"go test ./... 2>&1", ""},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			if got := EffectClass(c.cmd); got != c.wantClass {
				t.Errorf("EffectClass(%q) = %q, want %q", c.cmd, got, c.wantClass)
			}
		})
	}
	if !IsGitPush("git push origin main 2>&1") {
		t.Error(`IsGitPush("git push origin main 2>&1") = false, want true (the taint floor must still see it)`)
	}
}
