package manifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEnvKeyDiff(t *testing.T) {
	cases := []struct {
		name           string
		previous, next string
		want           KeyDiff
	}{
		{"added", "A=1\n", "A=1\nB=2\n", KeyDiff{Added: []string{"B"}}},
		{"removed", "A=1\nB=2\n", "A=1\n", KeyDiff{Removed: []string{"B"}}},
		{"changed", "A=1\n", "A=2\n", KeyDiff{Changed: []string{"A"}}},
		{"unchanged", "A=1\nB=2\n", "B=2\nA=1\n", KeyDiff{}},
		{"comments and blanks ignored", "# c\n\nA=1\n", "A=1\n   # other\n\n", KeyDiff{}},
		{"commented key is not a key", "#A=1\n", "A=1\n", KeyDiff{Added: []string{"A"}}},
		{"export prefix", "export A=1\n", "A=1\nexport B=2\n", KeyDiff{Added: []string{"B"}}},
		{"export change", "export A=1\n", "export A=2\n", KeyDiff{Changed: []string{"A"}}},
		{"duplicate last wins", "A=1\nA=2\n", "A=2\n", KeyDiff{}},
		{"duplicate last wins changed", "A=2\nA=1\n", "A=2\n", KeyDiff{Changed: []string{"A"}}},
		{"crlf", "A=1\r\nB=2\r\n", "A=1\nB=2\n", KeyDiff{}},
		{"value containing equals", "URL=a=b\n", "URL=a=c\n", KeyDiff{Changed: []string{"URL"}}},
		{"value containing equals unchanged", "URL=a=b\n", "URL=a=b\n", KeyDiff{}},
		{"quotes not interpreted", "A=\"1\"\n", "A=1\n", KeyDiff{Changed: []string{"A"}}},
		{"unreadable lines ignored", "garbage\n=novalue\n", "also garbage\n", KeyDiff{}},
		{"words before equals are not a key", "A=1\n", "A=1\ntwo words=x\nmy password is hunter2=x\n", KeyDiff{}},
		{"base64 padding line is not a key", "", "abc/def+gh==\nA=1\n", KeyDiff{Added: []string{"A"}}},
		{"dotted and underscored keys are keys", "", "_A.b-not=1\nA_1.b=2\n", KeyDiff{Added: []string{"A_1.b"}}},
		{"key starting with digit is not a key", "", "1A=x\n", KeyDiff{}},
		{"empty value is a value", "A=\n", "A=x\n", KeyDiff{Changed: []string{"A"}}},
		{"sorted output", "", "Z=1\nB=1\nM=1\n", KeyDiff{Added: []string{"B", "M", "Z"}}},
		{"empty inputs", "", "", KeyDiff{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EnvKeyDiff([]byte(tc.previous), []byte(tc.next))
			if !reflect.DeepEqual(norm(got), norm(tc.want)) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// norm turns nil and empty slices into one form so tests compare names only.
func norm(d KeyDiff) KeyDiff {
	fix := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	return KeyDiff{Added: fix(d.Added), Removed: fix(d.Removed), Changed: fix(d.Changed)}
}

func TestEnvKeyDiffNeverContainsValues(t *testing.T) {
	values := []string{"hunter2-leak", "cGFkZGVk/pad+value==", "hunter2-old", "s3cr3t-new", "tok=en/with=equals", "added-secret-value", "removed-secret-value"}
	previous := "PASSWORD=hunter2-old\nTOKEN=tok=en/with=equals\nGONE=removed-secret-value\nSAME=tok=en/with=equals\n"
	next := "PASSWORD=s3cr3t-new\nTOKEN=tok=en/with=equals\nFRESH=added-secret-value\nSAME=tok=en/with=equals\nmy password is hunter2-leak=x\ncGFkZGVk/pad+value==\n"

	got := EnvKeyDiff([]byte(previous), []byte(next))
	if len(got.Added) != 1 || len(got.Removed) != 1 || len(got.Changed) != 1 {
		t.Fatalf("fixture did not exercise all three lists: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if strings.Contains(string(raw), v) {
			t.Fatalf("diff JSON contains a value: %q", v)
		}
	}
}
