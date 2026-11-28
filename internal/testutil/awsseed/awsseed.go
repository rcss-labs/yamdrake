// Package awsseed connects AWS accounts in a test database, for tests that
// create runs: the engine refuses runs whose accounts are not connected.
package awsseed

import (
	"context"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/store"
)

// Connect adds a connected AWS account (role method, no secrets) for each
// account id that is not connected yet.
func Connect(t testing.TB, db store.DBTX, accountIDs ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range accountIDs {
		if _, err := store.AWSAccountByAccountID(ctx, db, id); err == nil {
			continue
		}
		role := store.AWSCredential{Method: "role", RoleARN: "arn:aws:iam::" + id + ":role/yumdrake-test"}
		if _, err := store.CreateAWSAccount(ctx, db, store.AWSAccount{Name: "test-" + id, AccountID: id, Discovery: role, Deploy: role}, nil, nil); err != nil {
			t.Fatalf("connect AWS account %s: %v", id, err)
		}
	}
}
