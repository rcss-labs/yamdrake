package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/rcss-labs/yamdrake/internal/github"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const environmentsFile = "environments.yaml"

// SyncInventory reads the inventory source and activates its head commit when
// every file at that commit is valid. Any problem leaves the active snapshot
// alone and records the problems with the rejected commit.
func (e *Engine) SyncInventory(ctx context.Context) error {
	return e.withLock(ctx, e.syncInventory)
}

// SetInventorySource stores the source and syncs it while holding the lock, so
// a poll that read the old source cannot record its outcome after the change.
// A failed sync is returned, but the new source stays stored. inTx, when not
// nil, runs in the transaction that stores the source (the API writes its
// audit entry there); if it fails nothing is stored and nothing is synced.
func (e *Engine) SetInventorySource(ctx context.Context, source store.InventorySource, inTx func(context.Context, store.DBTX) error) error {
	return e.withLock(ctx, func(ctx context.Context) error {
		if err := store.SetInventorySourceWith(ctx, e.pool, source, inTx); err != nil {
			return err
		}
		return e.syncInventory(ctx)
	})
}

// syncInventory is SyncInventory without the lock; the caller must hold it.
func (e *Engine) syncInventory(ctx context.Context) error {
	src, configured, err := store.GetInventorySource(ctx, e.pool)
	if err != nil {
		return err
	}
	if !configured {
		return nil
	}
	fail := func(what string, err error, attrs ...any) error {
		e.recordSyncError(ctx, what, err, func(message string, at time.Time) error {
			return store.SetInventorySyncError(ctx, e.pool, message, at)
		})
		return e.sourceError(what, err, append(attrs, "repo", src.Repo)...)
	}
	// SourceFor errors are stored and shown to users as they are: the
	// implementation must return credential-free errors.
	source, err := e.sourceForRepo(ctx, src.Repo)
	if err != nil {
		return fail("inventory source", err)
	}
	head, err := source.HeadCommit(ctx, src.Repo, src.Branch)
	if err != nil {
		return fail("read inventory branch head", err, "branch", src.Branch)
	}
	if err := store.ClearInventorySyncError(ctx, e.pool); err != nil {
		return err
	}

	status, err := store.GetInventoryStatus(ctx, e.pool)
	if err != nil {
		return err
	}
	active, haveActive, err := store.ActiveSnapshot(ctx, e.pool)
	if err != nil {
		return err
	}
	// A nil CheckedAt means the source changed since the last check (the store
	// resets the status then), so the same head must be read again.
	sourceChecked := status.CheckedAt != nil
	if haveActive && active.Commit == head && sourceChecked {
		// The head is the valid active commit; stale problems from a rejected
		// commit that was since reverted no longer apply.
		at := e.now()
		return store.SaveInventoryStatus(ctx, e.pool, store.InventoryStatus{CheckedAt: &at})
	}
	if status.Problems != nil && status.ProblemsCommit == head {
		// Already rejected; a commit's content never changes.
		at := e.now()
		status.CheckedAt = &at
		return store.SaveInventoryStatus(ctx, e.pool, status)
	}

	inventory, problems, err := e.readInventory(ctx, source, src, head, fail)
	if err != nil {
		return err
	}
	at := e.now() // taken after the reads, just before the write
	if len(problems) > 0 {
		encoded, err := json.Marshal(problems.Sorted())
		if err != nil {
			return fmt.Errorf("encode problems: %w", err)
		}
		e.log.Info("inventory commit rejected", "commit", head, "problems", len(problems))
		return store.SaveInventoryStatus(ctx, e.pool, store.InventoryStatus{CheckedAt: &at, Problems: encoded, ProblemsCommit: head})
	}

	encoded, err := json.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("encode inventory: %w", err)
	}
	// The same commit read through a changed source can differ from the active
	// snapshot; replace it then, and do nothing when the content is the same.
	replacing := haveActive && active.Commit == head
	if replacing && sameJSON(active.Inventory, encoded) {
		return store.SaveInventoryStatus(ctx, e.pool, store.InventoryStatus{CheckedAt: &at})
	}
	// One transaction: the new snapshot, the jobs re-validated against it and
	// the cleared status become visible together. The status row lock taken by
	// ActivateSnapshot is held until Commit, and every statement uses this one
	// connection, so nothing here can wait on itself.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin activation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if replacing {
		if err := store.DeactivateActiveSnapshot(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := store.ActivateSnapshot(ctx, tx, head, encoded, at); err != nil {
		return err
	}
	if err := e.revalidateJobs(ctx, tx, inventory, at); err != nil {
		return err
	}
	if err := store.SaveInventoryStatus(ctx, tx, store.InventoryStatus{CheckedAt: &at}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit activation: %w", err)
	}
	e.log.Info("inventory activated", "commit", head)
	return nil
}

// sameJSON reports whether two JSON documents have the same content,
// regardless of formatting (PostgreSQL normalises jsonb).
func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// readInventory reads and checks every inventory file at commit. The error is
// a source error; problems are definitions errors. File names in problems are
// relative to the inventory path (for example services/orders.yaml).
func (e *Engine) readInventory(ctx context.Context, source Source, src store.InventorySource, commit string,
	fail func(what string, err error, attrs ...any) error) (spec.Inventory, spec.Problems, error) {
	dir := strings.Trim(strings.TrimSpace(src.Path), "/")
	if dir == "." {
		dir = ""
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	files, err := source.ListFiles(ctx, src.Repo, commit, dir)
	if errors.Is(err, github.ErrNotFound) && dir != "" {
		// The commit was just read from the branch head, so a missing
		// directory is a wrong path: reported below as a missing
		// environments.yaml, not as a source failure.
		files, err = nil, nil
	}
	if err != nil {
		return spec.Inventory{}, nil, fail("list inventory files", err, "commit", commit)
	}

	var problems spec.Problems
	var haveEnvironments bool
	var serviceFiles []string
	for _, file := range files {
		relative, ok := strings.CutPrefix(file, prefix)
		if !ok {
			continue
		}
		switch {
		case relative == environmentsFile:
			haveEnvironments = true
		case strings.HasPrefix(relative, "services/") && strings.HasSuffix(relative, ".yml") &&
			!strings.Contains(strings.TrimPrefix(relative, "services/"), "/"):
			problems = append(problems, spec.YmlExtensionProblem(relative))
		case strings.HasPrefix(relative, "services/") && strings.HasSuffix(relative, ".yaml") &&
			!strings.Contains(strings.TrimPrefix(relative, "services/"), "/"):
			serviceFiles = append(serviceFiles, relative)
		}
	}

	var environments []spec.Environment
	if haveEnvironments {
		data, err := source.ReadFile(ctx, src.Repo, commit, prefix+environmentsFile)
		if err != nil {
			return spec.Inventory{}, nil, fail("read inventory file", err, "commit", commit, "file", environmentsFile)
		}
		var found spec.Problems
		environments, found = spec.ParseEnvironments(environmentsFile, data)
		problems = append(problems, found...)
	} else {
		problems = append(problems, spec.Problem{File: environmentsFile,
			Message: fmt.Sprintf("%s is required but was not found at commit %s", environmentsFile, commit)})
	}

	var services []spec.Service
	for _, relative := range serviceFiles {
		data, err := source.ReadFile(ctx, src.Repo, commit, prefix+relative)
		if err != nil {
			return spec.Inventory{}, nil, fail("read inventory file", err, "commit", commit, "file", relative)
		}
		service, found := spec.ParseService(relative, data)
		problems = append(problems, found...)
		services = append(services, service)
	}

	// Cross-file rules only make sense when every file parsed.
	if len(problems) == 0 {
		var cross spec.Problems
		var inventory spec.Inventory
		inventory, cross = spec.BuildInventory(environmentsFile, environments, services)
		return inventory, cross, nil
	}
	return spec.Inventory{}, problems.Sorted(), nil
}
