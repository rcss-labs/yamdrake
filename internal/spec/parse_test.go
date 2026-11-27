package spec

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, relative string) []byte {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(relative)
	if err != nil {
		t.Fatalf("read fixture %s: %v", relative, err)
	}
	return data
}

// parser wrappers reduce each parse function to the shared problem list.
func parseEnvironmentsProblems(file string, data []byte) Problems {
	_, problems := ParseEnvironments(file, data)
	return problems
}

func parseServiceProblems(file string, data []byte) Problems {
	_, problems := ParseService(file, data)
	return problems
}

func parseJobProblems(file string, data []byte) Problems {
	_, problems := ParseJob(file, data)
	return problems
}

func TestParseValidFixtures(t *testing.T) {
	t.Run("environments", func(t *testing.T) {
		envs, problems := ParseEnvironments("environments.yaml", readFixture(t, "valid/environments.yaml"))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if len(envs) != 5 {
			t.Fatalf("got %d environments, want 5", len(envs))
		}
		labels := map[string]int{}
		accounts := map[string]bool{}
		for _, env := range envs {
			labels[env.Label]++
			accounts[env.AWSAccount] = true
		}
		if labels["dev"] != 3 || labels["preprod"] != 1 || labels["prod"] != 1 {
			t.Errorf("label counts = %v", labels)
		}
		if len(accounts) != 3 {
			t.Errorf("got %d distinct accounts, want 3", len(accounts))
		}
		if envs[0].Name != "hetzner-01" || envs[0].AWSAccount != "111111111111" || envs[0].Line == 0 {
			t.Errorf("first environment = %+v", envs[0])
		}
	})

	t.Run("backend-api", func(t *testing.T) {
		svc, problems := ParseService("services/backend-api.yaml", readFixture(t, "valid/services/backend-api.yaml"))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if svc.Name != "backend-api" || svc.File != "services/backend-api.yaml" {
			t.Errorf("service = %q in %q", svc.Name, svc.File)
		}
		if len(svc.Environments) != 2 {
			t.Fatalf("got %d environments, want 2", len(svc.Environments))
		}
		hetzner := svc.Environments["hetzner-01"]
		if len(hetzner.EnvFiles) != 1 || len(hetzner.Tomls) != 1 {
			t.Fatalf("hetzner-01 env files = %d, tomls = %d", len(hetzner.EnvFiles), len(hetzner.Tomls))
		}
		file := hetzner.EnvFiles[0]
		if file.Source != "example-org/env-config-repo/hetzner-01/backend-api/backend-api.env" {
			t.Errorf("env file source = %q", file.Source)
		}
		if file.Destination != "s3://hetzner-01-config/env/backend-api/backend-api.env" {
			t.Errorf("env file destination = %q", file.Destination)
		}
		toml := hetzner.Tomls[0]
		if toml.Source != "s3://main-config-bucket/hetzner-01/backend-api.toml" ||
			toml.Destination != "s3://hetzner-01-config/toml/backend-api.toml" {
			t.Errorf("toml = %+v", toml)
		}
		prod := svc.Environments["prod-01"]
		if len(prod.EnvFiles) != 0 || len(prod.Tomls) != 0 || prod.ECSService == "" || prod.ECRRepository == "" {
			t.Errorf("prod-01 = %+v", prod)
		}
	})

	t.Run("orders", func(t *testing.T) {
		svc, problems := ParseService("services/orders.yaml", readFixture(t, "valid/services/orders.yaml"))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if svc.Name != "orders" || len(svc.Environments) != 2 {
			t.Errorf("service = %+v", svc)
		}
	})

	t.Run("job", func(t *testing.T) {
		job, problems := ParseJob("jobs/deploy-backend.yaml", readFixture(t, "valid/jobs/deploy-backend.yaml"))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if job.ID != "deploy-backend" || job.Title != "Backend and microservices" || job.SlackChannel != "#deployments" {
			t.Errorf("job header = %q %q %q", job.ID, job.Title, job.SlackChannel)
		}
		if !reflect.DeepEqual(job.Labels, []string{"dev", "preprod", "prod"}) {
			t.Errorf("labels = %v", job.Labels)
		}
		if job.Approval == nil || !reflect.DeepEqual(job.Approval.Labels, []string{"prod"}) || job.Approval.Expires != 24*time.Hour {
			t.Errorf("approval = %+v", job.Approval)
		}
		if len(job.Groups) != 2 || job.Groups[0].ID != "backends" || job.Groups[1].ID != "microservices" {
			t.Fatalf("groups = %+v", job.Groups)
		}
		if !reflect.DeepEqual(job.Groups[0].Services, []string{"backend-api"}) || job.Groups[0].Label != "Backend services" {
			t.Errorf("first group = %+v", job.Groups[0])
		}
		if len(job.Steps) != 4 {
			t.Fatalf("got %d steps, want 4", len(job.Steps))
		}
		roll := job.Steps[1]
		if roll.Use != "ecs-deploy" || roll.MaxParallel != 2 || roll.Timeout != 15*time.Minute {
			t.Errorf("roll-backends = %+v", roll)
		}
		wantNeeds := [][]Need{
			nil,
			{{Step: "config-backends", Scope: "step"}},
			nil,
			{{Step: "config-microservices", Scope: "service"}, {Step: "config-backends", Scope: "step"}},
		}
		for i, want := range wantNeeds {
			if len(job.Steps[i].Needs) == 0 && len(want) == 0 {
				continue
			}
			if !reflect.DeepEqual(job.Steps[i].Needs, want) {
				t.Errorf("step %s needs = %+v, want %+v", job.Steps[i].Name, job.Steps[i].Needs, want)
			}
		}
	})
}

func TestParseAppliesDefaults(t *testing.T) {
	t.Run("service without versions", func(t *testing.T) {
		svc, problems := ParseService("orders.yaml", readFixture(t, "valid/services/orders.yaml"))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		want := VersionRule{Pattern: `^v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`, Sort: "semver", Limit: 100}
		if svc.Versions != want {
			t.Errorf("versions = %+v, want %+v", svc.Versions, want)
		}
	})

	const jobWithoutExpires = `apiVersion: yumdrake/v1
kind: Job
job: deploy-backend
title: Backend
environment:
  labels: [prod]
groups:
  backends:
    label: Backend services
    services: [backend-api]
approval:
  when:
    labels: [prod]
steps:
  - name: config-backends
    group: backends
    use: copy-config
  - name: roll-backends
    group: backends
    use: ecs-deploy
    needs: [config-backends]
`
	t.Run("job approval and needs", func(t *testing.T) {
		job, problems := ParseJob("job.yaml", []byte(jobWithoutExpires))
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if job.Approval == nil || job.Approval.Expires != 24*time.Hour {
			t.Errorf("approval = %+v, want expires 24h", job.Approval)
		}
		if got := job.Steps[1].Needs; len(got) != 1 || got[0] != (Need{Step: "config-backends", Scope: "step"}) {
			t.Errorf("needs = %+v, want one step-scoped need", got)
		}
		if job.Steps[0].Timeout != 0 || job.Steps[0].MaxParallel != 0 {
			t.Errorf("unset timeout and max_parallel should be zero: %+v", job.Steps[0])
		}
	})
}

func TestParseReportsLineNumbers(t *testing.T) {
	problems := parseJobProblems("jobs/bad.yaml", readFixture(t, "invalid/job-use-build.yaml"))
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(problems), problems)
	}
	if problems[0].Line != 14 {
		t.Errorf("line = %d, want 14", problems[0].Line)
	}
	if problems[0].File != "jobs/bad.yaml" || !strings.Contains(problems[0].Message, "build") {
		t.Errorf("problem = %+v", problems[0])
	}
}

func TestParseRejections(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		parse    func(string, []byte) Problems
		fragment string
	}{
		{"missing apiVersion", "job-missing-apiversion", parseJobProblems, "apiVersion"},
		{"apiVersion v2", "job-apiversion-v2", parseJobProblems, "yumdrake/v2"},
		{"wrong kind", "job-wrong-kind", parseJobProblems, "kind"},
		{"unknown top-level field", "job-unknown-top-field", parseJobProblems, "inputs"},
		{"unknown step field", "job-unknown-step-field", parseJobProblems, "colour"},
		{"use run", "job-use-run", parseJobProblems, "run"},
		{"use verify", "job-use-verify", parseJobProblems, "verify"},
		{"label staging", "job-label-staging", parseJobProblems, "staging"},
		{"aws_account bare number", "env-aws-account-number", parseEnvironmentsProblems, "aws_account"},
		{"aws_account 11 digits", "env-aws-account-11-digits", parseEnvironmentsProblems, "11111111111"},
		{"ecs_service bare name", "service-ecs-bare-name", parseServiceProblems, "ecs_service"},
		{"ecr_repository without host", "service-ecr-no-registry", parseServiceProblems, "ecr_repository"},
		{"source_env_path two parts", "service-source-two-parts", parseServiceProblems, "example-org/backend-api.env"},
		{"destination not s3", "service-destination-not-s3", parseServiceProblems, "s3://"},
		{"pattern does not compile", "service-pattern-invalid", parseServiceProblems, "pattern"},
		{"limit zero", "service-limit-zero", parseServiceProblems, "limit"},
		{"max_parallel zero", "job-max-parallel-zero", parseJobProblems, "max_parallel"},
		{"timeout on copy-config", "job-timeout-on-copy-config", parseJobProblems, "timeout"},
		{"needs scope leg", "job-needs-scope-leg", parseJobProblems, "leg"},
		{"slack without hash", "job-slack-no-hash", parseJobProblems, "deployments"},
		{"upper-case job id", "job-uppercase-id", parseJobProblems, "Deploy-Backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := tt.parse(tt.fixture+".yaml", readFixture(t, "invalid/"+tt.fixture+".yaml"))
			if len(problems) == 0 {
				t.Fatal("expected a problem, got none")
			}
			for _, problem := range problems {
				if problem.File != tt.fixture+".yaml" {
					t.Errorf("problem file = %q", problem.File)
				}
				if strings.Contains(problem.Message, tt.fragment) {
					return
				}
			}
			t.Errorf("no problem mentions %q: %v", tt.fragment, problems)
		})
	}
}

func TestParseNeverPanics(t *testing.T) {
	billionLaughs := "a: &a [x, x, x, x, x, x, x, x, x]\n" +
		"b: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a]\n" +
		"c: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b]\n" +
		"d: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c]\n" +
		"e: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d]\n" +
		"f: &f [*e, *e, *e, *e, *e, *e, *e, *e, *e]\n" +
		"g: &g [*f, *f, *f, *f, *f, *f, *f, *f, *f]\n" +
		"h: &h [*g, *g, *g, *g, *g, *g, *g, *g, *g]\n" +
		"i: [*h, *h, *h, *h, *h, *h, *h, *h, *h]\n"

	tests := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"only a comment", "# nothing here\n"},
		{"tab indentation", "apiVersion: yumdrake/v1\nkind: Job\nsteps:\n\t- name: x\n"},
		{"duplicate key", "apiVersion: yumdrake/v1\nkind: Job\njob: a\njob: b\n"},
		{"two documents", "apiVersion: yumdrake/v1\nkind: Job\n---\napiVersion: yumdrake/v1\nkind: Job\n"},
		{"sequence at root", "- a\n- b\n"},
		{"scalar at root", "just text\n"},
		{"null document", "---\n"},
		{"1 MB of nested a:", strings.Repeat("a: ", 350000)},
		{"deeply nested flow sequences", strings.Repeat("[", 400000) + strings.Repeat("]", 400000)},
		{"deeply nested block mappings", func() string {
			var b strings.Builder
			for i := 0; i < 5000; i++ {
				b.WriteString(strings.Repeat(" ", i) + "a:\n")
			}
			return b.String()
		}()},
		{"alias expansion bomb", billionLaughs},
		{"binary junk", "\x00\x01\x02\xff\xfe: [\n"},
	}
	functions := []struct {
		name  string
		parse func(string, []byte) Problems
	}{
		{"environments", parseEnvironmentsProblems},
		{"service", parseServiceProblems},
		{"job", parseJobProblems},
	}
	for _, tt := range tests {
		for _, fn := range functions {
			t.Run(tt.name+"/"+fn.name, func(t *testing.T) {
				start := time.Now()
				problems := fn.parse("hostile.yaml", []byte(tt.data))
				if len(problems) == 0 {
					t.Fatal("expected at least one problem")
				}
				if elapsed := time.Since(start); elapsed > 5*time.Second {
					t.Errorf("parse took %s", elapsed)
				}
			})
		}
	}
}

func TestParseHostileInputsReportLines(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantLine int
		fragment string
	}{
		{"duplicate key", "apiVersion: yumdrake/v1\nkind: Job\njob: a\njob: b\n", 4, "job"},
		{"second document", "apiVersion: yumdrake/v1\nkind: Job\n---\nkind: Job\n", 4, "document"},
		{"alias", "apiVersion: yumdrake/v1\nkind: Job\nx: &a one\ny: *a\n", 4, "alias"},
		{"tab indentation", "apiVersion: yumdrake/v1\nkind: Job\nsteps:\n\t- name: x\n", 4, ""},
		{"sequence at root", "- a\n- b\n", 1, "mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := parseJobProblems("job.yaml", []byte(tt.data))
			for _, problem := range problems {
				if problem.Line == tt.wantLine && strings.Contains(problem.Message, tt.fragment) {
					return
				}
			}
			t.Errorf("no problem on line %d mentioning %q: %v", tt.wantLine, tt.fragment, problems)
		})
	}
}

func TestProblemsSortedAndFormatted(t *testing.T) {
	problems := Problems{
		{File: "b.yaml", Line: 1, Message: "third"},
		{File: "a.yaml", Line: 9, Message: "second"},
		{File: "a.yaml", Line: 2, Message: "first"},
	}
	got := problems.Sorted().Error()
	want := "a.yaml:2: first\na.yaml:9: second\nb.yaml:1: third"
	if got != want {
		t.Errorf("formatted problems:\n%s\nwant:\n%s", got, want)
	}
	if problems[0].Message != "third" {
		t.Error("Sorted must not reorder the receiver")
	}
}
