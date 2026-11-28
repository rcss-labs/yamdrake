package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/cli"
	"github.com/rcss-labs/yumdrake/internal/spec"
)

func selectionJob() spec.Job {
	return spec.Job{ID: "roll", Groups: []spec.Group{
		{ID: "backends", Services: []string{"billing", "orders"}},
		{ID: "workers", Services: []string{"mailer", "billing"}},
	}}
}

func TestBuildSelection(t *testing.T) {
	latest := func(group string) (string, error) { return "9." + group, nil }
	tests := []struct {
		name   string
		sets   []string
		only   []string
		latest bool
		want   map[string]selectionGroup
	}{
		{
			name: "a whole group at one version",
			sets: []string{"backends=1.4.2", "workers=2.0.0"},
			want: map[string]selectionGroup{
				"backends": {Version: "1.4.2", Services: map[string]string{"billing": "", "orders": ""}},
				"workers":  {Version: "2.0.0", Services: map[string]string{"mailer": "", "billing": ""}},
			},
		},
		{
			name: "one service overrides the group version",
			sets: []string{"backends=1.4.2", "backends.orders=1.3.9", "workers=2.0.0"},
			want: map[string]selectionGroup{
				"backends": {Version: "1.4.2", Services: map[string]string{"billing": "", "orders": "1.3.9"}},
				"workers":  {Version: "2.0.0", Services: map[string]string{"mailer": "", "billing": ""}},
			},
		},
		{
			name: "only a group narrows to that group",
			sets: []string{"backends=1.4.2"},
			only: []string{"backends"},
			want: map[string]selectionGroup{"backends": {Version: "1.4.2", Services: map[string]string{"billing": "", "orders": ""}}},
		},
		{
			name: "only a service narrows to it in every group that has it",
			sets: []string{"backends=1.4.2", "workers=2.0.0"},
			only: []string{"billing"},
			want: map[string]selectionGroup{
				"backends": {Version: "1.4.2", Services: map[string]string{"billing": ""}},
				"workers":  {Version: "2.0.0", Services: map[string]string{"billing": ""}},
			},
		},
		{
			name: "only takes comma separated and repeated names",
			sets: []string{"backends=1.4.2", "workers=2.0.0"},
			only: []string{"orders,mailer", "billing"},
			want: map[string]selectionGroup{
				"backends": {Version: "1.4.2", Services: map[string]string{"billing": "", "orders": ""}},
				"workers":  {Version: "2.0.0", Services: map[string]string{"mailer": "", "billing": ""}},
			},
		},
		{
			name: "a group left out by only needs no version",
			sets: []string{"backends=1.4.2"},
			only: []string{"orders"},
			want: map[string]selectionGroup{"backends": {Version: "1.4.2", Services: map[string]string{"orders": ""}}},
		},
		{
			name:   "latest fills the groups without a set",
			sets:   []string{"backends=1.4.2"},
			latest: true,
			want: map[string]selectionGroup{
				"backends": {Version: "1.4.2", Services: map[string]string{"billing": "", "orders": ""}},
				"workers":  {Version: "9.workers", Services: map[string]string{"mailer": "", "billing": ""}},
			},
		},
		{
			name: "overrides on every selected service need no group version",
			sets: []string{"backends.billing=1.0.0", "backends.orders=1.1.0"},
			only: []string{"backends"},
			want: map[string]selectionGroup{"backends": {Version: "", Services: map[string]string{"billing": "1.0.0", "orders": "1.1.0"}}},
		},
		{
			name: "a version may contain an equals sign",
			sets: []string{"backends=a=b"},
			only: []string{"backends"},
			want: map[string]selectionGroup{"backends": {Version: "a=b", Services: map[string]string{"billing": "", "orders": ""}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildSelection(selectionJob(), tt.sets, tt.only, tt.latest, latest)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("selection = %+v\nwant        %+v", got, tt.want)
			}
		})
	}
}

// Every mistake names what was wrong and lists the valid names (Review Focus 5).
func TestBuildSelectionErrors(t *testing.T) {
	noLatest := func(string) (string, error) { return "", errors.New("must not be called") }
	tests := []struct {
		name   string
		sets   []string
		only   []string
		latest bool
		wants  []string
	}{
		{"unknown group in set", []string{"backend=1.0.0"}, nil, false, []string{`"backend"`, "backends", "workers"}},
		{"unknown service in set", []string{"backends=1.0.0", "workers=1.0.0", "backends.biling=1.0.0"}, nil, false, []string{`"biling"`, "billing", "orders"}},
		{"service of another group in set", []string{"backends=1.0.0", "workers=1.0.0", "backends.mailer=1.0.0"}, nil, false, []string{`"mailer"`, "backends", "billing", "orders"}},
		{"unknown name in only", []string{"backends=1.0.0"}, []string{"ordres"}, false, []string{`"ordres"`, "backends", "workers", "billing", "mailer", "orders"}},
		{"a group without a version", []string{"backends=1.0.0"}, nil, false, []string{`"workers"`, "--set workers=VERSION", "--latest"}},
		{"a group with only some versions", []string{"backends.billing=1.0.0"}, []string{"backends"}, false, []string{`"backends"`, "orders"}},
		{"a missing version", []string{"backends="}, nil, false, []string{"backends", "version"}},
		{"no equals sign", []string{"backends"}, nil, false, []string{`"backends"`, "GROUP=VERSION"}},
		{"no versions at all", nil, []string{}, false, []string{"backends"}}, // no sets, no latest: first group lacks a version
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildSelection(selectionJob(), tt.sets, tt.only, tt.latest, noLatest)
			if err == nil {
				t.Fatal("no error")
			}
			if cli.ExitCodeFor(err) != 2 {
				t.Errorf("exit code = %d, want 2", cli.ExitCodeFor(err))
			}
			for _, want := range tt.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestBuildSelectionReportsEveryMistakeAtOnce(t *testing.T) {
	_, err := buildSelection(selectionJob(), []string{"bakends=1", "workers.maler=2"}, nil, false,
		func(string) (string, error) { return "", nil })
	if err == nil || !strings.Contains(err.Error(), `"bakends"`) || !strings.Contains(err.Error(), `"maler"`) {
		t.Errorf("err = %v", err)
	}
}

func TestBuildSelectionLatestWithoutTagsNamesTheGroup(t *testing.T) {
	_, err := buildSelection(selectionJob(), []string{"backends=1.0.0"}, nil, true,
		func(g string) (string, error) { return "", errors.New("group " + g + " has no tag every service has") })
	if err == nil || !strings.Contains(err.Error(), "workers") {
		t.Errorf("err = %v", err)
	}
}
