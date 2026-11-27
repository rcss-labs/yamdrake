// Package spec defines the yumdrake YAML model (environments, services and
// jobs) and parses it strictly. It is pure: no I/O, no database, no network.
// Parsing checks shape only; checks that need another file live elsewhere.
package spec

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Problem is one thing wrong with a file, positioned at a 1-based line.
// Line is 0 when the problem is about the whole file.
type Problem struct {
	File    string
	Line    int
	Message string
}

// Problems is a list of problems; it also implements error.
type Problems []Problem

// Error renders one "file:line: message" per line.
func (p Problems) Error() string {
	lines := make([]string, len(p))
	for i, problem := range p {
		if problem.Line > 0 {
			lines[i] = fmt.Sprintf("%s:%d: %s", problem.File, problem.Line, problem.Message)
		} else {
			lines[i] = fmt.Sprintf("%s: %s", problem.File, problem.Message)
		}
	}
	return strings.Join(lines, "\n")
}

// Sorted returns a copy ordered by file, then line, then message.
func (p Problems) Sorted() Problems {
	sorted := append(Problems(nil), p...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Message < b.Message
	})
	return sorted
}

type Environment struct {
	Name       string
	Label      string
	AWSAccount string
	Line       int
}

type EnvFile struct {
	Name        string
	Source      string
	Destination string
	Line        int
}

type Toml struct {
	Name        string
	Source      string
	Destination string
	Line        int
}

type VersionRule struct {
	Pattern string
	Sort    string
	Limit   int
}

type ServiceEnv struct {
	ECSService    string
	ECRRepository string
	EnvFiles      []EnvFile
	Tomls         []Toml
	Line          int
}

type Service struct {
	File         string
	Name         string
	Versions     VersionRule
	Environments map[string]ServiceEnv
}

// Need is a dependency of a step. Scope is "step" or "service".
type Need struct {
	Step  string
	Scope string
}

type Step struct {
	Name, Use, Group string
	MaxParallel      int           // 0 means not set
	Timeout          time.Duration // 0 means not set
	Needs            []Need
	Line             int
}

// Group is a named list of services; Job.Groups keeps file order.
type Group struct {
	ID       string
	Label    string
	Services []string
	Line     int
}

type Approval struct {
	Labels  []string
	Expires time.Duration
}

type Job struct {
	File         string
	ID, Title    string
	Labels       []string
	Groups       []Group
	Approval     *Approval
	SlackChannel string
	Steps        []Step
}
