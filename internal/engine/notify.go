package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/notify"
	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// enqueueNotification queues the message of kind for a run's transition in the
// transaction that makes it. The run is read again so the message words the
// state stored now. A job without notify.slack queues nothing. Message content
// never fails a transition: what cannot be read is left out of the text; only
// database errors are returned.
func (e *Engine) enqueueNotification(ctx context.Context, tx pgx.Tx, runID int64, kind, actor string, now time.Time) error {
	run, err := store.GetRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	var job spec.Job
	if err := json.Unmarshal(run.JobSnapshot, &job); err != nil || job.SlackChannel == "" {
		return nil
	}
	event := notify.Event{
		Kind: kind, Run: run, JobTitle: job.Title, Versions: VersionsSummary(run), Actor: actor,
	}
	if user, err := store.UserByID(ctx, tx, run.RequestedBy); err == nil {
		event.Requester = user.Name
		if event.Requester == "" {
			event.Requester = user.Email
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if kind == notify.KindFinished {
		if err := e.describeOutcome(ctx, tx, &event); err != nil {
			return err
		}
	}
	return notify.Enqueue(ctx, tx, job.SlackChannel, event, e.publicURL, now)
}

// describeOutcome fills a finished event's counts and failures from the run's
// step legs: a service is updated when every one of its step legs succeeded,
// and fails at its first failed or unknown leg, with that leg's short reason.
func (e *Engine) describeOutcome(ctx context.Context, tx pgx.Tx, event *notify.Event) error {
	steps, err := store.StepsOfRun(ctx, tx, event.Run.ID)
	if err != nil {
		return err
	}
	legs, err := store.LegsOfRun(ctx, tx, event.Run.ID)
	if err != nil {
		return err
	}
	stepName := map[int64]string{}
	stepPosition := map[int64]int{}
	for _, s := range steps {
		stepName[s.ID] = s.Name
		stepPosition[s.ID] = s.Position
	}

	allSucceeded := map[string]bool{}
	firstFailure := map[string]notify.Failure{}
	failurePosition := map[string]int{}
	for _, l := range legs {
		if l.Kind != "step" || l.StepID == nil {
			continue
		}
		if _, seen := allSucceeded[l.Service]; !seen {
			allSucceeded[l.Service] = true
		}
		if l.State != legSucceeded {
			allSucceeded[l.Service] = false
		}
		if l.State != legFailed && l.State != legUnknown {
			continue
		}
		position := stepPosition[*l.StepID]
		if previous, found := failurePosition[l.Service]; found && previous <= position {
			continue
		}
		failurePosition[l.Service] = position
		firstFailure[l.Service] = notify.Failure{Service: l.Service, Step: stepName[*l.StepID], Short: l.FailureShort}
	}

	event.Total = len(allSucceeded)
	for _, ok := range allSucceeded {
		if ok {
			event.Updated++
		}
	}
	for _, f := range firstFailure {
		event.Failures = append(event.Failures, f)
	}
	sort.Slice(event.Failures, func(i, j int) bool { return event.Failures[i].Service < event.Failures[j].Service })
	return nil
}
