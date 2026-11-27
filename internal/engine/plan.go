// Package engine creates and advances runs.
package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/rcss-labs/yamdrake/internal/spec"
	"github.com/rcss-labs/yamdrake/internal/store"
)

// GroupSelection is what a person chose in one group of the job.
type GroupSelection struct {
	Version string
	// Services maps a selected service to its version override; "" means the
	// group's Version. A service that is absent is not selected.
	Services map[string]string
}

// Selection maps a group id to the choice made in that group.
type Selection map[string]GroupSelection

// Built is a run's plan, ready for store.InsertRun.
//
// Legs refer to steps by index: a step leg's StepID points at the position in
// Steps (and in the slice given to store.InsertRun), because step ids do not
// exist until they are inserted. InsertRun translates the index to the id.
type Built struct {
	Steps []store.RunStep
	// Legs holds one "resolve" leg in state queued (StepID nil), then one
	// pending "step" leg per step per selected service of the step's group.
	Legs []store.Leg
	// Resources are the lock resources, sorted and unique.
	Resources []string
}

// Build turns a selection into the steps, legs and lock resources of a run. It
// reports every problem it finds; when there is any, Built is the zero value.
// A step whose group has no selected service is left out, and needs pointing
// at it are removed from the steps that remain.
func Build(job spec.Job, inv spec.Inventory, envName string, sel Selection) (Built, spec.Problems) {
	var problems spec.Problems
	add := func(format string, args ...any) {
		problems = append(problems, spec.Problem{File: job.File, Message: fmt.Sprintf(format, args...)})
	}

	environment, ok := inv.Environment(envName)
	if !ok {
		add("environment %q does not exist", envName)
		return Built{}, problems
	}
	if !slices.Contains(job.Labels, environment.Label) {
		add("environment %q has label %q, which job %q does not allow", envName, environment.Label, job.ID)
		return Built{}, problems
	}

	// selected holds, per group id, its chosen services in group order with their versions.
	type chosen struct{ service, version string }
	selected := map[string][]chosen{}
	total := 0

	groupIDs := make([]string, 0, len(sel))
	for id := range sel {
		groupIDs = append(groupIDs, id)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		group, ok := job.Group(groupID)
		if !ok {
			add("group %q is not a group of job %q", groupID, job.ID)
			continue
		}
		choice := sel[groupID]
		for _, name := range sortedKeys(choice.Services) {
			if !slices.Contains(group.Services, name) {
				add("service %q is not in group %q", name, groupID)
			}
		}
		for _, name := range group.Services { // group order
			override, isSelected := choice.Services[name]
			if !isSelected {
				continue
			}
			if _, inEnvironment := inv.Services[name].Environments[envName]; !inEnvironment {
				add("service %q does not exist in environment %q", name, envName)
				continue
			}
			version := override
			if version == "" {
				version = choice.Version
			}
			if version == "" {
				add("service %q has no version: set one for the service or for group %q", name, groupID)
				continue
			}
			selected[groupID] = append(selected[groupID], chosen{name, version})
			total++
		}
	}
	if len(problems) == 0 && total == 0 {
		add("no service is selected")
	}
	if len(problems) > 0 {
		return Built{}, problems.Sorted()
	}

	// Keep the steps whose group has a selected service, in job order.
	kept := map[string]bool{}
	for _, step := range job.Steps {
		if len(selected[step.Group]) > 0 {
			kept[step.Name] = true
		}
	}
	var built Built
	built.Legs = append(built.Legs, store.Leg{Kind: "resolve", State: "queued"})
	for _, step := range job.Steps {
		if !kept[step.Name] {
			continue
		}
		needs := []spec.Need{}
		for _, need := range step.Needs {
			if kept[need.Step] {
				needs = append(needs, need)
			}
		}
		encoded, err := json.Marshal(needs)
		if err != nil { // a slice of plain structs cannot fail to encode
			panic(fmt.Sprintf("encode needs: %v", err))
		}
		index := int64(len(built.Steps))
		built.Steps = append(built.Steps, store.RunStep{
			Name: step.Name, Use: step.Use, GroupID: step.Group, Position: int(index),
			MaxParallel: step.MaxParallel, Timeout: step.Timeout, Needs: encoded,
		})
		for _, c := range selected[step.Group] {
			stepIndex := index
			built.Legs = append(built.Legs, store.Leg{
				StepID: &stepIndex, Kind: "step", Service: c.service, Version: c.version, State: "pending",
			})
		}
	}

	resources := map[string]bool{}
	for _, chosenServices := range selected {
		for _, c := range chosenServices {
			serviceEnv := inv.Services[c.service].Environments[envName]
			if serviceEnv.ECSService != "" {
				resources[serviceEnv.ECSService] = true
			}
			for _, file := range serviceEnv.EnvFiles {
				resources[file.Destination] = true
			}
			for _, toml := range serviceEnv.Tomls {
				resources[toml.Destination] = true
			}
		}
	}
	built.Resources = sortedKeys(resources)
	return built, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
