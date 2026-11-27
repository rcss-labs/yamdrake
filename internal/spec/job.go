package spec

import (
	"fmt"
	"sort"
	"strings"
)

// Group looks a group up by id.
func (j Job) Group(id string) (Group, bool) {
	for _, group := range j.Groups {
		if group.ID == id {
			return group, true
		}
	}
	return Group{}, false
}

// Step looks a step up by name; with duplicate names the first one wins.
func (j Job) Step(name string) (Step, bool) {
	for _, step := range j.Steps {
		if step.Name == name {
			return step, true
		}
	}
	return Step{}, false
}

// ServicesIn maps every group id to the services of that group that have a
// block for the environment, in group order. A group with none maps to an
// empty, non-nil slice, so every group id is always a key.
func (j Job) ServicesIn(envName string, inv Inventory) map[string][]string {
	result := make(map[string][]string, len(j.Groups))
	for _, group := range j.Groups {
		services := []string{}
		for _, name := range group.Services {
			if service, ok := inv.Services[name]; ok {
				if _, inEnvironment := service.Environments[envName]; inEnvironment {
					services = append(services, name)
				}
			}
		}
		result[group.ID] = services
	}
	return result
}

// StepOrder returns step names in dependency layers: every step appears in the
// first layer after all the steps it needs, and each layer is sorted by name.
// It is meant for valid jobs. If the graph cannot be fully layered (a cycle or
// a need on an unknown step) it returns nil instead of a partial order.
func (j Job) StepOrder() [][]string {
	needs := map[string][]string{}
	for _, step := range j.Steps {
		if _, seen := needs[step.Name]; seen {
			continue // first definition wins
		}
		var names []string
		for _, need := range step.Needs {
			names = append(names, need.Step)
		}
		needs[step.Name] = names
	}

	placed := map[string]bool{}
	var layers [][]string
	for len(placed) < len(needs) {
		var layer []string
		for name, deps := range needs {
			if placed[name] {
				continue
			}
			ready := true
			for _, dep := range deps {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				layer = append(layer, name)
			}
		}
		if len(layer) == 0 {
			return nil
		}
		sort.Strings(layer)
		for _, name := range layer {
			placed[name] = true
		}
		layers = append(layers, layer)
	}
	return layers
}

// ValidateJob checks a parsed job against the inventory and checks its step
// graph. It reports every problem at once, sorted.
func ValidateJob(job Job, inv Inventory) Problems {
	var problems Problems
	add := func(line int, format string, args ...any) {
		problems = append(problems, Problem{File: job.File, Line: line, Message: fmt.Sprintf(format, args...)})
	}

	validateLabels(job, add)
	validateGroups(job, inv, add)
	validateSteps(job, add)
	return problems.Sorted()
}

type addProblem func(line int, format string, args ...any)

func validateLabels(job Job, add addProblem) {
	if len(job.Labels) == 0 {
		add(job.LabelsLine, "environment.labels must list at least one label")
	}
	seen := map[string]bool{}
	for _, label := range job.Labels {
		if seen[label] {
			add(job.LabelsLine, "environment label %q is listed more than once", label)
		}
		seen[label] = true
	}
	if job.Approval != nil {
		for _, label := range job.Approval.Labels {
			if !contains(job.Labels, label) {
				add(job.Approval.Line, "approval.when.labels contains %q, which is not in environment.labels", label)
			}
		}
	}
}

func validateGroups(job Job, inv Inventory, add addProblem) {
	if len(job.Groups) == 0 {
		add(0, "job defines no groups: at least one group is required")
	}

	// Permitted environments are the inventory's environments whose label the job allows.
	var permitted []string
	for _, environment := range inv.Environments {
		if contains(job.Labels, environment.Label) {
			permitted = append(permitted, environment.Name)
		}
	}

	groupIDs := map[string]bool{}
	serviceGroup := map[string]string{} // service name -> group that first lists it
	for _, group := range job.Groups {
		if groupIDs[group.ID] {
			add(group.Line, "group %q is defined more than once", group.ID)
		}
		groupIDs[group.ID] = true
		if len(group.Services) == 0 {
			add(group.Line, "group %q lists no services", group.ID)
			continue
		}

		allKnown := true
		for _, name := range group.Services {
			if _, ok := inv.Services[name]; !ok {
				add(group.Line, "service %q in group %q is not in the inventory", name, group.ID)
				allKnown = false
			}
			if first, dup := serviceGroup[name]; dup {
				if first == group.ID {
					add(group.Line, "service %q is listed more than once in group %q", name, group.ID)
				} else {
					add(group.Line, "service %q appears more than once in the job (groups %q and %q)", name, first, group.ID)
				}
			} else {
				serviceGroup[name] = group.ID
			}
		}

		// Skip when labels are empty or a service is unknown: those are reported already.
		if allKnown && len(job.Labels) > 0 && !deployableIn(group, permitted, inv) {
			add(group.Line, "group %q has no service in any environment permitted by the job labels %v", group.ID, job.Labels)
		}
	}
}

func deployableIn(group Group, environments []string, inv Inventory) bool {
	for _, name := range group.Services {
		for _, environment := range environments {
			if _, ok := inv.Services[name].Environments[environment]; ok {
				return true
			}
		}
	}
	return false
}

func validateSteps(job Job, add addProblem) {
	if len(job.Steps) == 0 {
		add(0, "job defines no steps: at least one step is required")
	}

	first := map[string]Step{}
	for _, step := range job.Steps {
		if _, dup := first[step.Name]; dup {
			add(step.Line, "step %q is defined more than once", step.Name)
			continue
		}
		first[step.Name] = step
	}

	for _, step := range job.Steps {
		if _, ok := job.Group(step.Group); !ok {
			add(step.Line, "step %q uses group %q, which is not defined", step.Name, step.Group)
		}
		for _, need := range step.Needs {
			target, exists := first[need.Step]
			switch {
			case need.Step == step.Name:
				add(need.Line, "step %q needs itself", step.Name)
			case !exists:
				add(need.Line, "step %q needs %q, which is not a step of this job", step.Name, need.Step)
			case need.Scope == "service" && target.Group != step.Group:
				add(need.Line, "needs scope %q requires the same group, but step %q uses group %q and %q uses group %q",
					need.Scope, step.Name, step.Group, target.Name, target.Group)
			}
		}
	}

	reportCycles(first, add)
}

// reportCycles finds strongly connected components of the needs graph (Tarjan)
// and reports each component of two or more steps once. Self-needs and needs
// on unknown steps are not edges here; they have their own problems.
func reportCycles(steps map[string]Step, add addProblem) {
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sort.Strings(names)

	index := map[string]int{}
	lowlink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	counter := 0

	var connect func(name string)
	connect = func(name string) {
		index[name] = counter
		lowlink[name] = counter
		counter++
		stack = append(stack, name)
		onStack[name] = true

		for _, need := range steps[name].Needs {
			if _, known := steps[need.Step]; !known || need.Step == name {
				continue
			}
			if _, visited := index[need.Step]; !visited {
				connect(need.Step)
				lowlink[name] = min(lowlink[name], lowlink[need.Step])
			} else if onStack[need.Step] {
				lowlink[name] = min(lowlink[name], index[need.Step])
			}
		}

		if lowlink[name] != index[name] {
			return
		}
		var component []string
		for {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[top] = false
			component = append(component, top)
			if top == name {
				break
			}
		}
		if len(component) < 2 {
			return
		}
		sort.Strings(component)
		quoted := make([]string, len(component))
		line := steps[component[0]].Line
		for i, member := range component {
			quoted[i] = fmt.Sprintf("%q", member)
			if memberLine := steps[member].Line; memberLine < line {
				line = memberLine
			}
		}
		add(line, "steps form a dependency cycle: %s", strings.Join(quoted, ", "))
	}
	for _, name := range names {
		if _, visited := index[name]; !visited {
			connect(name)
		}
	}
}
