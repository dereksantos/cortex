package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// fakeFleet is a control plane whose slot advances one step per status poll
// after a load: the scripted states play in order, the last one repeats.
type fakeFleet struct {
	mu       sync.Mutex
	initial  fleetSlot   // what status says before any load
	script   []fleetSlot // what status says after the load, poll by poll
	loadRes  fleetSlot
	loadHTTP int
	notFound bool
	loads    []string
	polls    int
}

func (f *fakeFleet) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.notFound {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not swappable"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fleet/status":
			s := f.initial
			if len(f.loads) > 0 {
				i := f.polls
				if i >= len(f.script) {
					i = len(f.script) - 1
				}
				s = f.script[i]
				f.polls++
			}
			_ = json.NewEncoder(w).Encode(s)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fleet/load":
			var body struct{ Model string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("load body: %v", err)
			}
			f.loads = append(f.loads, body.Model)
			code := f.loadHTTP
			if code == 0 {
				code = http.StatusAccepted
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(f.loadRes)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
}

func runFakeSwap(t *testing.T, f *fakeFleet, live bool, model string) (string, error) {
	t.Helper()
	defer style.ForceColor(false)()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	fs := newFleetSwap(fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL + "/"}}), &out, live)
	fs.tick, fs.poll, fs.timeout = time.Millisecond, time.Millisecond, 5*time.Second
	err := fs.run(context.Background(), model)
	return out.String(), err
}

var glmLoaded = fleetSlot{Model: "north", State: "ready", Loaded: "glm-4.7-flash", Silicon: "RTX 3090"}

func TestFleetSwapLoadsAndWaitsUntilReady(t *testing.T) {
	f := &fakeFleet{
		initial: glmLoaded,
		loadRes: fleetSlot{Model: "north", State: "ready", Loaded: "glm-4.7-flash", Silicon: "RTX 3090", From: "glm-4.7-flash"},
		script: []fleetSlot{
			{Model: "north", State: "stopping", Stopping: "glm-4.7-flash", Silicon: "RTX 3090"},
			{Model: "north", State: "loading", Loading: "north", Silicon: "RTX 3090"},
			{Model: "north", State: "loading", Loading: "north", Silicon: "RTX 3090"},
			{Model: "north", State: "ready", Loaded: "north", IsLoaded: true, Silicon: "RTX 3090"},
		},
	}
	out, err := runFakeSwap(t, f, false, "north")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if len(f.loads) != 1 || f.loads[0] != "north" {
		t.Fatalf("loads = %v, want [north]", f.loads)
	}
	// Plain mode prints each phase once, in order, then the ready line.
	want := []string{"north: unloading glm-4.7-flash", "north: loading", "north ready on RTX 3090"}
	at := 0
	for _, w := range want {
		i := strings.Index(out[at:], w)
		if i < 0 {
			t.Fatalf("output missing %q after offset %d:\n%s", w, at, out)
		}
		at += i + len(w)
	}
	if strings.Count(out, "north: loading") != 1 {
		t.Errorf("a phase repeated across polls should print once:\n%s", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("plain mode must not repaint:\n%q", out)
	}
}

func TestFleetSwapLiveRepaintsOneLine(t *testing.T) {
	f := &fakeFleet{
		initial: glmLoaded,
		loadRes: fleetSlot{Model: "north", State: "loading", Loading: "north", Silicon: "RTX 3090", From: "glm-4.7-flash"},
		script: []fleetSlot{
			{Model: "north", State: "loading", Loading: "north", Silicon: "RTX 3090"},
			{Model: "north", State: "ready", Loaded: "north", IsLoaded: true, Silicon: "RTX 3090"},
		},
	}
	out, err := runFakeSwap(t, f, true, "north")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "\rnorth · loading") {
		t.Errorf("live mode should repaint the loading line in place:\n%q", out)
	}
	last := out[strings.LastIndex(out, "\r")+1:]
	if !strings.HasPrefix(last, "north ready on RTX 3090") || !strings.HasSuffix(last, "\033[K\n") {
		t.Errorf("the final repaint should be the ready line, cleared and ended: %q", last)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("live mode should leave exactly one line behind:\n%q", out)
	}
}

func TestFleetSwapNoOpCases(t *testing.T) {
	cases := []struct {
		name      string
		fleet     *fakeFleet
		wantOut   string
		wantLoads int
	}{
		{"already loaded", &fakeFleet{initial: fleetSlot{Model: "north", State: "ready", Loaded: "north", IsLoaded: true, Silicon: "RTX 3090"}},
			"north is loaded on RTX 3090", 0},
		{"not a swappable model", &fakeFleet{notFound: true}, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runFakeSwap(t, c.fleet, false, "north")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if strings.TrimSpace(out) != c.wantOut {
				t.Errorf("output = %q, want %q", out, c.wantOut)
			}
			if len(c.fleet.loads) != c.wantLoads {
				t.Errorf("loads = %v, want %d", c.fleet.loads, c.wantLoads)
			}
		})
	}
}

func TestFleetSwapUnreachableIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens there now
	var out bytes.Buffer
	fs := newFleetSwap(fleetControlFor(&Config{Backend: Backend{FleetControl: base}}), &out, true)
	if err := fs.run(context.Background(), "north"); err != nil {
		t.Fatalf("an unreachable control plane must not fail /model: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("an unreachable control plane must print nothing, got %q", out.String())
	}
}

func TestFleetSwapReportsFailureAndRefusal(t *testing.T) {
	t.Run("unit failed", func(t *testing.T) {
		f := &fakeFleet{initial: glmLoaded, loadRes: fleetSlot{Model: "north", State: "loading", Silicon: "RTX 3090"},
			script: []fleetSlot{{Model: "north", State: "failed", Silicon: "RTX 3090"}}}
		out, err := runFakeSwap(t, f, false, "north")
		if err == nil || !strings.Contains(out, "north failed to load on RTX 3090") {
			t.Errorf("err = %v, out = %q; want a failure line and an error", err, out)
		}
	})
	t.Run("load refused", func(t *testing.T) {
		f := &fakeFleet{initial: glmLoaded, loadHTTP: http.StatusInternalServerError,
			loadRes: fleetSlot{Error: "systemctl start north-cuda failed: boom"}}
		out, err := runFakeSwap(t, f, false, "north")
		if err == nil || !strings.Contains(out, "could not load north: systemctl start north-cuda failed: boom") {
			t.Errorf("err = %v, out = %q; want the control plane's message", err, out)
		}
	})
}

func TestFleetSwapTimesOut(t *testing.T) {
	f := &fakeFleet{initial: glmLoaded, loadRes: fleetSlot{Model: "north", State: "loading", Silicon: "RTX 3090"},
		script: []fleetSlot{{Model: "north", State: "loading", Loading: "north", Silicon: "RTX 3090"}}}
	defer style.ForceColor(false)()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	fs := newFleetSwap(fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL}}), &out, false)
	fs.tick, fs.poll, fs.timeout = time.Millisecond, time.Millisecond, 20*time.Millisecond
	if err := fs.run(context.Background(), "north"); err == nil || !strings.Contains(out.String(), "still not ready after") {
		t.Errorf("err = %v, out = %q; want a timeout", err, out.String())
	}
}

func TestPhaseOf(t *testing.T) {
	cases := []struct {
		name string
		s    fleetSlot
		from string
		want string
	}{
		{"old model still answering", fleetSlot{State: "ready", Loaded: "glm"}, "glm", "unloading glm"},
		{"systemd stopping the old unit", fleetSlot{State: "loading", Stopping: "glm"}, "glm", "unloading glm"},
		{"slot stopping, name unknown", fleetSlot{State: "stopping"}, "", "unloading"},
		{"new model loading", fleetSlot{State: "loading", Loading: "north"}, "glm", "loading"},
		{"empty slot", fleetSlot{State: "empty"}, "", "loading"},
		{"ready", fleetSlot{State: "ready", Loaded: "north"}, "glm", "ready"},
		{"failed", fleetSlot{State: "failed"}, "glm", "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := phaseOf(c.s, "north", c.from); got != c.want {
				t.Errorf("phaseOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFleetControlConfig(t *testing.T) {
	if fleetControlFor(&Config{}) != nil {
		t.Error("unset backend.fleet_control must leave /model alone")
	}
	if fleetControlFor(nil) != nil {
		t.Error("nil config must leave /model alone")
	}
	fc := fleetControlFor(&Config{Backend: Backend{FleetControl: " http://chatterbox:4001/ "}})
	if fc == nil || fc.base != "http://chatterbox:4001" {
		t.Errorf("base = %+v, want trimmed http://chatterbox:4001", fc)
	}
	merged := mergeBackend(Backend{Type: "litellm", FleetControl: "http://user:4001"}, Backend{FleetControl: "http://project:4001"})
	if merged.FleetControl != "http://project:4001" || merged.Type != "litellm" {
		t.Errorf("project fleet_control should override the user's and keep the rest: %+v", merged)
	}
	if kept := mergeBackend(Backend{FleetControl: "http://user:4001"}, Backend{}); kept.FleetControl != "http://user:4001" {
		t.Errorf("an unset project fleet_control should keep the user's: %+v", kept)
	}
}

// A control plane behind the backend key (switchboard) gets it as a Bearer
// token on both calls; with no key configured, no Authorization header goes out.
func TestFleetControlSendsBackendKey(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"model":"north","state":"ready","loaded":"north","is_loaded":true}`))
	}))
	defer srv.Close()
	t.Setenv("CORTEX_TEST_FLEET_KEY", "sk-test")
	fc := fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL, KeyEnv: "CORTEX_TEST_FLEET_KEY"}})
	if _, _, err := fc.status(context.Background(), "north"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fc.load(context.Background(), "north"); err != nil {
		t.Fatal(err)
	}
	if len(auths) != 2 || auths[0] != "Bearer sk-test" || auths[1] != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test on both calls", auths)
	}
	auths = nil
	if _, _, err := fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL}}).status(context.Background(), "north"); err != nil {
		t.Fatal(err)
	}
	if len(auths) != 1 || auths[0] != "" {
		t.Errorf("no key configured: Authorization = %q, want none", auths)
	}
}

// A control plane that stops answering mid-swap ends the wait after a few
// missed polls with a warning, instead of painting "loading" until the
// five-minute timeout.
func TestFleetSwapGivesUpWhenControlPlaneGoesAway(t *testing.T) {
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"model":"north","state":"loading","loading":"north","from":"qwen"}`))
			return
		}
		gets++
		if gets == 1 { // the pre-load status check
			_, _ = w.Write([]byte(`{"model":"north","state":"ready","loaded":"qwen","is_loaded":false}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	fs := newFleetSwap(fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL}}), &out, false)
	fs.tick, fs.poll, fs.timeout, fs.misses = time.Millisecond, time.Millisecond, 5*time.Second, 3
	start := time.Now()
	err := fs.run(context.Background(), "north")
	if err == nil || !strings.Contains(err.Error(), "lost contact") {
		t.Fatalf("err = %v, want lost contact", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s: should give up after a few misses, not wait for the timeout", time.Since(start))
	}
	if !strings.Contains(out.String(), "lost contact with fleet control") {
		t.Errorf("output %q should warn about losing the control plane", out.String())
	}
}

// A refused load is a definite failure (errFleetLoadFailed, so /model can
// put the previous model back); a model already serving by is_loaded alone
// counts as ready in both the early exit and the poll loop.
func TestFleetSwapRefusalIsDefiniteAndReadyIsOnePredicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"model":"north","error":"slot cuda is leased to agent-a"}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"north","state":"ready","loaded":"qwen","is_loaded":false}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := newFleetSwap(fleetControlFor(&Config{Backend: Backend{FleetControl: srv.URL}}), &out, false).run(context.Background(), "north")
	if !errors.Is(err, errFleetLoadFailed) {
		t.Fatalf("err = %v, want errFleetLoadFailed", err)
	}
	if !strings.Contains(out.String(), "leased to agent-a") {
		t.Errorf("output %q should carry the fleet's reason", out.String())
	}
	if !fleetReady(fleetSlot{State: "ready", IsLoaded: true}, "north") || phaseOf(fleetSlot{State: "ready", IsLoaded: true}, "north", "") != "ready" {
		t.Error("is_loaded alone must read as ready in both places")
	}
	if fleetReady(fleetSlot{State: "ready", Loaded: "qwen"}, "north") {
		t.Error("another model loaded is not ready for north")
	}
}
