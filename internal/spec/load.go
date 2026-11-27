package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// LoadDir reads <inventoryDir>/environments.yaml and every
// <inventoryDir>/services/*.yaml, builds the inventory, then parses each job
// file. Jobs are checked against the inventory only when the inventory itself
// is free of problems, because a broken inventory would make every job look
// wrong too. Problems carry the paths as given (inventoryDir joined with the
// file name, or the job path unchanged) and are sorted. A missing
// environments.yaml is a problem, not an error return.
func LoadDir(inventoryDir string, jobFiles []string) (Inventory, []Job, Problems) {
	var problems Problems
	readFailure := func(path string, err error) {
		message := fmt.Sprintf("cannot read file: %v", err)
		if errors.Is(err, fs.ErrNotExist) {
			message = "file does not exist"
		}
		problems = append(problems, Problem{File: path, Message: message})
	}

	var inv Inventory
	envPath := filepath.Join(inventoryDir, "environments.yaml")
	if data, err := readFile(envPath); err != nil {
		readFailure(envPath, err)
	} else {
		environments, parseProblems := ParseEnvironments(envPath, data)
		problems = append(problems, parseProblems...)

		servicePaths, err := filepath.Glob(filepath.Join(inventoryDir, "services", "*.yaml"))
		if err != nil {
			readFailure(filepath.Join(inventoryDir, "services"), err)
		}
		sort.Strings(servicePaths)
		var services []Service
		for _, path := range servicePaths {
			data, err := readFile(path)
			if err != nil {
				readFailure(path, err)
				continue
			}
			service, parseProblems := ParseService(path, data)
			problems = append(problems, parseProblems...)
			services = append(services, service)
		}

		var inventoryProblems Problems
		inv, inventoryProblems = BuildInventory(envPath, environments, services)
		problems = append(problems, inventoryProblems...)
	}
	inventoryValid := len(problems) == 0

	var jobs []Job
	for _, path := range jobFiles {
		data, err := readFile(path)
		if err != nil {
			readFailure(path, err)
			continue
		}
		job, parseProblems := ParseJob(path, data)
		problems = append(problems, parseProblems...)
		if len(parseProblems) == 0 && inventoryValid {
			problems = append(problems, ValidateJob(job, inv)...)
		}
		jobs = append(jobs, job)
	}
	return inv, jobs, problems.Sorted()
}

// readFile reads a file the user named on the command line; reading
// caller-chosen paths is the purpose of LoadDir.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // the path is the user's input by design
}
