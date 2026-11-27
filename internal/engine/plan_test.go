package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rcss-labs/yamdrake/internal/spec"
)

const testAccount = "123456789012"

func ecsARN(name string) string {
	return fmt.Sprintf("arn:aws:ecs:eu-west-1:%s:service/cluster/%s", testAccount, name)
}

// fixture: group "api" has 4 services, group "workers" has 3; each group has 2 steps.
// "migrate" (api) is needed by "deploy-api"; "deploy-workers" needs "deploy-api".
func fixture() (spec.Job, spec.Inventory) {
	inv := spec.Inventory{
		Environments: []spec.Environment{
			{Name: "staging", Label: "nonprod", AWSAccount: testAccount},
			{Name: "prod", Label: "prod", AWSAccount: testAccount},
		},
		Services: map[string]spec.Service{},
	}
	add := func(name string, envs ...string) {
		service := spec.Service{Name: name, Environments: map[string]spec.ServiceEnv{}}
		for _, env := range envs {
			service.Environments[env] = spec.ServiceEnv{
				ECSService: ecsARN(name + "-" + env),
				EnvFiles:   []spec.EnvFile{{Name: "env", Destination: "s3://cfg/" + env + "/" + name + ".env"}},
				Tomls:      []spec.Toml{{Name: "app", Destination: "s3://cfg/" + env + "/" + name + ".toml"}},
			}
		}
		inv.Services[name] = service
	}
	for _, name := range []string{"a1", "a2", "a3", "a4", "w1", "w2", "w3"} {
		add(name, "staging", "prod")
	}
	add("a5", "prod") // listed in group api but absent from staging

	job := spec.Job{
		ID:     "release",
		Labels: []string{"nonprod"},
		Groups: []spec.Group{
			{ID: "api", Services: []string{"a1", "a2", "a3", "a4", "a5"}},
			{ID: "workers", Services: []string{"w1", "w2", "w3"}},
		},
		Steps: []spec.Step{
			{Name: "migrate", Use: "copy-config", Group: "api"},
			{Name: "deploy-api", Use: "ecs-deploy", Group: "api", MaxParallel: 2, Needs: []spec.Need{{Step: "migrate", Scope: "service"}}},
			{Name: "config-workers", Use: "copy-config", Group: "workers"},
			{Name: "deploy-workers", Use: "ecs-deploy", Group: "workers",
				Needs: []spec.Need{{Step: "deploy-api", Scope: "step"}, {Step: "config-workers", Scope: "service"}}},
		},
	}
	return job, inv
}

func fullSelection() Selection {
	return Selection{
		"api":     {Version: "1.0.0", Services: map[string]string{"a1": "", "a2": "", "a3": "", "a4": ""}},
		"workers": {Version: "2.0.0", Services: map[string]string{"w1": "", "w2": "", "w3": ""}},
	}
}

func problemMessages(problems spec.Problems) string {
	messages := make([]string, len(problems))
	for i, p := range problems {
		messages[i] = p.Message
	}
	return strings.Join(messages, "\n")
}

func TestBuildFullSelection(t *testing.T) {
	job, inv := fixture()
	built, problems := Build(job, inv, "staging", fullSelection())
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(built.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(built.Steps))
	}
	if len(built.Legs) != 15 {
		t.Fatalf("legs = %d, want 14 step legs + 1 resolve leg", len(built.Legs))
	}

	resolve := built.Legs[0]
	if resolve.Kind != "resolve" || resolve.State != "queued" || resolve.StepID != nil {
		t.Errorf("first leg = %+v, want a queued resolve leg without a step", resolve)
	}

	perService := map[string]int{}
	for _, leg := range built.Legs[1:] {
		if leg.Kind != "step" || leg.State != "pending" || leg.StepID == nil {
			t.Fatalf("leg = %+v, want a pending step leg with a step index", leg)
		}
		step := built.Steps[*leg.StepID]
		wantVersion := "1.0.0"
		if step.GroupID == "workers" {
			wantVersion = "2.0.0"
		}
		if leg.Version != wantVersion {
			t.Errorf("leg %s/%s version = %q, want %q", step.Name, leg.Service, leg.Version, wantVersion)
		}
		if (step.GroupID == "api") != strings.HasPrefix(leg.Service, "a") {
			t.Errorf("leg of step %s (group %s) has service %s", step.Name, step.GroupID, leg.Service)
		}
		perService[step.Name]++
	}
	want := map[string]int{"migrate": 4, "deploy-api": 4, "config-workers": 3, "deploy-workers": 3}
	for name, count := range want {
		if perService[name] != count {
			t.Errorf("step %s has %d legs, want %d", name, perService[name], count)
		}
	}

	for i, step := range built.Steps {
		if step.Position != i {
			t.Errorf("step %s position = %d, want %d", step.Name, step.Position, i)
		}
	}
	if built.Steps[1].MaxParallel != 2 || built.Steps[1].Use != "ecs-deploy" {
		t.Errorf("step copy = %+v", built.Steps[1])
	}
	var needs []spec.Need
	if err := json.Unmarshal(built.Steps[3].Needs, &needs); err != nil || len(needs) != 2 {
		t.Fatalf("deploy-workers needs = %s (%v), want 2 entries", built.Steps[3].Needs, err)
	}

	// 7 ECS ARNs, plus an env file and a toml destination per service.
	var wantResources []string
	for _, name := range []string{"a1", "a2", "a3", "a4", "w1", "w2", "w3"} {
		wantResources = append(wantResources,
			ecsARN(name+"-staging"),
			"s3://cfg/staging/"+name+".env",
			"s3://cfg/staging/"+name+".toml")
	}
	sort.Strings(wantResources)
	if !slices.Equal(built.Resources, wantResources) {
		t.Errorf("resources = %v, want %v", built.Resources, wantResources)
	}
}

func TestBuildPerServiceVersionOverride(t *testing.T) {
	job, inv := fixture()
	sel := fullSelection()
	sel["api"].Services["a2"] = "1.5.0"
	built, problems := Build(job, inv, "staging", sel)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	for _, leg := range built.Legs[1:] {
		want := "1.0.0"
		switch {
		case leg.Service == "a2":
			want = "1.5.0"
		case strings.HasPrefix(leg.Service, "w"):
			want = "2.0.0"
		}
		if leg.Version != want {
			t.Errorf("service %s version = %q, want %q", leg.Service, leg.Version, want)
		}
	}
}

func TestBuildPartialSelectionDropsEmptyStepsAndTheirNeeds(t *testing.T) {
	job, inv := fixture()
	sel := Selection{"workers": {Version: "2.0.0", Services: map[string]string{"w2": ""}}}
	built, problems := Build(job, inv, "staging", sel)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(built.Steps) != 2 || built.Steps[0].Name != "config-workers" || built.Steps[1].Name != "deploy-workers" {
		t.Fatalf("steps = %+v, want only the workers steps", built.Steps)
	}
	if len(built.Legs) != 3 {
		t.Fatalf("legs = %d, want resolve + 2 step legs", len(built.Legs))
	}
	var needs []spec.Need
	if err := json.Unmarshal(built.Steps[1].Needs, &needs); err != nil {
		t.Fatal(err)
	}
	if len(needs) != 1 || needs[0].Step != "config-workers" {
		t.Errorf("deploy-workers needs = %+v, want only config-workers (deploy-api was dropped)", needs)
	}
	if string(built.Steps[0].Needs) != "[]" {
		t.Errorf("step with no needs = %s, want []", built.Steps[0].Needs)
	}
	for _, leg := range built.Legs[1:] {
		if leg.Service != "w2" || *leg.StepID > 1 {
			t.Errorf("unexpected leg %+v", leg)
		}
	}
	wantResources := []string{ecsARN("w2-staging"), "s3://cfg/staging/w2.env", "s3://cfg/staging/w2.toml"}
	if !slices.Equal(built.Resources, wantResources) {
		t.Errorf("resources = %v, want %v", built.Resources, wantResources)
	}
}

func TestBuildSkipsServiceNotInEnvironmentAsProblem(t *testing.T) {
	job, inv := fixture()
	sel := Selection{"api": {Version: "1.0.0", Services: map[string]string{"a1": "", "a5": ""}}}
	_, problems := Build(job, inv, "staging", sel)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "a5") || !strings.Contains(problems[0].Message, "staging") {
		t.Fatalf("problems = %v, want one problem naming a5 and staging", problems)
	}
}

func TestBuildRejections(t *testing.T) {
	tests := []struct {
		name string
		env  string
		sel  func() Selection
		want string
	}{
		{"unknown environment", "nowhere", fullSelection, `environment "nowhere"`},
		{"label not permitted by the job", "prod", fullSelection, `label "prod"`},
		{"unknown group", "staging", func() Selection {
			return Selection{"ghosts": {Version: "1", Services: map[string]string{"a1": ""}}}
		}, `group "ghosts"`},
		{"service not in group", "staging", func() Selection {
			return Selection{"api": {Version: "1", Services: map[string]string{"w1": ""}}}
		}, `service "w1" is not in group "api"`},
		{"nothing selected", "staging", func() Selection { return Selection{} }, "no service is selected"},
		{"group selected without services", "staging", func() Selection {
			return Selection{"api": {Version: "1"}}
		}, "no service is selected"},
		{"empty version", "staging", func() Selection {
			return Selection{"api": {Services: map[string]string{"a1": ""}}}
		}, `service "a1" has no version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, inv := fixture()
			built, problems := Build(job, inv, tt.env, tt.sel())
			if len(problems) == 0 {
				t.Fatalf("no problems, want one containing %q", tt.want)
			}
			if !strings.Contains(problemMessages(problems), tt.want) {
				t.Errorf("problems = %q, want one containing %q", problemMessages(problems), tt.want)
			}
			if len(built.Legs) != 0 || len(built.Steps) != 0 {
				t.Errorf("a rejected selection returned a plan: %+v", built)
			}
		})
	}
}
