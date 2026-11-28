package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

func TestApprovalExpiryReadsTheJobSnapshot(t *testing.T) {
	snapshot := func(a *spec.Approval) store.Run {
		return store.Run{JobSnapshot: mustJSON(t, spec.Job{ID: "j", Approval: a})}
	}
	cases := []struct {
		name string
		run  store.Run
		want time.Duration
	}{
		{"no approval block", snapshot(nil), 24 * time.Hour},
		{"approval without an expiry", snapshot(&spec.Approval{Labels: []string{"prod"}}), 24 * time.Hour},
		{"approval with an expiry", snapshot(&spec.Approval{Labels: []string{"prod"}, Expires: 90 * time.Minute}), 90 * time.Minute},
		{"unreadable snapshot", store.Run{JobSnapshot: []byte("not json")}, 24 * time.Hour},
	}
	for _, c := range cases {
		if got := ApprovalExpiry(c.run); got != c.want {
			t.Errorf("%s: ApprovalExpiry = %v, want %v", c.name, got, c.want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return encoded
}
