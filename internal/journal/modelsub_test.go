package journal

import "testing"

func TestModelSubstitution_RoundTrip(t *testing.T) {
	p := ModelSubstitutionPayload{
		Role:   "code",
		Old:    "qwen/qwen3-coder:free",
		New:    "cohere/north-mini-code:free",
		Reason: "next curated pick still served",
	}
	e, err := NewModelSubstitutionEntry(p)
	if err != nil {
		t.Fatalf("NewModelSubstitutionEntry: %v", err)
	}
	if e.Type != TypeModelSubstitution {
		t.Errorf("Type = %s, want %s", e.Type, TypeModelSubstitution)
	}
	got, err := ParseModelSubstitution(e)
	if err != nil {
		t.Fatalf("ParseModelSubstitution: %v", err)
	}
	if *got != p {
		t.Errorf("round trip = %+v, want %+v", *got, p)
	}
}

func TestModelSubstitution_RequiresRoleOldNew(t *testing.T) {
	cases := []ModelSubstitutionPayload{
		{Old: "a", New: "b"},     // missing Role
		{Role: "code", New: "b"}, // missing Old
		{Role: "code", Old: "a"}, // missing New
	}
	for _, p := range cases {
		if _, err := NewModelSubstitutionEntry(p); err == nil {
			t.Errorf("NewModelSubstitutionEntry(%+v) = nil error, want an error", p)
		}
	}
}

func TestParseModelSubstitution_RejectsWrongType(t *testing.T) {
	e := &Entry{Type: "capture.event", V: 1, Payload: []byte(`{}`)}
	if _, err := ParseModelSubstitution(e); err == nil {
		t.Error("expected error parsing capture.event as model.substitution")
	}
}
