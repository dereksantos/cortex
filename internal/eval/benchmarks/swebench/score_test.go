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
	inst := Instance{Repo: "django/django", Version: "4.2"}
	got := imageNameFor("swebench/sweb.eval.x86_64.", inst)
	want := "swebench/sweb.eval.x86_64.django__django:v4.2"
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
