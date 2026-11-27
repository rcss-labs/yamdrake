//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
)

// The hook runs in the same transaction as the source change, so a hook that
// fails undoes the change and a hook that succeeds commits with it.
func TestSetInventorySourceWithHookCommitsOrRollsBackTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	first := InventorySource{Repo: "acme/ops", Branch: "main", Path: "inv"}
	if err := SetInventorySource(ctx, pool, first); err != nil {
		t.Fatalf("SetInventorySource: %v", err)
	}

	hookFailure := errors.New("audit refused")
	second := InventorySource{Repo: "acme/other", Branch: "main", Path: "inv"}
	err := SetInventorySourceWith(ctx, pool, second, func(context.Context, DBTX) error { return hookFailure })
	if !errors.Is(err, hookFailure) {
		t.Fatalf("SetInventorySourceWith with a failing hook = %v, want the hook's error", err)
	}
	if got, _, _ := GetInventorySource(ctx, pool); got != first {
		t.Fatalf("source after a failed hook = %+v, want it unchanged (%+v)", got, first)
	}

	err = SetInventorySourceWith(ctx, pool, second, func(ctx context.Context, tx DBTX) error {
		return InsertAudit(ctx, tx, AuditEntry{At: nanosecondNow(), Actor: "system", Action: "inventory.source_set", TargetType: "inventory_source", TargetID: "1"})
	})
	if err != nil {
		t.Fatalf("SetInventorySourceWith: %v", err)
	}
	if got, _, _ := GetInventorySource(ctx, pool); got != second {
		t.Fatalf("source after a good hook = %+v, want %+v", got, second)
	}
	entries, err := ListAudit(ctx, pool, 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "inventory.source_set" {
		t.Fatalf("audit entries = %+v, %v; want the one written by the hook", entries, err)
	}
}
