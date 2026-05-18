package ops

import (
	"context"
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition/dag"
)

func TestDetectUnfamiliarity_GoBleedPattern(t *testing.T) {
	// The third-arm prototype's failure mode: imports sqlx but the
	// body uses database/sql APIs. AST detects unused sqlx import.
	code := `package main

import (
	"database/sql"
	_ "github.com/lib/pq"
	"github.com/jmoiron/sqlx"
)

func InsertUser(name, email string) error {
	db, err := sql.Open("postgres", "host=localhost")
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO users (name, email) VALUES ($1, $2)", name, email)
	return err
}
`
	handler := NewDetectUnfamiliarityHandler(DetectUnfamiliarityConfig{})
	res, err := handler(context.Background(), map[string]any{"code": code}, dag.Budget{LatencyMS: 1000, Tokens: 100, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	findings, ok := res.Out["findings"].([]UnfamiliarityFinding)
	if !ok {
		t.Fatalf("findings type: got %T", res.Out["findings"])
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Package)
	}
	wantPkg := "github.com/jmoiron/sqlx"
	found := false
	for _, p := range got {
		if p == wantPkg {
			found = true
		}
	}
	if !found {
		t.Errorf("expected finding for %s; got %v", wantPkg, got)
	}
}

func TestDetectUnfamiliarity_NoFindingsWhenImportUsed(t *testing.T) {
	code := `package main

import "github.com/jmoiron/sqlx"

func F() (*sqlx.DB, error) {
	return sqlx.Connect("postgres", "")
}
`
	handler := NewDetectUnfamiliarityHandler(DetectUnfamiliarityConfig{})
	res, _ := handler(context.Background(), map[string]any{"code": code}, dag.Budget{LatencyMS: 1000, Tokens: 100, Depth: 5})
	findings := res.Out["findings"].([]UnfamiliarityFinding)
	for _, f := range findings {
		if f.Package == "github.com/jmoiron/sqlx" {
			t.Errorf("sqlx is used; should not be flagged: %+v", findings)
		}
	}
}

func TestDetectUnfamiliarity_BlankAndDotImportsSkipped(t *testing.T) {
	code := `package main

import (
	_ "github.com/lib/pq"
	. "fmt"
)

func F() { Println("hi") }
`
	handler := NewDetectUnfamiliarityHandler(DetectUnfamiliarityConfig{})
	res, _ := handler(context.Background(), map[string]any{"code": code}, dag.Budget{LatencyMS: 1000, Tokens: 100, Depth: 5})
	findings := res.Out["findings"].([]UnfamiliarityFinding)
	if len(findings) != 0 {
		t.Errorf("blank/dot imports should not be flagged; got %+v", findings)
	}
}

func TestDetectUnfamiliarity_EmptyInput(t *testing.T) {
	handler := NewDetectUnfamiliarityHandler(DetectUnfamiliarityConfig{})
	res, err := handler(context.Background(), map[string]any{"code": ""}, dag.Budget{LatencyMS: 1000, Tokens: 100, Depth: 5})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	findings := res.Out["findings"].([]UnfamiliarityFinding)
	if len(findings) != 0 {
		t.Errorf("empty code should produce no findings; got %+v", findings)
	}
}
