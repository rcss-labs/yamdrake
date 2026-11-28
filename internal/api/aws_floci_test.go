//go:build integration && floci

package api

import (
	"net/http"
	"testing"

	"github.com/rcss-labs/yamdrake/test/awstest"
)

// The connection test calls GetCallerIdentity with the discovery credential and
// compares the account it answers with the account the row says.
func TestTestConnectionReportsAccountMismatch(t *testing.T) {
	h := newHarness(t)
	world := awstest.New(t)
	home := world.Account(awstest.RandomAccountID())  // the account the orchestrator itself runs in
	keyed := world.Account(awstest.RandomAccountID()) // an account whose access key is submitted
	other := world.Account(awstest.RandomAccountID()) // an account the rows wrongly claim
	other2 := world.Account(awstest.RandomAccountID())
	h.rebuild(func(d *Deps) { d.AWSConfig = home.AWSConfig("us-east-1") })

	roleARN := home.Role("yumdrake-discovery-" + awstest.RandomSuffix())
	deployRole := map[string]any{"method": "role", "role_arn": roleARN}
	create := func(name, accountID string, discovery map[string]any) string {
		created := h.mustDo("POST", "/api/v1/admin/aws-accounts", map[string]any{
			"name": name, "account_id": accountID, "discovery": discovery, "deploy": deployRole,
		}, http.StatusCreated)
		return "/api/v1/admin/aws-accounts/" + itoa(int64(created["id"].(float64))) + "/test"
	}
	check := func(what, path string, wantOK bool, wantCode string) {
		t.Helper()
		got := h.mustDo("POST", path, nil, http.StatusOK)
		if got["ok"] != wantOK || got["error_code"] != wantCode || got["message"] == "" {
			t.Errorf("%s: result = %v, want ok %v and error_code %q", what, got, wantOK, wantCode)
		}
	}

	// A key whose account is the one on the row passes; another account's key does not.
	key := func(a *awstest.Account) map[string]any { return awsKeyCredential(a.ID, "test") }
	check("key of the same account", create("key-ok", keyed.ID, key(keyed)), true, "")
	check("key of another account", create("key-other", other.ID, key(keyed)), false, "AccountMismatch")

	// A role is assumed from the orchestrator's own credentials, with the external id.
	role := map[string]any{"method": "role", "role_arn": roleARN, "external_id": "ext-" + awstest.RandomSuffix()}
	check("role in the same account", create("role-ok", home.ID, role), true, "")
	check("role in another account", create("role-other", other2.ID, role), false, "AccountMismatch")
}
