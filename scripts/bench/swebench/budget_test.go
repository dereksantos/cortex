package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBudgetGuard(t *testing.T) {
	tests := []struct {
		name      string
		g         BudgetGuard
		observed  []float64
		spent     float64
		wantStart bool
		wantEst   float64
		wantLimit float64
	}{
		{"first instance uses EstFirst", BudgetGuard{Budget: 5, InstanceCap: 1, EstFirst: 1}, nil, 0, true, 1, 1},
		{"observed max drives estimate", BudgetGuard{Budget: 5, InstanceCap: 2, EstFirst: 1}, []float64{0.3, 1.5, 0.2}, 3.6, false, 1.5, 1.4},
		{"estimate capped by instance cap", BudgetGuard{Budget: 5, InstanceCap: 1, EstFirst: 3}, nil, 3.9, true, 1, 1},
		{"exactly at budget may start", BudgetGuard{Budget: 5, InstanceCap: 1, EstFirst: 1}, []float64{1}, 4, true, 1, 1},
		{"limit clipped to remaining budget", BudgetGuard{Budget: 5, InstanceCap: 1, EstFirst: 0.1}, nil, 4.7, true, 0.1, 0.3},
		{"overspent limit is zero", BudgetGuard{Budget: 5, InstanceCap: 1, EstFirst: 0.1}, nil, 5.2, false, 0.1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.g
			for _, c := range tt.observed {
				g.Observe(c)
			}
			if got := g.CanStart(tt.spent); got != tt.wantStart {
				t.Errorf("CanStart(%v)=%v want %v", tt.spent, got, tt.wantStart)
			}
			if got := g.NextEstimate(); math.Abs(got-tt.wantEst) > 1e-9 {
				t.Errorf("NextEstimate=%v want %v", got, tt.wantEst)
			}
			if got := g.InstanceLimit(tt.spent); math.Abs(got-tt.wantLimit) > 1e-9 {
				t.Errorf("InstanceLimit=%v want %v", got, tt.wantLimit)
			}
		})
	}
}

func TestOpenRouterUsage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    float64
		wantErr bool
	}{
		{"ok", 200, `{"data":{"usage":84.25,"limit":null}}`, 84.25, false},
		{"http error", 401, `{"error":"no"}`, 0, true},
		{"bad json", 200, `nope`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			u := newOpenRouterUsage(srv.URL+"/api/v1/", "k-test")
			got, err := u.Usage(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr %v", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "k-test") {
				t.Error("error message leaks the key")
			}
			if got != tt.want {
				t.Errorf("usage=%v want %v", got, tt.want)
			}
			if gotAuth != "Bearer k-test" || gotPath != "/api/v1/key" {
				t.Errorf("request auth=%q path=%q", gotAuth, gotPath)
			}
		})
	}
}

func TestRedactTree(t *testing.T) {
	const secret = "sk-or-v1-TESTSECRET"
	tests := []struct {
		name      string
		files     map[string]string
		wantFound bool
	}{
		{"clean tree", map[string]string{"a.txt": "nothing here", "sub/b.jsonl": "{}"}, false},
		{"secret in nested file", map[string]string{"a.txt": "ok", "sub/t.jsonl": `{"content":"env: KEY=` + secret + `"}`}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tt.files {
				p := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			found, err := redactTree(root, secret)
			if err != nil {
				t.Fatal(err)
			}
			if found != tt.wantFound {
				t.Errorf("found=%v want %v", found, tt.wantFound)
			}
			for rel := range tt.files {
				b, _ := os.ReadFile(filepath.Join(root, rel))
				if strings.Contains(string(b), secret) {
					t.Errorf("%s still contains the secret", rel)
				}
			}
		})
	}
	if found, err := redactTree(filepath.Join(t.TempDir(), "missing"), secret); err != nil || found {
		t.Errorf("missing root: found=%v err=%v", found, err)
	}
}
