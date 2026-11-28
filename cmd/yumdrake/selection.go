package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rcss-labs/yamdrake/internal/cli"
	"github.com/rcss-labs/yamdrake/internal/spec"
)

// selectionGroup is one group's choice in the body of POST /jobs/{id}/runs.
// Services holds every selected service; "" means the group's Version.
type selectionGroup struct {
	Version  string            `json:"version"`
	Services map[string]string `json:"services"`
}

// buildSelection turns --set and --only into the server's selection, checking
// every name against the job's definition so that nothing is created for a
// typo. Every problem found is reported at once, with the valid names.
//
//   - --set GROUP=VERSION selects the whole group at VERSION.
//   - --set GROUP.SERVICE=VERSION overrides one service of the group.
//   - --only NAME,... narrows the selection to the named groups or services.
//   - A selected group with no version for some service is an error, unless
//     useLatest is set: then latest(group) gives its newest common version.
func buildSelection(job spec.Job, sets, only []string, useLatest bool, latest func(group string) (string, error)) (map[string]selectionGroup, error) {
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	groupIDs := make([]string, len(job.Groups))
	serviceSet := map[string]bool{}
	for i, g := range job.Groups {
		groupIDs[i] = g.ID
		for _, s := range g.Services {
			serviceSet[s] = true
		}
	}
	serviceNames := make([]string, 0, len(serviceSet))
	for s := range serviceSet {
		serviceNames = append(serviceNames, s)
	}

	groupVersion := map[string]string{}
	serviceVersion := map[string]map[string]string{}
	for _, set := range sets {
		key, version, ok := strings.Cut(set, "=")
		if !ok {
			problem("--set %q must look like GROUP=VERSION or GROUP.SERVICE=VERSION", set)
			continue
		}
		if version == "" {
			problem("--set %q needs a version after the equals sign", set)
			continue
		}
		groupID, serviceName, isService := strings.Cut(key, ".")
		if group, whole := job.Group(key); whole {
			groupVersion[group.ID] = version
			continue
		}
		group, found := job.Group(groupID)
		if !found {
			problem("unknown group %q in --set. Groups: %s", groupID, listNames(groupIDs))
			continue
		}
		if !isService || !slices.Contains(group.Services, serviceName) {
			problem("service %q is not in group %q. Services of %s: %s", serviceName, groupID, groupID, listNames(group.Services))
			continue
		}
		if serviceVersion[groupID] == nil {
			serviceVersion[groupID] = map[string]string{}
		}
		serviceVersion[groupID][serviceName] = version
	}

	onlyNames := map[string]bool{}
	for _, item := range only {
		for _, name := range strings.Split(item, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, isGroup := job.Group(name); !isGroup && !serviceSet[name] {
				problem("unknown name %q in --only. Groups: %s. Services: %s", name, listNames(groupIDs), listNames(serviceNames))
				continue
			}
			onlyNames[name] = true
		}
	}

	selection := map[string]selectionGroup{}
	for _, group := range job.Groups {
		var chosen []string
		for _, service := range group.Services {
			if len(onlyNames) == 0 || onlyNames[group.ID] || onlyNames[service] {
				chosen = append(chosen, service)
			}
		}
		if len(chosen) == 0 {
			continue
		}
		choice := selectionGroup{Version: groupVersion[group.ID], Services: map[string]string{}}
		var missing []string
		for _, service := range chosen {
			override := serviceVersion[group.ID][service]
			choice.Services[service] = override
			if override == "" && choice.Version == "" {
				missing = append(missing, service)
			}
		}
		if len(missing) > 0 && useLatest && len(problems) == 0 {
			version, err := latest(group.ID)
			if err != nil {
				problem("group %q: %v", group.ID, err)
			} else {
				choice.Version, missing = version, nil
			}
		}
		if len(missing) > 0 && !useLatest {
			problem("group %q has no version for %s: pass --set %s=VERSION (or --latest for the newest common version)",
				group.ID, strings.Join(missing, ", "), group.ID)
		}
		selection[group.ID] = choice
	}
	if len(problems) == 0 && len(selection) == 0 {
		problem("nothing is selected. Groups: %s. Services: %s", listNames(groupIDs), listNames(serviceNames))
	}
	if len(problems) > 0 {
		return nil, &cli.ExitError{Code: cli.ExitUsage, Message: strings.Join(problems, "\n")}
	}
	return selection, nil
}
