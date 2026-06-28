package outline

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// goUnits returns a Go file's top-level declarations in file order via go/ast:
// funcs (methods carry their receiver), types, and const/var blocks. A parse
// error yields nil (the file is listed but not expanded). EndLine spans let the
// model read_file exactly the declaration.
func goUnits(src []byte) []unit {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var units []unit
	for _, decl := range f.Decls {
		switch v := decl.(type) {
		case *ast.FuncDecl:
			name := "func " + v.Name.Name
			if v.Recv != nil && len(v.Recv.List) > 0 {
				name = "func (" + recvType(v.Recv.List[0].Type) + ") " + v.Name.Name
			}
			units = append(units, unit{label: name, start: fset.Position(v.Pos()).Line, end: fset.Position(v.End()).Line})
		case *ast.GenDecl:
			switch v.Tok {
			case token.TYPE:
				for _, spec := range v.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						units = append(units, unit{label: "type " + ts.Name.Name, start: fset.Position(ts.Pos()).Line, end: fset.Position(ts.End()).Line})
					}
				}
			case token.CONST, token.VAR:
				if name := valueNames(v); name != "" {
					units = append(units, unit{label: v.Tok.String() + " " + name, start: fset.Position(v.Pos()).Line, end: fset.Position(v.End()).Line})
				}
			}
		}
	}
	return units
}

// valueNames compacts a const/var block to its first declared name plus a "+N"
// count of the rest. Blank identifiers are ignored.
func valueNames(g *ast.GenDecl) string {
	var names []string
	for _, s := range g.Specs {
		if vs, ok := s.(*ast.ValueSpec); ok {
			for _, n := range vs.Names {
				if n.Name != "_" {
					names = append(names, n.Name)
				}
			}
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return fmt.Sprintf("%s +%d", names[0], len(names)-1)
	}
}

// recvType renders a method receiver type, including pointer and generic forms.
func recvType(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return "*" + recvType(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr:
		return recvType(v.X)
	case *ast.IndexListExpr:
		return recvType(v.X)
	}
	return "?"
}

// declRe maps a language family to "this line starts a coherence unit" — coarse,
// column-0 patterns. Precision isn't critical: a section just needs to begin at a
// plausible unit start. Go is absent (it uses the AST path).
var declRe = map[string]*regexp.Regexp{
	"py":   regexp.MustCompile(`^(def |class |async def |@\w)`),
	"js":   regexp.MustCompile(`^(export |function|class|const|let|var|async function)\b`),
	"ts":   regexp.MustCompile(`^(export |function|class|const|let|var|interface|type|enum|async function)\b`),
	"rs":   regexp.MustCompile(`^(pub|fn|struct|enum|impl|trait|mod|macro_rules!)\b`),
	"java": regexp.MustCompile(`^\s*(public|private|protected|static|class|interface|enum|@)\w`),
	"c":    regexp.MustCompile(`^[A-Za-z_].*[A-Za-z0-9_]\s*\(`),
	"rb":   regexp.MustCompile(`^(def |class |module )`),
	"sh":   regexp.MustCompile(`^(function\b|\w+\s*\(\)\s*\{)`),
	"sql":  regexp.MustCompile(`(?i)^(create|alter|insert|drop)\b`),
	"tf":   regexp.MustCompile(`^(resource|module|variable|output|provider|data|locals|terraform)\b`),
	"md":   regexp.MustCompile(`^#{1,6}\s`),
	"toml": regexp.MustCompile(`^\[`),
	"ini":  regexp.MustCompile(`^\[`),
	"yaml": regexp.MustCompile(`^[A-Za-z_][\w-]*:`),
}

// regexUnits sections a non-Go file: a declaration/heading regex when the format
// has one, else prose paragraph blocks, else a positional line table — never no
// outline for a file past the small-file floor.
func regexUnits(path string, lines []string) []unit {
	lang := fileLang(path)
	if re := declRe[lang]; re != nil {
		if u := scanBoundaries(lines, re); len(u) >= 2 {
			return u
		}
	}
	if isProse(lang) {
		if u := scanParagraphs(lines); len(u) >= 2 {
			return u
		}
	}
	return positional(len(lines))
}

const (
	maxSections = 80
	nameCap     = 60
	posWindow   = 60
)

// scanBoundaries turns every line matching re into a section running to the next
// match (or EOF).
func scanBoundaries(lines []string, re *regexp.Regexp) []unit {
	var starts []int
	for i, ln := range lines {
		if re.MatchString(ln) {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	var units []unit
	for i, s := range starts {
		end := len(lines) - 1
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		units = append(units, unit{label: label(lines[s]), start: s + 1, end: end + 1})
	}
	return capUnits(units)
}

// scanParagraphs sections prose at blank-line boundaries.
func scanParagraphs(lines []string) []unit {
	var starts []int
	prevBlank := true
	for i, ln := range lines {
		blank := strings.TrimSpace(ln) == ""
		if !blank && prevBlank {
			starts = append(starts, i)
		}
		prevBlank = blank
	}
	if len(starts) < 2 {
		return nil
	}
	var units []unit
	for i, s := range starts {
		end := len(lines) - 1
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		units = append(units, unit{label: label(lines[s]), start: s + 1, end: end + 1})
	}
	return capUnits(units)
}

// positional divides a file into equal line windows — the floor for content with
// no internal structure.
func positional(total int) []unit {
	window := posWindow
	if n := total / maxSections; n > window {
		window = n
	}
	var units []unit
	for start := 1; start <= total; start += window {
		end := start + window - 1
		if end > total {
			end = total
		}
		units = append(units, unit{label: fmt.Sprintf("lines %d-%d", start, end), start: start, end: end})
	}
	return units
}

// capUnits keeps an outline under maxSections by coalescing adjacent sections.
func capUnits(units []unit) []unit {
	if len(units) <= maxSections {
		return units
	}
	group := (len(units) + maxSections - 1) / maxSections
	var out []unit
	for i := 0; i < len(units); i += group {
		j := i + group - 1
		if j >= len(units) {
			j = len(units) - 1
		}
		u := units[i]
		u.end = units[j].end
		out = append(out, u)
	}
	return out
}

func label(line string) string {
	s := strings.TrimSpace(line)
	if len(s) > nameCap {
		s = s[:nameCap] + "…"
	}
	return s
}

func splitLines(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

func isProse(lang string) bool {
	switch lang {
	case "md", "txt", "":
		return true
	}
	return false
}

// fileLang maps a file extension to the outline language key.
func fileLang(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "py":
		return "py"
	case "js", "jsx", "mjs", "cjs":
		return "js"
	case "ts", "tsx":
		return "ts"
	case "rs":
		return "rs"
	case "java":
		return "java"
	case "c", "cc", "cpp", "cxx", "h", "hpp", "hxx":
		return "c"
	case "rb":
		return "rb"
	case "sh", "bash", "zsh":
		return "sh"
	case "sql":
		return "sql"
	case "tf":
		return "tf"
	case "md", "markdown":
		return "md"
	case "txt":
		return "txt"
	case "toml":
		return "toml"
	case "ini", "conf", "cfg":
		return "ini"
	case "yaml", "yml":
		return "yaml"
	}
	return ""
}
