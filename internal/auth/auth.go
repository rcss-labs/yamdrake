// Package auth holds the identity of the caller (Principal), the permission
// rule that decides what a Principal may do, and password hashing.
package auth

import "github.com/rcss-labs/yamdrake/internal/store"

// Action is something a principal may be allowed to do.
type Action string

const (
	View              Action = "view"
	ViewLogs          Action = "view_logs"
	Run               Action = "run"
	Cancel            Action = "cancel"
	Approve           Action = "approve"
	Restore           Action = "restore"
	ManageJobs        Action = "manage_jobs"
	ManageConnections Action = "manage_connections"
	ManageAccess      Action = "manage_access"
	ViewAudit         Action = "view_audit"
)

// knownActions is the closed set of actions. Anything else is refused for
// everyone, admins included, so a typo can never be granted by accident.
var knownActions = map[Action]bool{
	View: true, ViewLogs: true, Run: true, Cancel: true, Approve: true, Restore: true,
	ManageJobs: true, ManageConnections: true, ManageAccess: true, ViewAudit: true,
}

// Target is what an action is aimed at. The zero value is for actions that
// have no scope (managing access, viewing the audit log).
type Target struct {
	Label       string
	Environment string
	FolderID    int64
}

// Principal is an authenticated caller together with everything needed to
// authorise it. It never carries a password hash or a credential.
type Principal struct {
	UserID int64
	Email  string
	Name   string
	Admin  bool
	Grants []store.Grant
	Via    string // "session" or "token"
}

// Can reports whether the principal may perform a on t. An unknown action is
// refused for everyone.
func (p Principal) Can(a Action, t Target) bool {
	if !knownActions[a] {
		return false
	}
	if p.Admin || a == View {
		return true
	}
	for _, g := range p.Grants {
		if g.Action == string(a) && grantCovers(g, t) {
			return true
		}
	}
	return false
}

// grantCovers reports whether every scope the grant sets matches the target.
// A scope the grant leaves empty matches anything.
func grantCovers(g store.Grant, t Target) bool {
	return (g.Label == "" || g.Label == t.Label) &&
		(g.Environment == "" || g.Environment == t.Environment) &&
		(g.FolderID == 0 || g.FolderID == t.FolderID)
}

// MayApproveRunStartedBy reports whether the principal may approve a run that
// userID started: it needs Approve on the target and, unless the principal is
// an admin, it must not be the person who started the run.
func (p Principal) MayApproveRunStartedBy(userID int64, t Target) bool {
	if !p.Can(Approve, t) {
		return false
	}
	return p.Admin || p.UserID != userID
}
