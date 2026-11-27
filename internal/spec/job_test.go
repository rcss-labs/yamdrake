package spec

import (
	"reflect"
	"strings"
	"testing"
)

const testJobFile = "jobs/deploy-backend.yaml"

// validJobAndInventory parses the valid fixtures fresh, so each test may mutate its copies.
func validJobAndInventory(t *testing.T) (Job, Inventory) {
	t.Helper()
	envs, services := validModel(t)
	inv, problems := BuildInventory(testEnvFile, envs, services)
	if len(problems) > 0 {
		t.Fatalf("inventory: %v", problems)
	}
	job, problems := ParseJob(testJobFile, readFixture(t, "valid/jobs/deploy-backend.yaml"))
	if len(problems) > 0 {
		t.Fatalf("job: %v", problems)
	}
	return job, inv
}

func TestValidateJobAcceptsValidFixture(t *testing.T) {
	job, inv := validJobAndInventory(t)
	if problems := ValidateJob(job, inv); len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestValidateJobRejections(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(job *Job, inv *Inventory)
		fragment string
		line     func(job Job) int
	}{
		{
			name:     "empty environment labels",
			mutate:   func(job *Job, _ *Inventory) { job.Labels = nil; job.Approval = nil },
			fragment: "environment.labels must list at least one label",
			line:     func(job Job) int { return job.LabelsLine },
		},
		{
			name:     "duplicate environment label",
			mutate:   func(job *Job, _ *Inventory) { job.Labels = []string{"dev", "prod", "dev"} },
			fragment: `environment label "dev" is listed more than once`,
			line:     func(job Job) int { return job.LabelsLine },
		},
		{
			name:     "no groups",
			mutate:   func(job *Job, _ *Inventory) { job.Groups = nil; job.Steps = nil },
			fragment: "job defines no groups",
			line:     func(Job) int { return 0 },
		},
		{
			name:     "group without services",
			mutate:   func(job *Job, _ *Inventory) { job.Groups[0].Services = nil },
			fragment: `group "backends" lists no services`,
			line:     func(job Job) int { return job.Groups[0].Line },
		},
		{
			name: "duplicate group id",
			mutate: func(job *Job, _ *Inventory) {
				job.Groups = append(job.Groups, Group{ID: "backends", Services: []string{"backend-api"}, Line: 99})
			},
			fragment: `group "backends" is defined more than once`,
			line:     func(Job) int { return 99 },
		},
		{
			name:     "unknown service",
			mutate:   func(job *Job, _ *Inventory) { job.Groups[0].Services = []string{"backend-api", "ghost"} },
			fragment: `service "ghost" in group "backends" is not in the inventory`,
			line:     func(job Job) int { return job.Groups[0].Line },
		},
		{
			name:     "service in two groups",
			mutate:   func(job *Job, _ *Inventory) { job.Groups[0].Services = []string{"backend-api", "orders"} },
			fragment: `service "orders" appears more than once in the job (groups "backends" and "microservices")`,
			line:     func(job Job) int { return job.Groups[1].Line },
		},
		{
			name:     "no steps",
			mutate:   func(job *Job, _ *Inventory) { job.Steps = nil },
			fragment: "job defines no steps",
			line:     func(Job) int { return 0 },
		},
		{
			name:     "duplicate step name",
			mutate:   func(job *Job, _ *Inventory) { job.Steps[1].Name = "config-backends" },
			fragment: `step "config-backends" is defined more than once`,
			line:     func(job Job) int { return job.Steps[1].Line },
		},
		{
			name:     "step with unknown group",
			mutate:   func(job *Job, _ *Inventory) { job.Steps[0].Group = "nowhere" },
			fragment: `step "config-backends" uses group "nowhere", which is not defined`,
			line:     func(job Job) int { return job.Steps[0].Line },
		},
		{
			name:     "needs unknown step",
			mutate:   func(job *Job, _ *Inventory) { job.Steps[1].Needs[0].Step = "ghost" },
			fragment: `step "roll-backends" needs "ghost", which is not a step of this job`,
			line:     func(job Job) int { return job.Steps[1].Needs[0].Line },
		},
		{
			name:     "needs itself",
			mutate:   func(job *Job, _ *Inventory) { job.Steps[1].Needs[0].Step = "roll-backends" },
			fragment: `step "roll-backends" needs itself`,
			line:     func(job Job) int { return job.Steps[1].Needs[0].Line },
		},
		{
			name:     "service scope across groups",
			mutate:   func(job *Job, _ *Inventory) { job.Steps[3].Needs[1].Scope = "service" },
			fragment: `scope "service" requires the same group, but step "roll-microservices" uses group "microservices" and "config-backends" uses group "backends"`,
			line:     func(job Job) int { return job.Steps[3].Needs[1].Line },
		},
		{
			name: "approval label outside environment labels",
			mutate: func(job *Job, _ *Inventory) {
				job.Labels = []string{"dev", "prod"}
				job.Approval.Labels = []string{"preprod"}
			},
			fragment: `approval.when.labels contains "preprod", which is not in environment.labels`,
			line:     func(job Job) int { return job.Approval.Line },
		},
		{
			name:     "group with no permitted environment",
			mutate:   func(job *Job, _ *Inventory) { job.Labels = []string{"preprod"}; job.Approval = nil },
			fragment: `group "backends" has no service in any environment permitted by the job labels`,
			line:     func(job Job) int { return job.Groups[0].Line },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job, inv := validJobAndInventory(t)
			tc.mutate(&job, &inv)
			wantLine := tc.line(job)
			problems := ValidateJob(job, inv)
			for _, problem := range problems {
				if strings.Contains(problem.Message, tc.fragment) {
					if problem.File != testJobFile || problem.Line != wantLine {
						t.Errorf("problem %+v: want file %s line %d", problem, testJobFile, wantLine)
					}
					return
				}
			}
			t.Errorf("no problem contains %q; got:\n%v", tc.fragment, problems)
		})
	}
}

func TestValidateJobProblemsAreSortedAndPositioned(t *testing.T) {
	job, inv := validJobAndInventory(t)
	job.Groups[0].Services = []string{"ghost"}
	job.Steps[0].Group = "nowhere"
	problems := ValidateJob(job, inv)
	if len(problems) < 2 {
		t.Fatalf("expected several problems, got %v", problems)
	}
	if !reflect.DeepEqual(problems, problems.Sorted()) {
		t.Errorf("problems are not sorted: %v", problems)
	}
}

func TestValidateJobReportsCycleOnce(t *testing.T) {
	job, inv := validJobAndInventory(t)
	job.Steps = []Step{
		{Name: "a", Use: "copy-config", Group: "backends", Line: 10, Needs: []Need{{Step: "b", Scope: "step", Line: 11}}},
		{Name: "b", Use: "copy-config", Group: "backends", Line: 12, Needs: []Need{{Step: "c", Scope: "step", Line: 13}}},
		{Name: "c", Use: "copy-config", Group: "backends", Line: 14, Needs: []Need{{Step: "a", Scope: "step", Line: 15}}},
	}
	problems := ValidateJob(job, inv)
	if len(problems) != 1 {
		t.Fatalf("want exactly one problem, got %v", problems)
	}
	for _, name := range []string{`"a"`, `"b"`, `"c"`} {
		if !strings.Contains(problems[0].Message, name) {
			t.Errorf("cycle problem %q does not mention %s", problems[0].Message, name)
		}
	}
	if !strings.Contains(problems[0].Message, "cycle") || problems[0].Line != 10 {
		t.Errorf("problem = %+v, want a cycle problem on line 10", problems[0])
	}
}

func TestValidateJobMissingNeedIsNotAlsoACycle(t *testing.T) {
	job, inv := validJobAndInventory(t)
	job.Steps[0].Needs = []Need{{Step: "ghost", Scope: "step", Line: 5}}
	for _, problem := range ValidateJob(job, inv) {
		if strings.Contains(problem.Message, "cycle") {
			t.Errorf("unexpected cycle problem: %v", problem)
		}
	}
}

func TestStepOrderLayers(t *testing.T) {
	job, _ := validJobAndInventory(t)
	want := [][]string{
		{"config-backends", "config-microservices"},
		{"roll-backends", "roll-microservices"},
	}
	if got := job.StepOrder(); !reflect.DeepEqual(got, want) {
		t.Errorf("StepOrder() = %v, want %v", got, want)
	}
}

func TestStepOrderOnCyclicJobTerminatesWithNil(t *testing.T) {
	job := Job{Steps: []Step{
		{Name: "a", Needs: []Need{{Step: "b"}}},
		{Name: "b", Needs: []Need{{Step: "a"}}},
	}}
	if got := job.StepOrder(); got != nil {
		t.Errorf("StepOrder() = %v, want nil for a cyclic job", got)
	}
}

func TestServicesInOmitsServicesMissingFromEnvironment(t *testing.T) {
	job, inv := validJobAndInventory(t)
	inv.Services["extra"] = Service{Name: "extra", Environments: map[string]ServiceEnv{"dev-02": {}}}
	job.Groups[0].Services = []string{"backend-api", "extra", "orders"}

	got := job.ServicesIn("prod-01", inv)
	if want := []string{"backend-api", "orders"}; !reflect.DeepEqual(got["backends"], want) {
		t.Errorf(`ServicesIn("prod-01")["backends"] = %v, want %v`, got["backends"], want)
	}
	// "microservices" lists orders, which exists in prod-01 but not dev-03.
	empty := job.ServicesIn("dev-03", inv)
	services, present := empty["microservices"]
	if !present || len(services) != 0 {
		t.Errorf(`ServicesIn("dev-03")["microservices"] = %v (present %v), want present and empty`, services, present)
	}
}

func TestGroupAndStepLookup(t *testing.T) {
	job, _ := validJobAndInventory(t)
	if group, ok := job.Group("microservices"); !ok || group.Services[0] != "orders" {
		t.Errorf("Group(microservices) = %+v, %v", group, ok)
	}
	if _, ok := job.Group("nope"); ok {
		t.Error("Group(nope) should not be found")
	}
	if step, ok := job.Step("roll-backends"); !ok || step.Use != "ecs-deploy" {
		t.Errorf("Step(roll-backends) = %+v, %v", step, ok)
	}
	if _, ok := job.Step("nope"); ok {
		t.Error("Step(nope) should not be found")
	}
}
