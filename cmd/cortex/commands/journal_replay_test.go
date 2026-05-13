package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/llm"
)

func TestParseConfigOverrides(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantModel string
		wantProv  string
		wantTemp  *float64
		wantMaxT  *int
		wantErr   string
	}{
		{name: "empty", input: ""},
		{name: "single model", input: "model=claude-haiku-4.5", wantModel: "claude-haiku-4.5"},
		{
			name:      "model and temperature",
			input:     "model=claude-opus-4.7,temperature=0.7",
			wantModel: "claude-opus-4.7",
			wantTemp:  fptr(0.7),
		},
		{
			name:      "provider model max_tokens",
			input:     "provider=anthropic,model=claude-haiku-4.5,max_tokens=1024",
			wantProv:  "anthropic",
			wantModel: "claude-haiku-4.5",
			wantMaxT:  iptr(1024),
		},
		{
			name:      "slashes preserved in model value",
			input:     "model=anthropic/claude-opus-4.7",
			wantModel: "anthropic/claude-opus-4.7",
		},
		{
			name:      "spaces trimmed around k v",
			input:     "  model = claude-haiku-4.5 ,  temperature = 0.0  ",
			wantModel: "claude-haiku-4.5",
			wantTemp:  fptr(0.0),
		},
		{
			name:      "quoted values stripped",
			input:     `model="claude/x",temperature="0.5"`,
			wantModel: "claude/x",
			wantTemp:  fptr(0.5),
		},
		{name: "unknown key rejected", input: "secret=foo", wantErr: "unknown override key"},
		{name: "empty value rejected", input: "model=", wantErr: "empty value"},
		{name: "empty key rejected", input: "=foo", wantErr: "empty key"},
		{name: "missing equals rejected", input: "model", wantErr: "missing '=' in"},
		{name: "non-numeric temperature", input: "temperature=hot", wantErr: "temperature"},
		{name: "temperature out of range (high)", input: "temperature=3.0", wantErr: "temperature"},
		{name: "temperature out of range (negative)", input: "temperature=-0.1", wantErr: "temperature"},
		{name: "max_tokens non-int", input: "max_tokens=many", wantErr: "max_tokens"},
		{name: "max_tokens zero", input: "max_tokens=0", wantErr: "max_tokens"},
		{name: "max_tokens negative", input: "max_tokens=-1", wantErr: "max_tokens"},
		{name: "duplicate key (last wins)", input: "model=a,model=b", wantModel: "b"},
		{name: "shell injection guard — semicolon in value", input: "model=a;rm -rf /", wantErr: "invalid character"},
		{name: "newline in value rejected", input: "model=a\nb", wantErr: "invalid character"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfigOverrides(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Model != tc.wantModel {
				t.Errorf("Model=%q want %q", got.Model, tc.wantModel)
			}
			if got.Provider != tc.wantProv {
				t.Errorf("Provider=%q want %q", got.Provider, tc.wantProv)
			}
			if !fptrEqual(got.Temperature, tc.wantTemp) {
				t.Errorf("Temperature=%v want %v", deref(got.Temperature), deref(tc.wantTemp))
			}
			if !iptrEqual(got.MaxTokens, tc.wantMaxT) {
				t.Errorf("MaxTokens=%v want %v", iptrDeref(got.MaxTokens), iptrDeref(tc.wantMaxT))
			}
		})
	}
}

func TestConfigOverrides_IsEmpty(t *testing.T) {
	var o ConfigOverrides
	if !o.IsEmpty() {
		t.Error("zero-value ConfigOverrides should be empty")
	}
	o.Model = "x"
	if o.IsEmpty() {
		t.Error("ConfigOverrides with Model set should not be empty")
	}
}

// fakeLLM implements llm.Provider for counterfactual replay tests. It
// echoes back a stub rerank JSON over a deterministic ID order so we
// can assert the counterfactual output without an actual model call.
type fakeLLM struct {
	respond func(prompt, system string) string
}

func (f *fakeLLM) Generate(_ context.Context, prompt string) (string, error) {
	if f.respond == nil {
		return "", nil
	}
	return f.respond(prompt, ""), nil
}
func (f *fakeLLM) GenerateWithSystem(_ context.Context, prompt, system string) (string, error) {
	if f.respond == nil {
		return "", nil
	}
	return f.respond(prompt, system), nil
}
func (f *fakeLLM) GenerateWithStats(ctx context.Context, prompt string) (string, llm.GenerationStats, error) {
	s, err := f.Generate(ctx, prompt)
	return s, llm.GenerationStats{}, err
}
func (f *fakeLLM) IsAvailable() bool { return true }
func (f *fakeLLM) Name() string      { return "fake" }

func TestCounterfactualReflectRerank_HappyPath(t *testing.T) {
	src := &journal.Entry{Type: journal.TypeReflectRerank, V: 1, Offset: 7}
	payload := &journal.ReflectRerankPayload{
		QueryText:     "authentication patterns",
		InputIDs:      []string{"a", "b", "c"},
		InputContents: map[string]string{"a": "use JWT", "b": "use sessions", "c": "use OAuth"},
		RankedIDs:     []string{"a", "b", "c"},
	}
	overrides := ConfigOverrides{Model: "claude-haiku-4.5"}

	fake := &fakeLLM{respond: func(prompt, system string) string {
		return `{"ranking": ["c", "a", "b"], "contradictions": [], "reasoning": "fake"}`
	}}
	factory := func(_ ConfigOverrides) (llm.Provider, error) { return fake, nil }

	out := counterfactualReflectRerank(context.Background(), src, payload, overrides, factory)
	if out.Status != journal.ReplayStatusExecuted {
		t.Fatalf("Status=%q want %q (err=%q)", out.Status, journal.ReplayStatusExecuted, out.Error)
	}
	if len(out.CounterfactualRankedIDs) != 3 || out.CounterfactualRankedIDs[0] != "c" {
		t.Errorf("CounterfactualRankedIDs=%v want [c a b]", out.CounterfactualRankedIDs)
	}
	if out.JaccardK == 0 {
		t.Errorf("JaccardK=0, want >0")
	}
	// Top-3 intersection is full → Jaccard=1.0 regardless of order.
	if out.JaccardTopK != 1.0 {
		t.Errorf("JaccardTopK=%v want 1.0", out.JaccardTopK)
	}
}

func TestCounterfactualReflectRerank_MissingInputContents(t *testing.T) {
	src := &journal.Entry{Type: journal.TypeReflectRerank, V: 1, Offset: 1}
	payload := &journal.ReflectRerankPayload{
		QueryText: "x",
		InputIDs:  []string{"a"},
		RankedIDs: []string{"a"},
	}
	out := counterfactualReflectRerank(context.Background(), src, payload, ConfigOverrides{Model: "x"}, func(ConfigOverrides) (llm.Provider, error) { return nil, nil })
	if out.Status != journal.ReplayStatusFailed {
		t.Errorf("Status=%q want %q", out.Status, journal.ReplayStatusFailed)
	}
	if !strings.Contains(out.Error, "no input_contents") {
		t.Errorf("Error=%q want contains 'no input_contents'", out.Error)
	}
}

func TestCounterfactualReflectRerank_ProviderUnavailable(t *testing.T) {
	src := &journal.Entry{Type: journal.TypeReflectRerank, V: 1, Offset: 2}
	payload := &journal.ReflectRerankPayload{
		QueryText:     "x",
		InputIDs:      []string{"a"},
		InputContents: map[string]string{"a": "alpha"},
		RankedIDs:     []string{"a"},
	}
	out := counterfactualReflectRerank(context.Background(), src, payload, ConfigOverrides{Model: "x"}, func(ConfigOverrides) (llm.Provider, error) { return nil, nil })
	if out.Status != journal.ReplayStatusFailed {
		t.Errorf("Status=%q want failed (err=%q)", out.Status, out.Error)
	}
}

// TestRunReplay_EmitsCounterfactualEntries drives runReplay end-to-end:
// seed a reflect.rerank entry, run replay with --config-overrides +
// --execute against a fake LLM, and read back the replay.counterfactual
// entry from the writer-class journal.
func TestRunReplay_EmitsCounterfactualEntries(t *testing.T) {
	tempDir := t.TempDir()
	reflectDir := filepath.Join(tempDir, "journal", "reflect")
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: reflectDir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	payload := journal.ReflectRerankPayload{
		QueryText:     "auth",
		InputIDs:      []string{"a", "b"},
		InputContents: map[string]string{"a": "alpha", "b": "beta"},
		RankedIDs:     []string{"a", "b"},
	}
	entry, err := journal.NewReflectRerankEntry(payload)
	if err != nil {
		t.Fatalf("new entry: %v", err)
	}
	if _, err := w.Append(entry); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Run buildReplayPayload directly (avoids LLM cfg+flag boilerplate
	// from runReplay; the same code path lands in the replay journal).
	src := readFirstReflectEntry(t, reflectDir)
	overrides := ConfigOverrides{Model: "fake-model"}
	fake := &fakeLLM{respond: func(prompt, _ string) string {
		_ = prompt
		_ = cognition.Result{} // keep cognition import attached for clarity
		return `{"ranking":["b","a"],"reasoning":"flip"}`
	}}
	factory := func(_ ConfigOverrides) (llm.Provider, error) { return fake, nil }
	cf, ok := buildReplayPayload(context.Background(), src, "reflect", overrides, true, factory)
	if !ok {
		t.Fatal("buildReplayPayload returned ok=false on reflect.rerank entry")
	}
	cfEntry, err := journal.NewReplayCounterfactualEntry(cf)
	if err != nil {
		t.Fatalf("build counterfactual entry: %v", err)
	}

	rw, err := openReplayWriter(tempDir)
	if err != nil {
		t.Fatalf("open replay writer: %v", err)
	}
	if _, err := rw.Append(cfEntry); err != nil {
		t.Fatalf("append cf: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close cf: %v", err)
	}

	reader, err := journal.NewReader(filepath.Join(tempDir, "journal", "replay"))
	if err != nil {
		t.Fatalf("replay reader: %v", err)
	}
	defer reader.Close()
	got, err := reader.Next()
	if err != nil {
		t.Fatalf("read cf: %v", err)
	}
	cfPayload, err := journal.ParseReplayCounterfactual(got)
	if err != nil {
		t.Fatalf("parse cf: %v", err)
	}
	if cfPayload.Status != journal.ReplayStatusExecuted {
		t.Errorf("status=%q want executed (err=%q)", cfPayload.Status, cfPayload.Error)
	}
	if cfPayload.SourceType != journal.TypeReflectRerank {
		t.Errorf("SourceType=%q", cfPayload.SourceType)
	}
}

func readFirstReflectEntry(t *testing.T, dir string) *journal.Entry {
	t.Helper()
	r, err := journal.NewReader(dir)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer r.Close()
	e, err := r.Next()
	if err == io.EOF {
		t.Fatal("no entries")
	}
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return e
}

func TestJaccardTopK(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		k    int
		want float64
	}{
		{"identical top3", []string{"a", "b", "c"}, []string{"a", "b", "c"}, 3, 1.0},
		{"reverse same set", []string{"a", "b", "c"}, []string{"c", "b", "a"}, 3, 1.0},
		{"half overlap", []string{"a", "b", "c", "d"}, []string{"a", "x", "y", "z"}, 4, 1.0 / 7.0},
		{"empty both", nil, nil, 5, 1.0},
		{"empty one", nil, []string{"a"}, 5, 0.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jaccardTopK(tc.a, tc.b, tc.k)
			if got != tc.want && !floatNear(got, tc.want, 1e-9) {
				t.Errorf("jaccardTopK(%v,%v,%d)=%v want %v", tc.a, tc.b, tc.k, got, tc.want)
			}
		})
	}
}

func floatNear(a, b, eps float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < eps
}

// fakeFile keeps os imported.
var _ = os.Stat

func fptr(f float64) *float64    { return &f }
func iptr(i int) *int            { return &i }
func deref(p *float64) any       { if p == nil { return nil }; return *p }
func iptrDeref(p *int) any       { if p == nil { return nil }; return *p }
func fptrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func iptrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
