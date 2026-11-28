package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/spec"
	"github.com/rcss-labs/yumdrake/internal/store"
)

func TestVersionEntries(t *testing.T) {
	job := spec.Job{Groups: []spec.Group{
		{ID: "backend", Label: "Backend services", Services: []string{"a", "b", "c", "d"}},
		{ID: "micro", Label: "Microservices", Services: []string{"m1", "m2"}},
	}}
	run := func(sel Selection) store.Run {
		snapshot, _ := json.Marshal(job)
		chosen, _ := json.Marshal(sel)
		return store.Run{JobSnapshot: snapshot, Selection: chosen}
	}
	cases := []struct {
		name string
		sel  Selection
		want []VersionEntry
	}{
		{"two groups in the job's order", Selection{
			"micro":   {Version: "1.4.2", Services: map[string]string{"m2": "", "m1": ""}},
			"backend": {Version: "2.31.0", Services: map[string]string{"b": "", "a": ""}},
		}, []VersionEntry{
			{Service: "a", Version: "2.31.0"}, {Service: "b", Version: "2.31.0"},
			{Service: "m1", Version: "1.4.2"}, {Service: "m2", Version: "1.4.2"},
		}},
		{"a group nobody selected is skipped", Selection{
			"micro": {Version: "1.4.2", Services: map[string]string{"m1": ""}},
		}, []VersionEntry{{Service: "m1", Version: "1.4.2"}}},
		{"a service's own override wins over its group's version", Selection{
			"backend": {Version: "2.31.0", Services: map[string]string{"a": "2.30.1", "b": ""}},
		}, []VersionEntry{{Service: "a", Version: "2.30.1"}, {Service: "b", Version: "2.31.0"}}},
		{"nothing selected is an empty list", Selection{}, []VersionEntry{}},
	}
	for _, c := range cases {
		got := VersionEntries(run(c.sel))
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: VersionEntries = %+v, want %+v (non-nil)", c.name, got, c.want)
		}
	}
	if got := VersionEntries(store.Run{JobSnapshot: []byte("{not json")}); got == nil || len(got) != 0 {
		t.Errorf("VersionEntries of an unreadable snapshot = %+v, want an empty non-nil list", got)
	}
}

func TestVersionsSummary(t *testing.T) {
	job := spec.Job{Groups: []spec.Group{
		{ID: "backend", Label: "Backend services", Services: []string{"a", "b", "c", "d"}},
		{ID: "micro", Label: "Microservices", Services: []string{"m1", "m2"}},
		{ID: "bare", Services: []string{"x"}},
	}}
	run := func(sel Selection) store.Run {
		snapshot, _ := json.Marshal(job)
		chosen, _ := json.Marshal(sel)
		return store.Run{JobSnapshot: snapshot, Selection: chosen}
	}
	cases := []struct {
		name string
		sel  Selection
		want string
	}{
		{"two groups in the job's order", Selection{
			"micro":   {Version: "1.4.2", Services: map[string]string{"m1": "", "m2": ""}},
			"backend": {Version: "2.31.0", Services: map[string]string{"a": "", "b": "", "c": "", "d": ""}},
		}, "Backend services 2.31.0 (4), Microservices 1.4.2 (2)"},
		{"a group nobody selected is left out", Selection{
			"micro": {Version: "1.4.2", Services: map[string]string{"m1": ""}},
		}, "Microservices 1.4.2 (1)"},
		{"a group without a label shows its id", Selection{
			"bare": {Version: "9", Services: map[string]string{"x": ""}},
		}, "bare 9 (1)"},
		{"nothing selected", Selection{}, ""},
	}
	for _, c := range cases {
		if got := VersionsSummary(run(c.sel)); got != c.want {
			t.Errorf("%s: VersionsSummary = %q, want %q", c.name, got, c.want)
		}
	}
	if got := VersionsSummary(store.Run{}); got != "" {
		t.Errorf("VersionsSummary of a run with no snapshot = %q, want empty", got)
	}
}
