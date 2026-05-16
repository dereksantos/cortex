package commands

import (
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

func TestApplyNIAHFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantLengths string
		wantDepths  string
		wantNeedle  string
		wantSeed    string
		wantErr     string // substring match; "" → expect success
	}{
		{
			name:        "single length and depth",
			args:        []string{"--length", "8k", "--depth", "0.5"},
			wantLengths: "8k",
			wantDepths:  "0.5",
		},
		{
			name:        "repeated length and depth accumulate",
			args:        []string{"--length", "8k", "--length", "16k", "--depth", "0.0", "--depth", "0.5", "--depth", "1.0"},
			wantLengths: "8k,16k",
			wantDepths:  "0.0,0.5,1.0",
		},
		{
			name:        "comma-separated single flag",
			args:        []string{"--length", "8k,16k", "--depth", "0.0,0.5,1.0"},
			wantLengths: "8k,16k",
			wantDepths:  "0.0,0.5,1.0",
		},
		{
			name:       "needle and seed singletons",
			args:       []string{"--needle", "find me", "--seed", "42"},
			wantNeedle: "find me",
			wantSeed:   "42",
		},
		{
			name:    "model flag rejected",
			args:    []string{"--length", "8k", "--model", "gpt-4"},
			wantErr: "--model is not valid",
		},
		{
			name:    "-m short form rejected",
			args:    []string{"-m", "claude-3"},
			wantErr: "--model is not valid",
		},
		{
			name:    "missing length value",
			args:    []string{"--length"},
			wantErr: "--length requires a value",
		},
		{
			name:    "missing depth value",
			args:    []string{"--depth"},
			wantErr: "--depth requires a value",
		},
		{
			name:    "missing needle value",
			args:    []string{"--needle"},
			wantErr: "--needle requires a value",
		},
		{
			name:    "missing seed value",
			args:    []string{"--seed"},
			wantErr: "--seed requires a value",
		},
		{
			name: "empty args is a no-op",
			args: nil,
			// All want-fields empty.
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := benchmarks.LoadOpts{Filter: map[string]string{}}
			err := applyNIAHFlags(tc.args, &opts)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil; opts=%+v", tc.wantErr, opts)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err=%q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got := opts.Filter["lengths"]; got != tc.wantLengths {
				t.Errorf("Filter[lengths] = %q, want %q", got, tc.wantLengths)
			}
			if got := opts.Filter["depths"]; got != tc.wantDepths {
				t.Errorf("Filter[depths] = %q, want %q", got, tc.wantDepths)
			}
			if got := opts.Filter["needle"]; got != tc.wantNeedle {
				t.Errorf("Filter[needle] = %q, want %q", got, tc.wantNeedle)
			}
			if got := opts.Filter["seed"]; got != tc.wantSeed {
				t.Errorf("Filter[seed] = %q, want %q", got, tc.wantSeed)
			}
		})
	}
}

func TestJoinExpandingCSV(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"8k"}, "8k"},
		{[]string{"8k", "16k"}, "8k,16k"},
		{[]string{"8k,16k", "32k"}, "8k,16k,32k"},
		{[]string{" 8k ", "", " 16k"}, "8k,16k"},
		{[]string{"8k, ,16k"}, "8k,16k"},
	}
	for _, tc := range cases {
		got := joinExpandingCSV(tc.in)
		if got != tc.want {
			t.Errorf("joinExpandingCSV(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
