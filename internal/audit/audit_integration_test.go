//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/db"
	"github.com/rcss-labs/yamdrake/internal/store"
	"github.com/rcss-labs/yamdrake/internal/testutil/testdb"
)

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := testdb.New(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func entries(t *testing.T, pool store.DBTX) []store.AuditEntry {
	t.Helper()
	list, err := store.ListAudit(context.Background(), pool, 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return list
}

func TestRecordWritesActorAndDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	u, err := store.CreateLocalUser(ctx, pool, "admin@example.com", "Admin", "local", "h")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	actor := &auth.Principal{UserID: u.ID, Email: u.Email}
	e := Event{
		Action: "grant.create", TargetType: "grant", TargetID: "7",
		Detail: map[string]any{"group": "ops", "labels": []any{"dev", "prod"}, "count": 2},
	}
	if err := Record(ctx, pool, actor, at, e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := entries(t, pool)
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	g := got[0]
	if g.ActorID == nil || *g.ActorID != u.ID || g.Actor != "admin@example.com" ||
		g.Action != "grant.create" || g.TargetType != "grant" || g.TargetID != "7" ||
		!g.At.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("entry = %+v", g)
	}
	var detail map[string]any
	if err := json.Unmarshal(g.Detail, &detail); err != nil {
		t.Fatalf("detail is not JSON: %v", err)
	}
	if detail["group"] != "ops" || detail["count"] != float64(2) || len(detail["labels"].([]any)) != 2 {
		t.Fatalf("detail = %v", detail)
	}
}

func TestRecordSystemActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	if err := Record(ctx, pool, nil, time.Now(), Event{Action: "sync.finish"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := entries(t, pool)
	if len(got) != 1 || got[0].ActorID != nil || got[0].Actor != "system" || got[0].Detail != nil {
		t.Fatalf("entries = %+v, want one system entry with no actor id and no detail", got)
	}
}

func TestRecordRefusesSecretKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newPool(t)
	cases := map[string]map[string]any{
		"password top level":         {"password": "x"},
		"token":                      {"token": "x"},
		"secret":                     {"secret": "x"},
		"private_key":                {"private_key": "x"},
		"hash":                       {"hash": "x"},
		"assertion":                  {"assertion": "x"},
		"upper case":                 {"PASSWORD": "x"},
		"mixed case":                 {"Private_Key": "x"},
		"nested map":                 {"user": map[string]any{"name": "a", "hash": "x"}},
		"nested deeper":              {"a": map[string]any{"b": map[string]any{"c": map[string]any{"Token": "x"}}}},
		"in a slice of any":          {"items": []any{map[string]any{"ok": 1}, map[string]any{"secret": "x"}}},
		"in a slice of maps":         {"items": []map[string]any{{"assertion": "x"}}},
		"slice in slice":             {"items": []any{[]any{map[string]any{"password": "x"}}}},
		"in a map of strings":        {"m": map[string]string{"token": "x"}},
		"nil value still a secret":   {"password": nil},
		"nested in nested slice map": {"a": []map[string]any{{"b": []any{map[string]any{"HASH": 1}}}}},
		"contains password":          {"new_password": "x"},
		"contains token":             {"api_token_value": "x"},
		"contains secret":            {"client_secret_ref": "x"},
		"contains private_key":       {"my_private_key_pem": "x"},
		"contains hash":              {"password_hash": "x"},
		"contains assertion":         {"saml_assertion_xml": "x"},
		"contains plaintext":         {"plaintext": "x"},
		"plaintext, upper case":      {"TokenPlainText": "x"},
		"substring deep in a slice":  {"a": []any{map[string]any{"b": map[string]any{"user_tokens": 3}}}},
	}
	for name, detail := range cases {
		err := Record(ctx, pool, nil, time.Now(), Event{Action: "x", Detail: detail})
		if err == nil {
			t.Errorf("%s: Record accepted a secret key", name)
		}
	}
	if got := entries(t, pool); len(got) != 0 {
		t.Fatalf("%d entries written despite refusals", len(got))
	}
	// Names that carry no secret word are fine.
	ok := map[string]any{"changed": true, "expires_at": "x", "group": map[string]any{"count": 1, "email_attribute": "mail"}}
	if err := Record(ctx, pool, nil, time.Now(), Event{Action: "x", Detail: ok}); err != nil {
		t.Fatalf("harmless keys refused: %v", err)
	}
}

// The refusal must not echo the value it refused.
func TestRecordRefusalDoesNotEchoValue(t *testing.T) {
	t.Parallel()
	err := Record(context.Background(), newPool(t), nil, time.Now(),
		Event{Action: "x", Detail: map[string]any{"password": "hunter2-marker"}})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "hunter2-marker") {
		t.Fatal("refusal echoes the secret value")
	}
}
