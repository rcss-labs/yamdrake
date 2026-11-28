package api

import "testing"

func TestTruncateNeverSplitsACharacter(t *testing.T) {
	if got := truncate("ééé", 3); got != "é" { // each é is two bytes
		t.Errorf("truncate(ééé, 3) = %q, want a whole é", got)
	}
	if got := truncate("abc", 5); got != "abc" {
		t.Errorf("truncate(abc, 5) = %q, want abc", got)
	}
}

func TestHasControlText(t *testing.T) {
	for text, want := range map[string]bool{
		"plain": false, "ünïcode ✓": false,
		"a\x00b": true, "a\nb": true, "a\tb": true, "del\x7f": true, "c1\u0085": true, "bad\xffutf8": true,
	} {
		if got := hasControlText(text, false); got != want {
			t.Errorf("hasControlText(%q, false) = %v, want %v", text, got, want)
		}
	}
	if hasControlText("line\n\tbreak\r\n", true) {
		t.Error("whitespace controls are refused in a multi-line field")
	}
	if !hasControlText("nul\x00", true) {
		t.Error("NUL is accepted in a multi-line field")
	}
}

type sampleBody struct {
	Name     string   `json:"name"`
	Nested   *nested  `json:"nested"`
	Tags     []string `json:"tags"`
	Password string   `json:"password" text:"any"`
	Doc      string   `json:"doc" text:"multiline"`
}

type nested struct {
	Label string `json:"label"`
}

func TestControlProblemWalksTheWholeBody(t *testing.T) {
	if p := controlProblem(&sampleBody{Name: "ok", Password: "p\x00w", Doc: "a\nb"}); p != "" {
		t.Errorf("password and multi-line fields are exempt, got %q", p)
	}
	for name, body := range map[string]*sampleBody{
		"top level":  {Name: "a\x00"},
		"nested":     {Nested: &nested{Label: "a\x00"}},
		"in a slice": {Tags: []string{"fine", "a\x00"}},
		"multi-line": {Doc: "a\x00"},
	} {
		if p := controlProblem(body); p == "" {
			t.Errorf("%s: a NUL was not found", name)
		}
	}
}

func TestCleanDisplayNameDropsWhatTheDatabaseRefuses(t *testing.T) {
	if got := cleanDisplayName("Ada\x00 Love\nlace\xff"); got != "Ada Lovelace" {
		t.Errorf("cleanDisplayName = %q, want Ada Lovelace", got)
	}
}
