package eval

import (
	"go/ast"
	"testing"
)

func TestSmellDensity_Clean(t *testing.T) {
	clean := `package h

import "net/http"

func List(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func Get(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books.go"},
		[]string{clean},
	)
	got, err := smellDensity(paths)
	if err != nil {
		t.Fatalf("smellDensity: %v", err)
	}
	if got != 0 {
		t.Errorf("clean code should score 0, got %.3f", got)
	}
}

func TestSmellDensity_Smelly(t *testing.T) {
	// 1 function with high cyclomatic, deep nesting, magic numbers, and >50 lines.
	smelly := `package h

func Big(x int) int {
	if x == 5 {
		if x > 7 {
			if x < 13 {
				if x != 17 {
					if x == 19 {
						return 23
					}
				}
			}
		}
	}
	if x == 29 || x == 31 {
		return 37
	}
	if x == 41 && x != 43 {
		return 47
	}
	for i := 0; i < 53; i++ {
		for j := 0; j < 59; j++ {
			x += 61
		}
	}
	switch x {
	case 67:
		return 71
	case 73:
		return 79
	case 83:
		return 89
	case 97:
		return 101
	case 103:
		return 107
	case 109:
		return 113
	case 127:
		return 131
	case 137:
		return 139
	case 149:
		return 151
	}
	if x > 157 {
		return 163
	}
	if x > 167 {
		return 173
	}
	return 179
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"smelly.go"},
		[]string{smelly},
	)
	got, err := smellDensity(paths)
	if err != nil {
		t.Fatalf("smellDensity: %v", err)
	}
	if got <= 0 {
		t.Errorf("smelly code should score > 0, got %.3f", got)
	}
}

func TestCyclomatic(t *testing.T) {
	src := `package h

func F(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 && x > -10 {
		return -1
	}
	for i := 0; i < x; i++ {
		x++
	}
	switch x {
	case 1:
		return 1
	case 2:
		return 2
	}
	return 0
}
`
	_, paths := writeFixtureFiles(t, []string{"x.go"}, []string{src})
	file, _, err := parseGoFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		// 1 base + 2 ifs + 1 && + 1 for + 2 case = 7
		got := cyclomatic(fn.Body)
		want := 7
		if got != want {
			t.Errorf("cyclomatic = %d, want %d", got, want)
		}
	}
}

func TestMaxNesting(t *testing.T) {
	src := `package h

func F(x int) {
	if x > 0 {
		for i := 0; i < x; i++ {
			if i%2 == 0 {
				switch i {
				case 1:
					return
				}
			}
		}
	}
}
`
	_, paths := writeFixtureFiles(t, []string{"x.go"}, []string{src})
	file, _, err := parseGoFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		// if(1) > for(2) > if(3) > switch(4) = 4
		got := maxNesting(fn.Body)
		want := 4
		if got != want {
			t.Errorf("maxNesting = %d, want %d", got, want)
		}
	}
}

func TestMagicLiterals(t *testing.T) {
	src := `package h

const Threshold = 100

func F() int {
	if x := 0; x == 1 {
		return -1
	}
	return 42 + 100 + 7
}
`
	_, paths := writeFixtureFiles(t, []string{"x.go"}, []string{src})
	file, _, err := parseGoFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	constLits := collectConstLiterals(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		// 0, 1, -1 trivial; 100 in const block; 42 and 7 are magic ints (weight 1.0).
		got := magicLiterals(fn.Body, constLits)
		want := 2.0
		if diff := got - want; diff > 0.001 || diff < -0.001 {
			t.Errorf("magicLiterals = %.3f, want %.3f", got, want)
		}
	}
}
