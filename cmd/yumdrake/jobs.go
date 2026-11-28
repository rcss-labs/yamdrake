package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/rcss-labs/yamdrake/internal/cli"
	"github.com/rcss-labs/yamdrake/internal/spec"
)

const jobsUsage = `usage: yumdrake jobs <command>

Commands:
  list [--folder NAME]   List the jobs.
  show JOB               Show a job's groups, steps and problems.

Example:
  yumdrake jobs list
`

const jobsListHelp = `usage: yumdrake jobs list [--folder NAME] [-o json]

List the jobs: id, title, folder, number of services, which environment labels
need approval, and the last run.

Example:
  yumdrake jobs list --folder Platform
`

const jobsShowHelp = `usage: yumdrake jobs show JOB [-o json]

Show a job: its groups and services, its steps with what they wait for, and
any problems found in its YAML. JOB is the job id from the YAML.

Example:
  yumdrake jobs show roll-backends
`

const versionsHelp = `usage: yumdrake versions JOB --env ENV [--group GROUP | --service SERVICE] [-o json]
       yumdrake versions running [--service NAME] [--env ENV] [-o json]

Without --group or --service: the tags every service of each group has in
common, newest first. With --service: that service's tags with digest and push
time. "versions running" shows what is deployed now in every environment.

Example:
  yumdrake versions roll-backends --env dev-01
  yumdrake versions running --service billing
`

const versionsRunningHelp = `usage: yumdrake versions running [--service NAME] [--env ENV] [-o json]

Show the running versions: one row per service, one column per environment.
Needs a server that has the running-versions route.

Example:
  yumdrake versions running --env prod-01
`

func jobsCmd(args []string, stdout, stderr io.Writer, e env) int {
	return subcommands{"list": jobsList, "show": jobsShow}.dispatch("jobs", jobsUsage, args, stdout, stderr, e)
}

// listJobs fetches every job.
func (s *session) listJobs() ([]jobSummary, error) {
	var out struct {
		Jobs []jobSummary `json:"jobs"`
	}
	if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/jobs", nil, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

// findJob resolves the job id from the YAML to the server's job. An unknown
// id is an error that names the closest ids.
func (s *session) findJob(jobID string) (jobSummary, error) {
	jobs, err := s.listJobs()
	if err != nil {
		return jobSummary{}, err
	}
	var ids []string
	for _, j := range jobs {
		if j.JobID == jobID {
			return j, nil
		}
		ids = append(ids, j.JobID)
	}
	return jobSummary{}, usageError("unknown job %q. %s", jobID, nearby(jobID, ids))
}

// nearby describes the ids that look like what was typed, else all of them.
func nearby(typed string, ids []string) string {
	var close []string
	for _, id := range ids {
		if strings.Contains(id, typed) || strings.Contains(typed, id) || editDistance(id, typed) <= 3 {
			close = append(close, id)
		}
	}
	if len(close) > 0 {
		return "Did you mean: " + listNames(close) + "?"
	}
	if len(ids) == 0 {
		return "No jobs are registered."
	}
	return "Known jobs: " + listNames(ids)
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func jobsList(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("jobs list", jobsListHelp)
	var folder string
	flags.StringVar(&folder, "folder", "", "only jobs in this folder")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 0, "jobs list takes no arguments"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	jobs, err := s.listJobs()
	if err != nil {
		return fail(stderr, err)
	}
	if folder != "" {
		if err := s.checkFolder(folder); err != nil {
			return fail(stderr, err)
		}
		kept := jobs[:0]
		for _, j := range jobs {
			if j.Folder == folder {
				kept = append(kept, j)
			}
		}
		jobs = kept
	}
	if err := s.attachLastRuns(jobs); err != nil {
		return fail(stderr, err)
	}
	if jobs == nil {
		jobs = []jobSummary{}
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, map[string]any{"jobs": jobs})
		return exitOK
	}
	if len(jobs) == 0 {
		s.printf("No jobs.\n")
		return exitOK
	}
	table := cli.NewTable(stdout)
	table.Row("JOB", "TITLE", "FOLDER", "SERVICES", "APPROVAL", "LAST RUN")
	for _, j := range jobs {
		approval := "-"
		if len(j.ApprovalLabels) > 0 {
			approval = strings.Join(j.ApprovalLabels, ",")
		}
		last := "-"
		if j.LastRun != nil {
			last = fmt.Sprintf("#%d %s", j.LastRun.ID, j.LastRun.State)
		}
		title := j.Title
		if !j.Runnable {
			title += " (not runnable: has problems)"
		}
		table.Row(j.JobID, title, j.Folder, fmt.Sprint(j.Services), approval, last)
	}
	_ = table.Flush()
	return exitOK
}

func (s *session) checkFolder(name string) error {
	var out struct {
		Folders []struct {
			Name string `json:"name"`
		} `json:"folders"`
	}
	if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/folders", nil, &out); err != nil {
		return err
	}
	var names []string
	for _, f := range out.Folders {
		if f.Name == name {
			return nil
		}
		names = append(names, f.Name)
	}
	return usageError("unknown folder %q. Folders: %s", name, listNames(names))
}

// attachLastRuns fills LastRun from the newest runs (the list is newest first).
func (s *session) attachLastRuns(jobs []jobSummary) error {
	var out struct {
		Runs []runSummary `json:"runs"`
	}
	if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/runs?limit=100", nil, &out); err != nil {
		return err
	}
	last := map[int64]*lastRun{}
	for _, r := range out.Runs {
		if _, seen := last[r.JobSourceID]; !seen {
			last[r.JobSourceID] = &lastRun{ID: r.ID, State: r.State}
		}
	}
	for i := range jobs {
		jobs[i].LastRun = last[jobs[i].ID]
	}
	return nil
}

func jobsShow(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("jobs show", jobsShowHelp)
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "jobs show needs one JOB"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	job, err := s.findJob(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d", job.ID), nil, &raw); err != nil {
		return fail(stderr, err)
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	var detail jobDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
	}
	s.printf("%s  %s\n", detail.JobID, detail.Title)
	s.printf("Folder: %s\n", orDash(detail.Folder))
	def := detail.Definition
	if def == nil {
		s.printf("The definition has not been read successfully yet.\n")
	} else {
		s.printf("Environment labels: %s\n", strings.Join(def.Labels, ", "))
		if def.Approval != nil && len(def.Approval.Labels) > 0 {
			s.printf("Approval needed for: %s (expires after %s)\n", strings.Join(def.Approval.Labels, ", "), def.Approval.Expires)
		}
		s.printf("\nGroups:\n")
		for _, g := range def.Groups {
			s.printf("  %s  %s\n    services: %s\n", g.ID, g.Label, strings.Join(g.Services, ", "))
		}
		s.printf("\nSteps:\n")
		for _, st := range def.Steps {
			s.printf("  %s  (%s, group %s)\n", st.Name, st.Use, st.Group)
			for _, n := range st.Needs {
				scope := "whole step"
				if n.Scope == "service" {
					scope = "per service"
				}
				s.printf("    needs %s (%s)\n", n.Step, scope)
			}
		}
	}
	if len(detail.Problems) > 0 {
		s.printf("\nProblems:\n")
		for _, p := range detail.Problems {
			s.printf("  %s\n", strings.TrimSpace(spec.Problems{p}.Error()))
		}
	}
	return exitOK
}

func versionsCmd(args []string, stdout, stderr io.Writer, e env) int {
	if len(args) > 0 && args[0] == "running" {
		return versionsRunning(args[1:], stdout, stderr, e)
	}
	flags := newAPIFlags("versions", versionsHelp)
	var envName, group, service string
	flags.StringVar(&envName, "env", "", "environment")
	flags.StringVar(&group, "group", "", "only this group")
	flags.StringVar(&service, "service", "", "only this service")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 1, "versions needs one JOB"); !ok {
		return code
	}
	if envName == "" {
		_, _ = fmt.Fprintf(stderr, "yumdrake: versions needs --env ENV\n\n%s", versionsHelp)
		return exitUsage
	}
	if group != "" && service != "" {
		_, _ = fmt.Fprintf(stderr, "yumdrake: use --group or --service, not both\n\n%s", versionsHelp)
		return exitUsage
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	job, err := s.findJob(positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	var raw json.RawMessage
	path := fmt.Sprintf("/api/v1/jobs/%d/versions?environment=%s", job.ID, url.QueryEscape(envName))
	if err := s.client.Do(s.ctx, http.MethodGet, path, nil, &raw); err != nil {
		return fail(stderr, unsupportedOn404(err, "version lists"))
	}
	var versions jobVersions
	if err := json.Unmarshal(raw, &versions); err != nil {
		return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
	}
	groupIDs := make([]string, 0, len(versions.Groups))
	var serviceNames []string
	for id, g := range versions.Groups {
		groupIDs = append(groupIDs, id)
		for name := range g.Services {
			serviceNames = append(serviceNames, name)
		}
		for name := range g.Unavailable {
			serviceNames = append(serviceNames, name)
		}
	}
	sort.Strings(groupIDs)
	switch {
	case group != "":
		if _, ok := versions.Groups[group]; !ok {
			return fail(stderr, usageError("unknown group %q. Groups: %s", group, listNames(groupIDs)))
		}
		groupIDs = []string{group}
	case service != "":
		if !containsString(serviceNames, service) {
			return fail(stderr, usageError("unknown service %q. Services: %s", service, listNames(serviceNames)))
		}
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	if service != "" {
		return printServiceVersions(s, versions, groupIDs, service)
	}
	for _, id := range groupIDs {
		g := versions.Groups[id]
		common := "(none)"
		if len(g.Common) > 0 {
			common = strings.Join(g.Common, ", ")
		}
		s.printf("%s: %s\n", id, common)
		if group != "" {
			for _, name := range sortedKeys(g.Services) {
				s.printf("  %s: %s\n", name, tagList(g.Services[name]))
			}
		}
		for _, name := range sortedKeys(g.Unavailable) {
			s.printf("  %s: %s\n", name, g.Unavailable[name])
		}
	}
	return exitOK
}

func printServiceVersions(s *session, versions jobVersions, groupIDs []string, service string) int {
	for _, id := range groupIDs {
		g := versions.Groups[id]
		if reason, bad := g.Unavailable[service]; bad {
			s.printf("%s: unavailable: %s\n", service, reason)
			return exitOK
		}
		entries, ok := g.Services[service]
		if !ok {
			continue
		}
		table := cli.NewTable(s.stdout)
		table.Row("TAG", "DIGEST", "PUSHED")
		for _, v := range entries {
			table.Row(v.Tag, shortDigest(v.Digest), formatTime(v.PushedAt))
		}
		_ = table.Flush()
		return exitOK
	}
	return exitOK
}

func tagList(entries []versionEntry) string {
	tags := make([]string, len(entries))
	for i, v := range entries {
		tags[i] = v.Tag
	}
	if len(tags) == 0 {
		return "(none)"
	}
	return strings.Join(tags, ", ")
}

// shortDigest keeps the algorithm and the first 12 characters of a digest.
func shortDigest(d string) string {
	algo, hex, ok := strings.Cut(d, ":")
	if !ok {
		return d[:min(len(d), 12)]
	}
	return algo + ":" + hex[:min(len(hex), 12)]
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unsupportedOn404 turns a 404 from a route an older server lacks into a
// message that says so. The caller has already resolved ids, so a 404 here is
// the route itself.
func unsupportedOn404(err error, what string) error {
	var apiErr *cli.ExitError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return usageError("the server does not support %s (it answered 404)", what)
	}
	return err
}

func versionsRunning(args []string, stdout, stderr io.Writer, e env) int {
	flags := newAPIFlags("versions running", versionsRunningHelp)
	var envName, service string
	flags.StringVar(&envName, "env", "", "only this environment")
	flags.StringVar(&service, "service", "", "only this service")
	positional, code, done := flags.parseArgs(args, stdout, stderr)
	if done {
		return code
	}
	if code, ok := needArgs(flags.commandFlags, positional, stderr, 0, "versions running takes no arguments"); !ok {
		return code
	}
	s, err := e.openSession(flags, stdout, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	var raw json.RawMessage
	if err := s.client.Do(s.ctx, http.MethodGet, "/api/v1/versions/running", nil, &raw); err != nil {
		return fail(stderr, unsupportedOn404(err, "running versions"))
	}
	var matrix runningMatrix
	if err := json.Unmarshal(raw, &matrix); err != nil {
		return fail(stderr, &cli.ExitError{Code: cli.ExitServer, Message: "the server's answer was not understood"})
	}
	var envNames, serviceNames []string
	for _, en := range matrix.Environments {
		envNames = append(envNames, en.Name)
	}
	for _, row := range matrix.Rows {
		serviceNames = append(serviceNames, row.Service)
	}
	if envName != "" && !containsString(envNames, envName) {
		return fail(stderr, usageError("unknown environment %q. Environments: %s", envName, listNames(envNames)))
	}
	if service != "" && !containsString(serviceNames, service) {
		return fail(stderr, usageError("unknown service %q. Services: %s", service, listNames(serviceNames)))
	}
	if s.json() {
		_ = cli.WriteJSON(stdout, raw)
		return exitOK
	}
	table := cli.NewTable(stdout)
	header := []string{"SERVICE"}
	var columns []int
	for i, en := range matrix.Environments {
		if envName == "" || en.Name == envName {
			header = append(header, en.Name)
			columns = append(columns, i)
		}
	}
	table.Row(header...)
	for _, row := range matrix.Rows {
		if service != "" && row.Service != service {
			continue
		}
		line := []string{row.Service}
		for _, i := range columns {
			cell := runningCell{State: "absent"}
			if i < len(row.Cells) {
				cell = row.Cells[i]
			}
			line = append(line, describeCell(cell))
		}
		table.Row(line...)
	}
	_ = table.Flush()
	return exitOK
}

func describeCell(c runningCell) string {
	switch c.State {
	case "absent":
		return "-"
	case "error":
		return "error: " + orDash(c.Error)
	}
	version := c.Version
	if version == "" {
		version = "?"
	}
	var notes []string
	if c.Behind {
		notes = append(notes, "behind")
	}
	if !c.ByYumdrake {
		notes = append(notes, "not by yumdrake")
	}
	if c.LastDeployFailed {
		notes = append(notes, "last deploy failed")
	}
	if len(notes) > 0 {
		version += " (" + strings.Join(notes, ", ") + ")"
	}
	return version
}
