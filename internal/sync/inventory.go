package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const environmentsFile = "environments.yaml"

// SyncInventory reads the inventory source and activates its head commit when
// every file at that commit is valid. Any problem leaves the active snapshot
// alone and records the problems with the rejected commit.
func (e *Engine) SyncInventory(ctx context.Context) error {
	src, configured, err := store.GetInventorySource(ctx, e.pool)
	if err != nil {
		return err
	}
	if !configured {
		return nil
	}
	source, err := e.sourceForRepo(ctx, src.Repo)
	if err != nil {
		return e.sourceError("inventory source", err, "repo", src.Repo)
	}
	head, err := source.HeadCommit(ctx, src.Repo, src.Branch)
	if err != nil {
		return e.sourceError("read inventory branch head", err, "repo", src.Repo, "branch", src.Branch)
	}
	at := e.now()

	status, err := store.GetInventoryStatus(ctx, e.pool)
	if err != nil {
		return err
	}
	active, haveActive, err := store.ActiveSnapshot(ctx, e.pool)
	if err != nil {
		return err
	}
	if haveActive && active.Commit == head {
		// The head is the valid active commit; stale problems from a rejected
		// commit that was since reverted no longer apply.
		return store.SaveInventoryStatus(ctx, e.pool, store.InventoryStatus{CheckedAt: &at})
	}
	if status.Problems != nil && status.ProblemsCommit == head {
		// Already rejected; a commit's content never changes.
		status.CheckedAt = &at
		return store.SaveInventoryStatus(ctx, e.pool, status)
	}

	inventory, problems, err := e.readInventory(ctx, source, src, head)
	if err != nil {
		return err
	}
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
	// One transaction: the new snapshot, the jobs re-validated against it and
	// the cleared status become visible together. The status row lock taken by
	// ActivateSnapshot is held until Commit, and every statement uses this one
	// connection, so nothing here can wait on itself.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin activation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
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

// readInventory reads and checks every inventory file at commit. The error is
// a source error; problems are definitions errors. File names in problems are
// relative to the inventory path (for example services/orders.yaml).
func (e *Engine) readInventory(ctx context.Context, source Source, src store.InventorySource, commit string) (spec.Inventory, spec.Problems, error) {
	dir := strings.Trim(strings.TrimSpace(src.Path), "/")
	if dir == "." {
		dir = ""
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	files, err := source.ListFiles(ctx, src.Repo, commit, dir)
	if err != nil {
		return spec.Inventory{}, nil, e.sourceError("list inventory files", err, "repo", src.Repo, "commit", commit)
	}

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
		case strings.HasPrefix(relative, "services/") && strings.HasSuffix(relative, ".yaml") &&
			!strings.Contains(strings.TrimPrefix(relative, "services/"), "/"):
			serviceFiles = append(serviceFiles, relative)
		}
	}

	var problems spec.Problems
	var environments []spec.Environment
	if haveEnvironments {
		data, err := source.ReadFile(ctx, src.Repo, commit, prefix+environmentsFile)
		if err != nil {
			return spec.Inventory{}, nil, e.sourceError("read inventory file", err, "repo", src.Repo, "commit", commit, "file", environmentsFile)
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
			return spec.Inventory{}, nil, e.sourceError("read inventory file", err, "repo", src.Repo, "commit", commit, "file", relative)
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
