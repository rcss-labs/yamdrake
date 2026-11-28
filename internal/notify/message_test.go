package notify

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/store"
)

func baseEvent(kind, state string) Event {
	return Event{
		Kind:      kind,
		Run:       store.Run{ID: 42, Environment: "prod", Label: "blue", State: state},
		JobTitle:  "Deploy backend",
		Requester: "alice",
		Versions:  "Backend services 2.31.0 (4), Microservices 1.4.2 (20)",
	}
}

func TestText(t *testing.T) {
	const link = "\nhttps://yum.example.com/runs/42"
	finished := func(state string, updated, total int, failures ...Failure) Event {
		e := baseEvent("finished", state)
		e.Updated, e.Total, e.Failures = updated, total, failures
		return e
	}
	approved := baseEvent("approved", "")
	approved.Actor = "bob"
	rejected := baseEvent("rejected", "")
	rejected.Actor = "bob"

	tests := []struct {
		name string
		e    Event
		want string
	}{
		{"approval_requested", baseEvent("approval_requested", "waiting_approval"),
			`alice wants to run "Deploy backend" in prod (blue): Backend services 2.31.0 (4), Microservices 1.4.2 (20). Review and approve:` + link},
		{"approved", approved, `Approved by bob: "Deploy backend" in prod.` + link},
		{"rejected", rejected, `Rejected by bob: "Deploy backend" in prod.` + link},
		{"approval_expired", baseEvent("approval_expired", ""), `The approval request for "Deploy backend" in prod expired.` + link},
		{"started", baseEvent("started", "running"), `Started: "Deploy backend" in prod by alice.` + link},
		{"finished succeeded", finished("succeeded", 24, 24),
			`Succeeded: "Deploy backend" in prod. 24 of 24 services updated.` + link},
		{"finished partially_succeeded", finished("partially_succeeded", 22, 24, Failure{"api", "deploy", "timeout"}, Failure{Service: "worker"}),
			"Partly succeeded: \"Deploy backend\" in prod. 22 of 24 services updated.\nFailed: api (timeout), worker" + link},
		{"finished failed", finished("failed", 0, 24, Failure{"api", "deploy", "boom"}),
			"Failed: \"Deploy backend\" in prod. 0 of 24 services updated.\nFailed: api (boom)" + link},
		{"finished cancelled", finished("cancelled", 3, 24),
			`Cancelled: "Deploy backend" in prod. 3 of 24 services updated.` + link},
		{"finished needs_reconciliation", finished("needs_reconciliation", 5, 24),
			`Needs reconciliation: "Deploy backend" in prod. 5 of 24 services updated.` + link},
		{"finished other state", finished("weird", 1, 2),
			`Ended (weird): "Deploy backend" in prod. 1 of 2 services updated.` + link},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.e, "https://yum.example.com"); got != tc.want {
				t.Errorf("Text() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestFinishedListsAtMostTenFailures(t *testing.T) {
	e := baseEvent("finished", "failed")
	for i := 1; i <= 12; i++ {
		e.Failures = append(e.Failures, Failure{Service: fmt.Sprintf("svc%d", i), Short: "bad"})
	}
	got := Text(e, "https://yum.example.com")
	if !strings.Contains(got, "svc10 (bad), and 2 more\n") {
		t.Errorf("want ten listed then \", and 2 more\", got %q", got)
	}
	if strings.Contains(got, "svc11") {
		t.Errorf("svc11 must not be listed: %q", got)
	}
}

func TestLongShortIsCutToTwoHundredRunes(t *testing.T) {
	e := baseEvent("finished", "failed")
	e.Failures = []Failure{{Service: "api", Short: strings.Repeat("é", 300)}}
	got := Text(e, "https://yum.example.com")
	want := "Failed: api (" + strings.Repeat("é", 200) + "…)"
	if !strings.Contains(got, want) {
		t.Errorf("short not cut to 200 runes plus ellipsis: %q", got)
	}
}

func TestLinkHasNoDoubleSlash(t *testing.T) {
	got := Text(baseEvent("started", "running"), "https://yum.example.com/")
	if !strings.HasSuffix(got, "\nhttps://yum.example.com/runs/42") {
		t.Errorf("got %q", got)
	}
}

func TestSlackMarkupInValuesIsEscaped(t *testing.T) {
	e := baseEvent("finished", "failed")
	e.JobTitle = "Deploy <!channel> & friends"
	e.Requester = "<@U123>"
	e.Run.Environment = "prod<eu>"
	e.Run.Label = "a&b"
	e.Versions = "x <!here> 1.0"
	e.Actor = "<!everyone>"
	e.Updated, e.Total = 0, 1
	e.Failures = []Failure{{Service: "api<1>", Step: "deploy", Short: "<!channel> & boom"}}

	got := Text(e, "https://yum.example.com")
	for _, raw := range []string{"<!channel>", "<@U123>", "<!here>", "<!everyone>", "<eu>", "<1>", "a&b"} {
		if strings.Contains(got, raw) {
			t.Errorf("message contains raw %q:\n%s", raw, got)
		}
	}
	for _, want := range []string{`"Deploy &lt;!channel&gt; &amp; friends"`, "prod&lt;eu&gt;", "api&lt;1&gt; (&lt;!channel&gt; &amp; boom)"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\nhttps://yum.example.com/runs/42") {
		t.Errorf("the run link must stay as built, got:\n%s", got)
	}
	for _, kind := range []string{"approval_requested", "approved", "rejected", "started"} {
		e.Kind = kind
		if text := Text(e, "https://yum.example.com"); strings.Contains(text, "<!") || strings.Contains(text, "<@") {
			t.Errorf("%s message contains raw markup:\n%s", kind, text)
		}
	}
}
