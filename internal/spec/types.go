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

// All model types below are stored as JSON by later tasks: field names are
// fixed by snake_case json tags, and time.Duration values stay nanosecond integers.

// Problem is one thing wrong with a file, positioned at a 1-based line.
// Line is 0 when the problem is about the whole file.
type Problem struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
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
	Name       string `json:"name"`
	Label      string `json:"label"`
	AWSAccount string `json:"aws_account"`
	Line       int    `json:"line"`
}

type EnvFile struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Line        int    `json:"line"`
}

type Toml struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Line        int    `json:"line"`
}

type VersionRule struct {
	Pattern string `json:"pattern"`
	Sort    string `json:"sort"`
	Limit   int    `json:"limit"`
}

type ServiceEnv struct {
	ECSService    string    `json:"ecs_service"`
	ECRRepository string    `json:"ecr_repository"`
	EnvFiles      []EnvFile `json:"env_files"`
	Tomls         []Toml    `json:"tomls"`
	Line          int       `json:"line"`
}

type Service struct {
	File         string                `json:"file"`
	Name         string                `json:"name"`
	Versions     VersionRule           `json:"versions"`
	Environments map[string]ServiceEnv `json:"environments"`
}

// Need is a dependency of a step. Scope is "step" or "service".
type Need struct {
	Step  string `json:"step"`
	Scope string `json:"scope"`
}

type Step struct {
	Name        string        `json:"name"`
	Use         string        `json:"use"`
	Group       string        `json:"group"`
	MaxParallel int           `json:"max_parallel"` // 0 means not set
	Timeout     time.Duration `json:"timeout"`      // 0 means not set
	Needs       []Need        `json:"needs"`
	Line        int           `json:"line"`
}

// Group is a named list of services; Job.Groups keeps file order.
type Group struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Services []string `json:"services"`
	Line     int      `json:"line"`
}

type Approval struct {
	Labels  []string      `json:"labels"`
	Expires time.Duration `json:"expires"`
}

type Job struct {
	File         string    `json:"file"`
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Labels       []string  `json:"labels"`
	Groups       []Group   `json:"groups"`
	Approval     *Approval `json:"approval"`
	SlackChannel string    `json:"slack_channel"`
	Steps        []Step    `json:"steps"`
}
