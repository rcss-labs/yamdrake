package engine

import (
	"encoding/json"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

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
