package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/style"
)

// A fleet whose GPU slots hold one model at a time (a LiteLLM proxy in front
// of several llama-servers sharing a port) answers a request for a model it
// hasn't loaded with whatever model IS loaded: the request succeeds, so
// `/model north` "works" while another model does the talking. When
// backend.fleet_control names the fleet's swap control plane, /model asks it
// to load the chosen model and shows the swap in place on one line
// (unloading → loading → ready) until the model can serve.
//
// The control plane speaks two calls (chatterbox's switchboard and its older
// fleetctl both serve them; switchboard refuses a swap while another agent
// holds a lease on the slot, and needs the backend key):
//
//	GET  <fleet_control>/v1/fleet/status?model=<id>  → fleetSlot; 404 = not a swappable model
//	POST <fleet_control>/v1/fleet/load {"model":id}  → fleetSlot; 202 = swap started
//
// Unset fleet_control, an unreachable control plane, or a model it doesn't
// know (404) all leave /model exactly as before: rename the request model.

// errFleetLoadFailed marks a definite failure: the control plane refused the
// load (a lease holds the slot, the swap is busy, the model is unknown to it)
// or the slot reported "failed". Timeouts, Ctrl-C and a lost control plane are
// not definite — the model may still arrive — so they don't carry it.
var errFleetLoadFailed = errors.New("fleet load failed")

// fleetReady is the one "model can serve" predicate for the early exit and
// the poll loop alike.
func fleetReady(s fleetSlot, model string) bool {
	return s.State == "ready" && (s.IsLoaded || s.Loaded == model)
}

// fleetSlot is the control plane's view of the slot a model lives in.
type fleetSlot struct {
	Model    string `json:"model"`
	State    string `json:"state"` // ready | loading | stopping | empty | failed
	Loaded   string `json:"loaded"`
	Loading  string `json:"loading"`
	Stopping string `json:"stopping"`
	Silicon  string `json:"silicon"`
	IsLoaded bool   `json:"is_loaded"`
	From     string `json:"from"`
	Error    string `json:"error"`
}

type fleetControl struct {
	base   string
	key    string // backend key, sent as a Bearer token when set
	client *http.Client
}

// fleetControlFor returns the configured control plane, or nil when
// backend.fleet_control is unset.
func fleetControlFor(cfg *Config) *fleetControl {
	if cfg == nil || strings.TrimSpace(cfg.Backend.FleetControl) == "" {
		return nil
	}
	return &fleetControl{base: strings.TrimRight(strings.TrimSpace(cfg.Backend.FleetControl), "/"),
		key:    cfg.backendKey(),
		client: &http.Client{Timeout: 5 * time.Second}}
}

func (fc *fleetControl) do(ctx context.Context, method, path string, body any) (fleetSlot, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fleetSlot{}, 0, fmt.Errorf("failed to encode fleet request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, fc.base+path, rd)
	if err != nil {
		return fleetSlot{}, 0, fmt.Errorf("failed to build fleet request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if fc.key != "" {
		req.Header.Set("Authorization", "Bearer "+fc.key)
	}
	res, err := fc.client.Do(req)
	if err != nil {
		return fleetSlot{}, 0, fmt.Errorf("failed to reach fleet control at %s: %w", fc.base, err)
	}
	defer func() { _ = res.Body.Close() }()
	var s fleetSlot
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&s); err != nil {
		return fleetSlot{}, res.StatusCode, fmt.Errorf("failed to read fleet control response: %w", err)
	}
	return s, res.StatusCode, nil
}

func (fc *fleetControl) status(ctx context.Context, model string) (fleetSlot, int, error) {
	return fc.do(ctx, http.MethodGet, "/v1/fleet/status?model="+url.QueryEscape(model), nil)
}

func (fc *fleetControl) load(ctx context.Context, model string) (fleetSlot, int, error) {
	return fc.do(ctx, http.MethodPost, "/v1/fleet/load", map[string]string{"model": model})
}

// fleetSwap drives one swap and its status line.
type fleetSwap struct {
	fc      *fleetControl
	out     io.Writer
	live    bool          // repaint one line in place; false prints one plain line per phase
	tick    time.Duration // repaint cadence
	poll    time.Duration // status poll cadence
	timeout time.Duration // give up waiting (the load continues on the fleet)
	misses  int           // consecutive failed status polls before giving up the wait
}

func newFleetSwap(fc *fleetControl, out io.Writer, live bool) *fleetSwap {
	return &fleetSwap{fc: fc, out: out, live: live, tick: 120 * time.Millisecond, poll: 600 * time.Millisecond, timeout: 5 * time.Minute, misses: 5}
}

// phaseOf names what the slot is doing for a swap to model, given the model
// it held before ("" when unknown).
func phaseOf(s fleetSlot, model, from string) string {
	switch {
	case fleetReady(s, model):
		return "ready"
	case s.State == "failed":
		return "failed"
	case s.Stopping != "":
		return "unloading " + s.Stopping
	case s.State == "stopping":
		if from != "" {
			return "unloading " + from
		}
		return "unloading"
	case s.State == "ready" && s.Loaded != "" && s.Loaded != model:
		// The old model still answers: systemd hasn't stopped it yet.
		return "unloading " + s.Loaded
	default:
		return "loading"
	}
}

// statusLine is the one line the swap repaints: model, phase, a slow
// three-dot pulse, elapsed seconds. Subtle by design: dim text, no glyphs.
func statusLine(model, phase string, elapsed time.Duration, frame int) string {
	dots := strings.Repeat(".", frame%4)
	return style.Paint(model, style.Accent) + style.Paint(fmt.Sprintf(" · %s%-3s %ds", phase, dots, int(elapsed.Seconds())), style.Dim)
}

// run makes model resident and returns once it can serve, it fails, the
// wait times out, or ctx ends. A model the control plane doesn't manage, or
// an unreachable control plane, returns nil having printed nothing.
func (fs *fleetSwap) run(ctx context.Context, model string) error {
	s, code, err := fs.fc.status(ctx, model)
	if err != nil || code == http.StatusNotFound {
		return nil
	}
	if fleetReady(s, model) {
		_, _ = fmt.Fprintln(fs.out, style.Paint(fmt.Sprintf("%s is loaded on %s", model, s.Silicon), style.Dim))
		return nil
	}
	s, code, err = fs.fc.load(ctx, model)
	if err != nil {
		_, _ = fmt.Fprintln(fs.out, style.Paint("could not load "+model+": "+err.Error(), style.Warn))
		return fmt.Errorf("%w: %s: %v", errFleetLoadFailed, model, err)
	}
	if code >= 300 {
		msg := s.Error
		if msg == "" {
			msg = fmt.Sprintf("fleet control returned HTTP %d", code)
		}
		_, _ = fmt.Fprintln(fs.out, style.Paint("could not load "+model+": "+msg, style.Warn))
		return fmt.Errorf("%w: %s: %s", errFleetLoadFailed, model, msg)
	}
	from := s.From
	start := time.Now()
	deadline := start.Add(fs.timeout)
	tick := time.NewTicker(fs.tick)
	defer tick.Stop()
	lastPoll, frame, phase, printed, missed := time.Now(), 0, phaseOf(s, model, from), "", 0
	paint := func() {
		if fs.live {
			_, _ = fmt.Fprintf(fs.out, "\r%s\033[K", statusLine(model, phase, time.Since(start), frame/3))
		} else if phase != printed {
			_, _ = fmt.Fprintln(fs.out, style.Paint(model+": "+phase, style.Dim))
			printed = phase
		}
	}
	finish := func(line string) {
		if fs.live {
			_, _ = fmt.Fprintf(fs.out, "\r%s\033[K\n", line)
		} else {
			_, _ = fmt.Fprintln(fs.out, line)
		}
	}
	for {
		if phase == "ready" {
			finish(style.Paint(model, style.Accent) + style.Paint(" ready", style.OK) +
				style.Paint(fmt.Sprintf(" on %s · %ds", s.Silicon, int(time.Since(start).Seconds())), style.Dim))
			return nil
		}
		if phase == "failed" {
			finish(style.Paint(model+" failed to load on "+s.Silicon+" (check its service log on the fleet)", style.Err))
			return fmt.Errorf("%w: %s", errFleetLoadFailed, model)
		}
		paint()
		select {
		case <-ctx.Done():
			finish(style.Paint(model+" is still loading on the fleet; requests answer once it's ready", style.Warn))
			return ctx.Err()
		case <-tick.C:
		}
		frame++
		if time.Now().After(deadline) {
			finish(style.Paint(fmt.Sprintf("%s still not ready after %s; it keeps loading on the fleet", model, fs.timeout), style.Warn))
			return fmt.Errorf("timed out waiting for %s", model)
		}
		if time.Since(lastPoll) >= fs.poll {
			lastPoll = time.Now()
			if ns, code, err := fs.fc.status(ctx, model); err == nil && code == http.StatusOK {
				s = ns
				phase = phaseOf(s, model, from)
				missed = 0
			} else if ctx.Err() == nil {
				// Without status the line would say "loading" until the timeout,
				// whatever the fleet is doing; after a few misses, say so and stop.
				missed++
				if missed >= fs.misses {
					finish(style.Paint(fmt.Sprintf("lost contact with fleet control at %s; %s may still be loading on the fleet", fs.fc.base, model), style.Warn))
					return fmt.Errorf("lost contact with fleet control while loading %s", model)
				}
			}
		}
	}
}

// switchFleetModel is /model's hook after SetModel: on a fleet with a swap
// control plane, load the model and wait for it with the in-place line.
// Ctrl-C stops the wait, not the load. When the fleet definitely can't load
// it (refused, or failed), the session goes back to prev — otherwise every
// next request would name a model the fleet won't serve.
func switchFleetModel(cs *CortexSession, model, prev string) {
	fc := fleetControlFor(cs.Config)
	if fc == nil {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := newFleetSwap(fc, os.Stdout, renderEnabled()).run(ctx, model)
	if errors.Is(err, errFleetLoadFailed) && prev != "" && prev != model {
		cs.SetModel(prev)
		_, _ = fmt.Fprintln(os.Stdout, style.Paint("code model -> "+prev+" (back; "+model+" didn't load)", style.Dim))
	}
}
