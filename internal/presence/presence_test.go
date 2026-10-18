package presence

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// fakeTerm records every sequence written, one entry per open/write/close.
type fakeTerm struct {
	writes []string
	fail   bool
}

func (f *fakeTerm) open() (io.WriteCloser, error) {
	if f.fail {
		return nil, errors.New("no controlling terminal")
	}
	return nopCloser{writerFunc(func(p []byte) (int, error) {
		f.writes = append(f.writes, string(p))
		return len(p), nil
	})}, nil
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestNewEmitterGating(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantNil bool
		wantPid int
	}{
		{"outside supacode", map[string]string{}, true, 0},
		{"socket alone is not a surface", map[string]string{socketEnv: "/tmp/s"}, true, 0},
		{"surface over ssh omits pid", map[string]string{surfaceEnv: "S"}, false, 0},
		{"local surface carries pid", map[string]string{surfaceEnv: "S", socketEnv: "/tmp/s"}, false, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTerm{}
			e := newEmitter(env(tt.vars), ft.open, 42)
			if (e == nil) != tt.wantNil {
				t.Fatalf("newEmitter nil = %v, want %v", e == nil, tt.wantNil)
			}
			if e != nil && e.pid != tt.wantPid {
				t.Errorf("pid = %d, want %d", e.pid, tt.wantPid)
			}
		})
	}
}

func TestEmitWireBytes(t *testing.T) {
	tests := []struct {
		ev   Event
		pid  int
		want string
	}{
		{Busy, 0, "\x1b]3008;start=cortex;event=busy\x1b\\"},
		{Idle, 77, "\x1b]3008;start=cortex;event=idle;pid=77\x1b\\"},
		{SessionStart, 77, "\x1b]3008;start=cortex;event=session_start;pid=77\x1b\\"},
		{SessionEnd, 77, "\x1b]3008;end=cortex;event=session_end;pid=77\x1b\\"},
		{AwaitingInput, 0, "\x1b]3008;start=cortex;event=awaiting_input\x1b\\"},
		{Compacting, 0, "\x1b]3008;start=cortex;event=compacting\x1b\\"},
		{Error, 0, "\x1b]3008;start=cortex;event=error\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(string(tt.ev), func(t *testing.T) {
			ft := &fakeTerm{}
			e := &Emitter{open: ft.open, pid: tt.pid}
			e.Emit(tt.ev)
			if len(ft.writes) != 1 || ft.writes[0] != tt.want {
				t.Errorf("writes = %q, want [%q]", ft.writes, tt.want)
			}
		})
	}
}

func TestEmitSkipsRepeats(t *testing.T) {
	ft := &fakeTerm{}
	e := &Emitter{open: ft.open}
	for _, ev := range []Event{Busy, Busy, AwaitingInput, Busy, Busy, Idle} {
		e.Emit(ev)
	}
	var got []string
	for _, w := range ft.writes {
		got = append(got, strings.TrimSuffix(strings.TrimPrefix(w, "\x1b]3008;start=cortex;event="), "\x1b\\"))
	}
	want := []string{"busy", "awaiting_input", "busy", "idle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestNilEmitterIsInert(t *testing.T) {
	var e *Emitter
	e.Emit(Busy)
	e.Notify("t", "b")
}

func TestWriteFailureIsSilent(t *testing.T) {
	ft := &fakeTerm{fail: true}
	e := &Emitter{open: ft.open}
	e.Emit(Busy)
	e.Notify("", "done")
	if len(ft.writes) != 0 {
		t.Errorf("writes = %q, want none", ft.writes)
	}
}

// decodeField reverses notifyField the way the host does: base64, then the
// bytes read back as the inside of a JSON string literal.
func decodeField(t *testing.T, field string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(field)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	var s string
	if err := json.Unmarshal(append(append([]byte{'"'}, raw...), '"'), &s); err != nil {
		t.Fatalf("field %q is not a JSON string body: %v", raw, err)
	}
	return s
}

func notifyFields(t *testing.T, seq string) (title, body string) {
	t.Helper()
	const prefix = "\x1b]3008;start=cortex;kind=notify;"
	if !strings.HasPrefix(seq, prefix) || !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatalf("sequence framing wrong: %q", seq)
	}
	meta := strings.TrimSuffix(strings.TrimPrefix(seq, prefix), "\x1b\\")
	for _, pair := range strings.Split(meta, ";") {
		k, v, _ := strings.Cut(pair, "=")
		switch k {
		case "title":
			title = decodeField(t, v)
		case "body":
			body = decodeField(t, v)
		default:
			t.Fatalf("unexpected field %q", k)
		}
	}
	return title, body
}

func TestNotifyRoundTrip(t *testing.T) {
	tests := []struct{ name, title, body string }{
		{"plain", "", "All tests pass."},
		{"escapes and markup", "cortex", "line one\nline \"two\" <b>&</b>\ttab \\ slash"},
		{"semicolons stay inside base64", "", "a;b=c;kind=notify"},
		{"unicode", "", "naïve café ❯ 日本"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTerm{}
			e := &Emitter{open: ft.open}
			e.Notify(tt.title, tt.body)
			if len(ft.writes) != 1 {
				t.Fatalf("writes = %d, want 1", len(ft.writes))
			}
			title, body := notifyFields(t, ft.writes[0])
			if title != tt.title || body != tt.body {
				t.Errorf("round trip = (%q, %q), want (%q, %q)", title, body, tt.title, tt.body)
			}
		})
	}
}

func TestNotifyBudgets(t *testing.T) {
	long := strings.Repeat("é\"", 2000) // multi-byte runes and escapes at every cut point
	seq := notifySequence(long, long)
	if len(seq) >= 2048 {
		t.Errorf("sequence is %d bytes; the host's OSC buffer discards >= 2048", len(seq))
	}
	title, body := notifyFields(t, seq)
	for name, got := range map[string]string{"title": title, "body": body} {
		if !strings.HasPrefix(long, got) || got == "" {
			t.Errorf("%s is not a non-empty prefix of the input: %q", name, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s split a rune", name)
		}
	}
	for name, field := range map[string]struct {
		got    string
		budget int
	}{"title": {title, notifyTitleBudget}, "body": {body, notifyBodyBudget}} {
		if n := len(jsonEscape(field.got)); n > field.budget {
			t.Errorf("%s escaped length %d over budget %d", name, n, field.budget)
		}
	}
}

func TestJSONEscapeLeavesHTMLAlone(t *testing.T) {
	if got := jsonEscape("<a&b>"); got != "<a&b>" {
		t.Errorf("jsonEscape = %q", got)
	}
	var buf bytes.Buffer
	buf.WriteString(jsonEscape("\x01"))
	if buf.String() != `\u0001` {
		t.Errorf("control char escape = %q", buf.String())
	}
}
