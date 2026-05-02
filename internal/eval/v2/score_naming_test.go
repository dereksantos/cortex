package eval

import (
	"testing"
)

func TestNamingAdherence_FullMatch(t *testing.T) {
	s1 := `package h

func ListBooks() {}
func GetBook() {}
func CreateBook() {}
func UpdateBook() {}
func DeleteBook() {}
`
	s2 := `package h

func ListAuthors() {}
func GetAuthor() {}
func CreateAuthor() {}
func UpdateAuthor() {}
func DeleteAuthor() {}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books.go", "authors.go"},
		[]string{s1, s2},
	)
	got, err := namingAdherence(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("namingAdherence: %v", err)
	}
	if got != 1.0 {
		t.Errorf("identical naming patterns should score 1.0, got %.3f", got)
	}
}

func TestNamingAdherence_DivergentPattern(t *testing.T) {
	s1 := `package h

func ListBooks() {}
func GetBook() {}
func CreateBook() {}
`
	// Different prefix style ("Handle..."), different suffix order ("AuthorsList")
	s2 := `package h

func HandleListAuthors() {}
func AuthorsGet() {}
func MakeAuthor() {}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books.go", "authors.go"},
		[]string{s1, s2},
	)
	got, err := namingAdherence(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("namingAdherence: %v", err)
	}
	if got > 0.4 {
		t.Errorf("divergent naming should score ≤ 0.4, got %.3f", got)
	}
}

func TestNamingAdherence_ErrVarsCounted(t *testing.T) {
	s1 := `package h

var ErrNotFound = error(nil)
var ErrConflict = error(nil)
`
	// S2 uses lowercase + suffix style — not S1's pattern.
	s2 := `package h

var errMissingAuthor = error(nil)
var AuthorNotFoundErr = error(nil)
`
	_, paths := writeFixtureFiles(t,
		[]string{"books.go", "authors.go"},
		[]string{s1, s2},
	)
	got, err := namingAdherence(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("namingAdherence: %v", err)
	}
	if got > 0.1 {
		t.Errorf("err vars with mismatched style should score near 0, got %.3f", got)
	}
}

func TestTemplatize(t *testing.T) {
	tests := []struct {
		name   string
		tokens []string
		want   string
	}{
		{"ListBooks", []string{"books", "book"}, "List<X>"},
		{"GetBook", []string{"books", "book"}, "Get<X>"},
		{"HandleListBooks", []string{"books", "book"}, "HandleList<X>"},
		{"BooksList", []string{"books", "book"}, "<X>List"},
		{"ListAuthors", []string{"authors", "author"}, "List<X>"},
		{"ListBranches", []string{"branches", "branch"}, "List<X>"},
		{"NoResource", []string{"books", "book"}, "NoResource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := templatize(tt.name, tt.tokens)
			if got != tt.want {
				t.Errorf("templatize(%q, %v) = %q, want %q", tt.name, tt.tokens, got, tt.want)
			}
		})
	}
}

func TestResourceTokensFromPath(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"/foo/books.go", []string{"books", "book"}},
		{"/foo/authors.go", []string{"authors", "author"}},
		{"/foo/branches.go", []string{"branches", "branch"}},
		{"/foo/loans.go", []string{"loans", "loan"}},
		{"/foo/members.go", []string{"members", "member"}},
		{"/foo/books_test.go", []string{"books", "book"}},
		{"/foo/branches_test.go", []string{"branches", "branch"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := resourceTokensFromPath(tt.path)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("token[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
