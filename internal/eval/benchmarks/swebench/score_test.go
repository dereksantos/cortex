package swebench

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParsePytestOutput(t *testing.T) {
	out := `
============================= test session starts ==============================
collected 4 items

tests/test_x.py::test_one PASSED                                          [ 25%]
tests/test_x.py::test_two FAILED                                          [ 50%]
tests/test_y.py::test_three PASSED                                        [ 75%]
tests/test_y.py::test_four ERROR                                          [100%]

================== 2 passed, 1 failed, 1 error in 0.40s ========================
`
	passed, failed := parsePytestOutput(out)
	wantPass := []string{"tests/test_x.py::test_one", "tests/test_y.py::test_three"}
	wantFail := []string{"tests/test_x.py::test_two", "tests/test_y.py::test_four"}
	if !slicesEqual(passed, wantPass) {
		t.Errorf("passed: got %v, want %v", passed, wantPass)
	}
	if !slicesEqual(failed, wantFail) {
		t.Errorf("failed: got %v, want %v", failed, wantFail)
	}
}

func TestScoreFromOutput_AllPass(t *testing.T) {
	inst := Instance{
		FailToPass: []string{"tests/test_x.py::test_one"},
		PassToPass: []string{"tests/test_y.py::test_three"},
	}
	out := `tests/test_x.py::test_one PASSED
tests/test_y.py::test_three PASSED`
	res := scoreFromOutput(inst, out)
	if !res.AllPassed {
		t.Errorf("expected AllPassed=true, got %+v", res)
	}
	if res.F2PPassed != 1 || res.F2PFailed != 0 {
		t.Errorf("F2P counts: %+v", res)
	}
	if res.P2PPassed != 1 || res.P2PFailed != 0 {
		t.Errorf("P2P counts: %+v", res)
	}
}

func TestScoreFromOutput_F2PFails(t *testing.T) {
	inst := Instance{
		FailToPass: []string{"tests/test_x.py::test_one"},
		PassToPass: []string{"tests/test_y.py::test_three"},
	}
	out := `tests/test_x.py::test_one FAILED
tests/test_y.py::test_three PASSED`
	res := scoreFromOutput(inst, out)
	if res.AllPassed {
		t.Errorf("expected AllPassed=false")
	}
	if res.F2PFailed != 1 {
		t.Errorf("F2P fail expected: %+v", res)
	}
	if res.P2PPassed != 1 {
		t.Errorf("P2P should still pass: %+v", res)
	}
}

func TestScoreFromOutput_P2PRegresses(t *testing.T) {
	inst := Instance{
		FailToPass: []string{"tests/test_x.py::test_one"},
		PassToPass: []string{"tests/test_y.py::test_three"},
	}
	out := `tests/test_x.py::test_one PASSED
tests/test_y.py::test_three FAILED`
	res := scoreFromOutput(inst, out)
	if res.AllPassed {
		t.Errorf("regression in PASS_TO_PASS must fail the run")
	}
	if res.P2PFailed != 1 {
		t.Errorf("P2P fail expected: %+v", res)
	}
}

func TestScoreFromOutput_MissingTestCountsAsFail(t *testing.T) {
	// A test the agent didn't even run (no PASSED or FAILED line) is
	// treated as failed — silent missing tests are not a win.
	inst := Instance{
		FailToPass: []string{"tests/test_x.py::test_one"},
		PassToPass: []string{"tests/test_y.py::test_three"},
	}
	out := `tests/test_x.py::test_one PASSED`
	res := scoreFromOutput(inst, out)
	if res.AllPassed {
		t.Errorf("missing P2P should fail")
	}
	if res.P2PFailed != 1 {
		t.Errorf("missing P2P should count as failed: %+v", res)
	}
}

func TestImageNameFor(t *testing.T) {
	// Verified images live on Docker Hub at
	// swebench/sweb.eval.x86_64.<org>_1776_<repo>-<issue>:latest.
	// The InstanceID's `__` separator maps to `_1776_`.
	inst := Instance{InstanceID: "django__django-10097", Repo: "django/django", Version: "4.2"}
	got := imageNameFor("swebench/sweb.eval.x86_64.", inst)
	want := "swebench/sweb.eval.x86_64.django_1776_django-10097:latest"
	if got != want {
		t.Errorf("imageNameFor: got %q want %q", got, want)
	}
}

func TestRunSWEBenchTests_DockerMissing(t *testing.T) {
	// Force the docker-locator to return missing.
	prev := dockerLookPath
	dockerLookPath = func(string) (string, error) { return "", errors.New("missing") }
	defer func() { dockerLookPath = prev }()

	_, err := RunSWEBenchTests(context.Background(), Instance{InstanceID: "x", Repo: "foo/bar", Version: "1.0"}, "", "swebench/sweb.eval.x86_64.", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("want docker missing error, got %v", err)
	}
}

func TestPreflight_DockerMissing(t *testing.T) {
	prev := dockerLookPath
	dockerLookPath = func(string) (string, error) { return "", errors.New("missing") }
	defer func() { dockerLookPath = prev }()

	err := preflight(context.Background())
	if err == nil {
		t.Fatal("want preflight error when docker is missing, got nil")
	}
	if !strings.Contains(err.Error(), "docker not on PATH") {
		t.Errorf("want 'docker not on PATH' in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "docs.docker.com") {
		t.Errorf("want install link in error message, got %v", err)
	}
}

func TestPreflight_DaemonDown(t *testing.T) {
	// Docker IS on PATH, but `docker info` fails. Stub LookPath too so
	// the test passes on hosts without Docker (macOS CI), and stub
	// info to return the actual error macOS prints when the daemon
	// isn't running so the assertion proves we propagate it usefully.
	prevLook := dockerLookPath
	prevInfo := preflightDockerInfo
	dockerLookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	preflightDockerInfo = func(context.Context) ([]byte, error) {
		out := []byte("Client:\n Version: 28.1.1\nerror during connect: Cannot connect to the Docker daemon at unix:///run/docker.sock. Is the docker daemon running?")
		return out, errors.New("exit status 1")
	}
	defer func() {
		dockerLookPath = prevLook
		preflightDockerInfo = prevInfo
	}()

	err := preflight(context.Background())
	if err == nil {
		t.Fatal("want preflight error when daemon is down, got nil")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("want daemon-down hint in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "open -a Docker") {
		t.Errorf("want macOS start hint in error, got %v", err)
	}
}

func TestPreflight_DaemonUp(t *testing.T) {
	prevLook := dockerLookPath
	prevInfo := preflightDockerInfo
	dockerLookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	preflightDockerInfo = func(context.Context) ([]byte, error) {
		return []byte("Client:\n Version: 28.1.1\nServer:\n Server Version: 28.1.1\n"), nil
	}
	defer func() {
		dockerLookPath = prevLook
		preflightDockerInfo = prevInfo
	}()

	if err := preflight(context.Background()); err != nil {
		t.Fatalf("want nil when docker is up, got %v", err)
	}
}

func TestPreflightImage_AlreadyLocal(t *testing.T) {
	prevInspect := preflightImageInspect
	prevPull := preflightImagePull
	pullCalled := false
	preflightImageInspect = func(context.Context, string) ([]byte, error) {
		return []byte(`[{"Id":"sha256:abc"}]`), nil
	}
	preflightImagePull = func(context.Context, string) ([]byte, error) {
		pullCalled = true
		return nil, errors.New("should not have been called")
	}
	defer func() { preflightImageInspect = prevInspect; preflightImagePull = prevPull }()

	inst := Instance{InstanceID: "django__django-10097", Repo: "django/django", Version: "2.2"}
	if err := preflightImage(context.Background(), inst, "swebench/sweb.eval.x86_64."); err != nil {
		t.Fatalf("want nil when image is local, got %v", err)
	}
	if pullCalled {
		t.Error("pull should be skipped when inspect succeeds")
	}
}

func TestPreflightImage_PullFails(t *testing.T) {
	prevInspect := preflightImageInspect
	prevPull := preflightImagePull
	preflightImageInspect = func(context.Context, string) ([]byte, error) {
		return []byte("Error: No such object"), errors.New("exit status 1")
	}
	preflightImagePull = func(context.Context, string) ([]byte, error) {
		return []byte("Error response from daemon: pull access denied for swebench/sweb.eval.x86_64.django_1776_django-10097"), errors.New("exit status 1")
	}
	defer func() { preflightImageInspect = prevInspect; preflightImagePull = prevPull }()

	inst := Instance{InstanceID: "django__django-10097", Repo: "django/django", Version: "2.2"}
	err := preflightImage(context.Background(), inst, "swebench/sweb.eval.x86_64.")
	if err == nil {
		t.Fatal("want error when pull fails, got nil")
	}
	if !strings.Contains(err.Error(), "django_1776_django-10097") {
		t.Errorf("want resolved image id in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("want registry error in 'raw' tail, got %v", err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stubPreflightForTest swaps every preflight indirection so the Load
// path treats Docker + the per-instance image as available. Restores
// originals via t.Cleanup. Use in tests whose subject isn't the
// preflight gate itself.
//
// Includes dockerLookPath so the test works on hosts without Docker
// installed (e.g. macOS CI runners), where preflight would otherwise
// fail at the LookPath step before reaching the stubbed info path.
func stubPreflightForTest(t *testing.T) {
	t.Helper()
	prevLook := dockerLookPath
	prevInfo := preflightDockerInfo
	prevInspect := preflightImageInspect
	prevPull := preflightImagePull
	dockerLookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	preflightDockerInfo = func(context.Context) ([]byte, error) {
		return []byte("Client:\n Server: ok\n"), nil
	}
	preflightImageInspect = func(context.Context, string) ([]byte, error) {
		return []byte(`[{"Id":"sha256:stub"}]`), nil
	}
	preflightImagePull = func(context.Context, string) ([]byte, error) {
		return []byte("Using cached"), nil
	}
	t.Cleanup(func() {
		dockerLookPath = prevLook
		preflightDockerInfo = prevInfo
		preflightImageInspect = prevInspect
		preflightImagePull = prevPull
	})
}
