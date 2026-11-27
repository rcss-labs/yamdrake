package auth

import (
	"testing"

	"github.com/rcss-labs/yamdrake/internal/store"
)

var allActions = []Action{View, ViewLogs, Run, Cancel, Approve, Restore, ManageJobs, ManageConnections, ManageAccess, ViewAudit}

func TestCan(t *testing.T) {
	t.Parallel()
	dev := Target{Label: "dev", Environment: "hetzner-01", FolderID: 3}
	prod := Target{Label: "prod", Environment: "prod-01", FolderID: 3}
	devFolder4 := Target{Label: "dev", Environment: "hetzner-01", FolderID: 4}
	grant := func(action, label, env string, folder int64) store.Grant {
		return store.Grant{Action: action, Label: label, Environment: env, FolderID: folder}
	}

	tests := []struct {
		name      string
		principal Principal
		action    Action
		target    Target
		want      bool
	}{
		{"no grants may view", Principal{}, View, prod, true},
		{"no grants may not run", Principal{}, Run, dev, false},
		{"no grants may not view logs", Principal{}, ViewLogs, dev, false},
		{"view_logs refused without a grant", Principal{Grants: []store.Grant{grant("run", "", "", 0)}}, ViewLogs, dev, false},
		{"label grant allows matching label", Principal{Grants: []store.Grant{grant("run", "dev", "", 0)}}, Run, dev, true},
		{"label grant refuses other label", Principal{Grants: []store.Grant{grant("run", "dev", "", 0)}}, Run, prod, false},
		{"environment grant refuses other environment", Principal{Grants: []store.Grant{grant("run", "", "hetzner-02", 0)}}, Run, dev, false},
		{"environment grant allows its environment", Principal{Grants: []store.Grant{grant("run", "", "hetzner-01", 0)}}, Run, dev, true},
		{"label and folder grant refuses other folder", Principal{Grants: []store.Grant{grant("run", "dev", "", 3)}}, Run, devFolder4, false},
		{"label and folder grant allows its folder", Principal{Grants: []store.Grant{grant("run", "dev", "", 3)}}, Run, dev, true},
		{"unscoped grant allows any target", Principal{Grants: []store.Grant{grant("run", "", "", 0)}}, Run, prod, true},
		{"unscoped grant allows the zero target", Principal{Grants: []store.Grant{grant("run", "", "", 0)}}, Run, Target{}, true},
		{"run does not imply approve", Principal{Grants: []store.Grant{grant("run", "", "", 0)}}, Approve, dev, false},
		{"run does not imply cancel", Principal{Grants: []store.Grant{grant("run", "", "", 0)}}, Cancel, dev, false},
		{"a grant for another action in scope does not help", Principal{Grants: []store.Grant{grant("cancel", "dev", "", 0)}}, Run, dev, false},
		{"second grant may be the one that matches", Principal{Grants: []store.Grant{grant("run", "prod", "", 0), grant("run", "dev", "", 0)}}, Run, dev, true},
		{"scoped grant does not match the zero target", Principal{Grants: []store.Grant{grant("run", "dev", "", 0)}}, Run, Target{}, false},
		{"unknown action refused with a grant of that name", Principal{Grants: []store.Grant{grant("teleport", "", "", 0)}}, Action("teleport"), dev, false},
		{"unknown action refused for admin", Principal{Admin: true}, Action("teleport"), dev, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.principal.Can(tc.action, tc.target); got != tc.want {
				t.Fatalf("Can(%q, %+v) = %v, want %v", tc.action, tc.target, got, tc.want)
			}
		})
	}

	t.Run("admin may do every action on any target", func(t *testing.T) {
		t.Parallel()
		admin := Principal{Admin: true}
		for _, a := range allActions {
			for _, target := range []Target{{}, dev, prod, devFolder4} {
				if !admin.Can(a, target) {
					t.Fatalf("admin Can(%q, %+v) = false", a, target)
				}
			}
		}
	})
	t.Run("without grants only view is allowed", func(t *testing.T) {
		t.Parallel()
		for _, a := range allActions {
			if got, want := (Principal{}).Can(a, dev), a == View; got != want {
				t.Fatalf("Can(%q) = %v, want %v", a, got, want)
			}
		}
	})
}

func TestMayApproveRunStartedBy(t *testing.T) {
	t.Parallel()
	target := Target{Label: "prod", Environment: "prod-01", FolderID: 1}
	approver := Principal{UserID: 7, Grants: []store.Grant{{Action: "approve"}}}
	tests := []struct {
		name      string
		principal Principal
		starter   int64
		want      bool
	}{
		{"own run refused", approver, 7, false},
		{"someone else's run allowed", approver, 8, true},
		{"admin may approve own run", Principal{UserID: 7, Admin: true}, 7, true},
		{"no approve grant refused", Principal{UserID: 7, Grants: []store.Grant{{Action: "run"}}}, 8, false},
		{"approve grant scoped elsewhere refused", Principal{UserID: 7, Grants: []store.Grant{{Action: "approve", Label: "dev"}}}, 8, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.principal.MayApproveRunStartedBy(tc.starter, target); got != tc.want {
				t.Fatalf("MayApproveRunStartedBy(%d) = %v, want %v", tc.starter, got, tc.want)
			}
		})
	}
}
