package spec

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const testEnvFile = "environments.yaml"

// validModel parses the valid fixtures fresh, so each test may mutate its copy.
func validModel(t *testing.T) ([]Environment, []Service) {
	t.Helper()
	envs, problems := ParseEnvironments(testEnvFile, readFixture(t, "valid/environments.yaml"))
	if len(problems) > 0 {
		t.Fatalf("environments: %v", problems)
	}
	var services []Service
	for _, name := range []string{"backend-api", "orders"} {
		file := "services/" + name + ".yaml"
		service, problems := ParseService(file, readFixture(t, "valid/services/"+name+".yaml"))
		if len(problems) > 0 {
			t.Fatalf("%s: %v", file, problems)
		}
		services = append(services, service)
	}
	return envs, services
}

func TestBuildInventoryAcceptsValidFixtures(t *testing.T) {
	envs, services := validModel(t)
	inv, problems := BuildInventory(testEnvFile, envs, services)
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	names := inv.ServiceNames()
	if !sort.StringsAreSorted(names) || !reflect.DeepEqual(names, []string{"backend-api", "orders"}) {
		t.Errorf("ServiceNames() = %v", names)
	}
	env, ok := inv.Environment("hetzner-01")
	if !ok || env.AWSAccount != "111111111111" {
		t.Errorf("Environment(hetzner-01) = %+v, %v", env, ok)
	}
	if _, ok := inv.Environment("nope"); ok {
		t.Error("Environment(nope) should not be found")
	}
	if inv.Services["orders"].Line == 0 {
		t.Error("service name line was not recorded")
	}
	if len(inv.Environments) != 5 || inv.Environments[0].Name != "hetzner-01" {
		t.Errorf("environments not in file order: %+v", inv.Environments)
	}
}

func TestBuildInventoryRejections(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(envs *[]Environment, services []Service) []Service
		wantFile string
		fragment string
		wantLine bool // the problem must carry a real line, not 0
	}{
		{"duplicate environment name",
			func(envs *[]Environment, s []Service) []Service { *envs = append(*envs, (*envs)[1]); return s },
			testEnvFile, `"dev-02"`, true},
		{"no environments",
			func(envs *[]Environment, s []Service) []Service { *envs = nil; return s },
			testEnvFile, "at least one environment", false},
		{"duplicate service name",
			func(_ *[]Environment, s []Service) []Service {
				// Name matches its file, so only the duplicate rule can fire.
				duplicate := s[0]
				duplicate.File = "zz/backend-api.yaml"
				duplicate.Environments = map[string]ServiceEnv{"prod-01": s[0].Environments["prod-01"]}
				return append(s, duplicate)
			},
			"zz/backend-api.yaml", "defined more than once", true},
		{"service name differs from file",
			func(_ *[]Environment, s []Service) []Service { s[1].Name = "orderz"; return s },
			"services/orders.yaml", `"orderz"`, true},
		{"unknown environment",
			func(_ *[]Environment, s []Service) []Service {
				s[1].Environments["staging-09"] = s[1].Environments["dev-02"]
				return s
			},
			"services/orders.yaml", `"staging-09"`, true},
		{"account mismatch",
			func(_ *[]Environment, s []Service) []Service {
				e := s[1].Environments["dev-02"]
				e.ECSService = "arn:aws:ecs:eu-central-1:999999999999:service/dev-02/orders"
				s[1].Environments["dev-02"] = e
				return s
			},
			"services/orders.yaml", `"999999999999"`, true},
		{"duplicate env file name",
			func(_ *[]Environment, s []Service) []Service {
				e := s[0].Environments["hetzner-01"]
				dup := e.EnvFiles[0]
				dup.Destination = "s3://hetzner-01-config/env/backend-api/other.env"
				e.EnvFiles = append(e.EnvFiles, dup)
				s[0].Environments["hetzner-01"] = e
				return s
			},
			"services/backend-api.yaml", `"backend-api.env"`, true},
		{"duplicate toml name",
			func(_ *[]Environment, s []Service) []Service {
				e := s[0].Environments["hetzner-01"]
				dup := e.Tomls[0]
				dup.Destination = "s3://hetzner-01-config/toml/backend-api/other.toml"
				e.Tomls = append(e.Tomls, dup)
				s[0].Environments["hetzner-01"] = e
				return s
			},
			"services/backend-api.yaml", `"backend-api.toml"`, true},
		{"same destination in two services",
			func(_ *[]Environment, s []Service) []Service {
				s[1].Environments["hetzner-01"] = ServiceEnv{
					ECSService: "arn:aws:ecs:eu-central-1:111111111111:service/hetzner-01/orders",
					Tomls: []Toml{{Name: "orders.toml", Source: "s3://b/orders.toml",
						Destination: "s3://hetzner-01-config/env/backend-api/backend-api.env", Line: 7}},
					Line: 5,
				}
				return s
			},
			"services/orders.yaml", `"s3://hetzner-01-config/env/backend-api/backend-api.env"`, true},
		{"service without environments",
			func(_ *[]Environment, s []Service) []Service { s[1].Environments = nil; return s },
			"services/orders.yaml", `"orders"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envs, services := validModel(t)
			services = tt.mutate(&envs, services)
			_, problems := BuildInventory(testEnvFile, envs, services)
			if len(problems) == 0 {
				t.Fatal("expected a problem, got none")
			}
			for _, p := range problems {
				if p.File == tt.wantFile && strings.Contains(p.Message, tt.fragment) {
					if tt.wantLine && p.Line == 0 {
						t.Errorf("problem has no line: %v", p)
					}
					return
				}
			}
			t.Errorf("no problem in %s mentioning %s; got %v", tt.wantFile, tt.fragment, problems)
		})
	}
}

func TestBuildInventoryReportsAllProblemsAtOnce(t *testing.T) {
	envs, services := validModel(t)
	services[0].Environments["staging-09"] = services[0].Environments["hetzner-01"]
	e := services[1].Environments["dev-02"]
	e.ECSService = "arn:aws:ecs:eu-central-1:999999999999:service/dev-02/orders"
	services[1].Environments["dev-02"] = e

	_, problems := BuildInventory(testEnvFile, envs, services)
	if len(problems) != 2 {
		t.Fatalf("want 2 problems, got %v", problems)
	}
	if problems[0].File != "services/backend-api.yaml" || problems[1].File != "services/orders.yaml" {
		t.Errorf("problems not sorted by file: %v", problems)
	}
	if problems[0].Line == 0 || problems[1].Line == 0 {
		t.Errorf("problems should carry the service environment line: %v", problems)
	}
}

func TestBuildInventoryHandlesEmptyInput(t *testing.T) {
	_, problems := BuildInventory(testEnvFile, nil, nil)
	if len(problems) != 1 || problems[0].File != testEnvFile {
		t.Errorf("empty input: %v", problems)
	}
	_, problems = BuildInventory(testEnvFile, []Environment{{Name: "a", AWSAccount: "111111111111"}},
		[]Service{{File: "services/x.yaml", Name: "x"}})
	if len(problems) != 1 {
		t.Errorf("service with nil environments: %v", problems)
	}
}

func TestInventoryJSONRoundTrip(t *testing.T) {
	envs, services := validModel(t)
	inv, _ := BuildInventory(testEnvFile, envs, services)
	data, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	var back Inventory
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, back) {
		t.Errorf("round trip changed the inventory")
	}
	if !strings.Contains(string(data), `"environments"`) || !strings.Contains(string(data), `"services"`) {
		t.Errorf("missing snake_case keys: %s", data)
	}
}
