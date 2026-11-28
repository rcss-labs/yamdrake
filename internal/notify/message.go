// Package notify words Slack messages for run events.
package notify

import (
	"fmt"
	"strings"

	"github.com/rcss-labs/yamdrake/internal/store"
)

// Event kinds a message can be worded for.
const (
	KindApprovalRequested = "approval_requested"
	KindApproved          = "approved"
	KindRejected          = "rejected"
	KindApprovalExpired   = "approval_expired"
	KindStarted           = "started"
	KindFinished          = "finished"
)

const (
	maxListedFailures = 10
	maxShortRunes     = 200
)

// Event is everything needed to word one message.
type Event struct {
	Kind      string
	Run       store.Run
	JobTitle  string
	Versions  string // "Backend services 2.31.0 (4), Microservices 1.4.2 (20)"
	Requester string // who asked for the run
	Actor     string // who decided, for approved and rejected
	// Updated and Total count services, for finished.
	Updated, Total int
	Failures       []Failure // for finished
}

// Failure is one failed service with a short reason (never a raw error body).
type Failure struct{ Service, Step, Short string }

// Text returns the message for e: its wording, a newline, then the run's link.
func Text(e Event, publicURL string) string {
	r := e.Run
	var body string
	switch e.Kind {
	case KindApprovalRequested:
		body = fmt.Sprintf("%s wants to run \"%s\" in %s (%s): %s. Review and approve:", e.Requester, e.JobTitle, r.Environment, r.Label, e.Versions)
	case KindApproved:
		body = fmt.Sprintf("Approved by %s: \"%s\" in %s.", e.Actor, e.JobTitle, r.Environment)
	case KindRejected:
		body = fmt.Sprintf("Rejected by %s: \"%s\" in %s.", e.Actor, e.JobTitle, r.Environment)
	case KindApprovalExpired:
		body = fmt.Sprintf("The approval request for \"%s\" in %s expired.", e.JobTitle, r.Environment)
	case KindStarted:
		body = fmt.Sprintf("Started: \"%s\" in %s by %s.", e.JobTitle, r.Environment, e.Requester)
	case KindFinished:
		body = fmt.Sprintf("%s: \"%s\" in %s. %d of %d services updated.", outcome(r.State), e.JobTitle, r.Environment, e.Updated, e.Total)
		if len(e.Failures) > 0 {
			body += "\n" + failureLine(e.Failures)
		}
	default:
		body = fmt.Sprintf("Run %d: %s", r.ID, e.Kind)
	}
	return fmt.Sprintf("%s\n%s/runs/%d", body, strings.TrimRight(publicURL, "/"), r.ID)
}

func outcome(state string) string {
	switch state {
	case "succeeded":
		return "Succeeded"
	case "partially_succeeded":
		return "Partly succeeded"
	case "failed":
		return "Failed"
	case "cancelled":
		return "Cancelled"
	case "needs_reconciliation":
		return "Needs reconciliation"
	}
	return fmt.Sprintf("Ended (%s)", state)
}

func failureLine(failures []Failure) string {
	listed := failures
	if len(listed) > maxListedFailures {
		listed = listed[:maxListedFailures]
	}
	parts := make([]string, 0, len(listed))
	for _, f := range listed {
		if f.Short == "" {
			parts = append(parts, f.Service)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", f.Service, truncateRunes(f.Short, maxShortRunes)))
	}
	line := "Failed: " + strings.Join(parts, ", ")
	if more := len(failures) - len(listed); more > 0 {
		line += fmt.Sprintf(", and %d more", more)
	}
	return line
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
