// Package audit writes entries to the append-only audit log.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// Event describes one change. Detail must never hold secrets.
type Event struct {
	Action, TargetType, TargetID string
	Detail                       map[string]any
}

// secretKeys are names that must never appear in Detail, at any depth, in any
// letter case. The check is a guard against mistakes, not a sanitiser.
var secretKeys = map[string]bool{
	"password": true, "token": true, "secret": true,
	"private_key": true, "hash": true, "assertion": true,
}

// Record appends e. A nil actor means the system itself. It refuses (and
// writes nothing) when Detail has a secret-looking key; the error names the
// key, never its value. Call it with the transaction that makes the change so
// both commit or neither does.
func Record(ctx context.Context, db store.DBTX, actor *auth.Principal, at time.Time, e Event) error {
	entry := store.AuditEntry{
		At: at, Actor: "system",
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
	}
	if actor != nil {
		id := actor.UserID
		entry.ActorID = &id
		entry.Actor = actor.Email
	}
	if len(e.Detail) > 0 {
		// Walk what will actually be stored, so any Go type that encodes to a
		// nested object (maps, slices, structs) is covered.
		encoded, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return fmt.Errorf("audit detail: %w", err)
		}
		if key, found := findSecretKey(decoded); found {
			return fmt.Errorf("audit detail has the key %q, which may hold a secret; refusing to record it", key)
		}
		entry.Detail = encoded
	}
	return store.InsertAudit(ctx, db, entry)
}

func findSecretKey(v any) (string, bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if secretKeys[strings.ToLower(k)] {
				return strings.ToLower(k), true
			}
			if key, found := findSecretKey(child); found {
				return key, true
			}
		}
	case []any:
		for _, child := range x {
			if key, found := findSecretKey(child); found {
				return key, true
			}
		}
	}
	return "", false
}
