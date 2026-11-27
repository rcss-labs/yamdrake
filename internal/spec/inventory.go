package spec

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Inventory is the validated set of environments and services. It is stored
// as JSON, so fields carry snake_case tags like the other model types.
type Inventory struct {
	Environments []Environment      `json:"environments"` // in file order
	Services     map[string]Service `json:"services"`
}

// Environment looks an environment up by name.
func (inv Inventory) Environment(name string) (Environment, bool) {
	for _, environment := range inv.Environments {
		if environment.Name == name {
			return environment, true
		}
	}
	return Environment{}, false
}

// ServiceNames returns the service names in sorted order.
func (inv Inventory) ServiceNames() []string {
	names := make([]string, 0, len(inv.Services))
	for name := range inv.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildInventory checks the rules that span files: environment and service
// uniqueness, file-name agreement, known environments, account ids and
// destination clashes. It reports every problem at once, sorted. The returned
// inventory holds the first definition of any duplicated name.
func BuildInventory(envFile string, envs []Environment, services []Service) (Inventory, Problems) {
	var problems Problems
	add := func(file string, line int, format string, args ...any) {
		problems = append(problems, Problem{File: file, Line: line, Message: fmt.Sprintf(format, args...)})
	}

	inv := Inventory{Services: map[string]Service{}}
	if len(envs) == 0 {
		add(envFile, 0, "no environments defined: at least one environment is required")
	}
	known := map[string]Environment{}
	for _, environment := range envs {
		if first, dup := known[environment.Name]; dup {
			add(envFile, environment.Line, "environment %q is defined more than once (first on line %d)", environment.Name, first.Line)
			continue
		}
		known[environment.Name] = environment
		inv.Environments = append(inv.Environments, environment)
	}

	// Process services in file order so "first definition wins" is stable.
	ordered := append([]Service(nil), services...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].File < ordered[j].File })

	// destinations maps environment name -> S3 URI -> where it was first used.
	destinations := map[string]map[string]string{}
	for _, service := range ordered {
		expected := strings.TrimSuffix(path.Base(service.File), ".yaml")
		if service.Name != expected {
			add(service.File, 0, "service name %q must equal the file name %q", service.Name, expected)
		}
		if first, dup := inv.Services[service.Name]; dup {
			add(service.File, 0, "service %q is defined more than once (also in %s)", service.Name, first.File)
		} else {
			inv.Services[service.Name] = service
		}
		if len(service.Environments) == 0 {
			add(service.File, 0, "service %q has no environments", service.Name)
			continue
		}

		envNames := make([]string, 0, len(service.Environments))
		for name := range service.Environments {
			envNames = append(envNames, name)
		}
		sort.Strings(envNames)
		for _, envName := range envNames {
			serviceEnv := service.Environments[envName]
			environment, ok := known[envName]
			if !ok {
				add(service.File, serviceEnv.Line, "service %q uses unknown environment %q", service.Name, envName)
				continue
			}
			if account, parsed := ecsAccount(serviceEnv.ECSService); parsed && account != environment.AWSAccount {
				add(service.File, serviceEnv.Line, "ecs_service %q is in account %q but environment %q is account %q",
					serviceEnv.ECSService, account, envName, environment.AWSAccount)
			}

			seenEnvFiles := map[string]bool{}
			for _, file := range serviceEnv.EnvFiles {
				if seenEnvFiles[file.Name] {
					add(service.File, file.Line, "env file name %q is used more than once in %s/%s", file.Name, service.Name, envName)
				}
				seenEnvFiles[file.Name] = true
				claim(destinations, add, service.File, file.Line, envName, file.Destination)
			}
			seenTomls := map[string]bool{}
			for _, toml := range serviceEnv.Tomls {
				if seenTomls[toml.Name] {
					add(service.File, toml.Line, "toml name %q is used more than once in %s/%s", toml.Name, service.Name, envName)
				}
				seenTomls[toml.Name] = true
				claim(destinations, add, service.File, toml.Line, envName, toml.Destination)
			}
		}
	}
	return inv, problems.Sorted()
}

// claim records a destination for an environment and reports a clash.
func claim(destinations map[string]map[string]string, add func(string, int, string, ...any),
	file string, line int, envName, destination string) {
	if destination == "" {
		return
	}
	if destinations[envName] == nil {
		destinations[envName] = map[string]string{}
	}
	if first, dup := destinations[envName][destination]; dup {
		add(file, line, "destination %q in environment %q is already used at %s", destination, envName, first)
		return
	}
	destinations[envName][destination] = fmt.Sprintf("%s:%d", file, line)
}

// ecsAccount extracts the account id from arn:aws:ecs:<region>:<account>:service/...
func ecsAccount(arn string) (string, bool) {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || parts[0] != "arn" {
		return "", false
	}
	return parts[4], true
}
