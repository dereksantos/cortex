package commands

import (
	"strings"
	"testing"
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
