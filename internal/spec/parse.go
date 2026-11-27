package spec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	apiVersion = "yumdrake/v1"

	// maxInputBytes bounds what the parser will look at; repositories are untrusted.
	maxInputBytes = 1 << 20
	// maxNestingDepth bounds YAML nesting. Real files nest fewer than ten levels.
	maxNestingDepth = 32
)

var (
	namePattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	awsAccountPattern   = regexp.MustCompile(`^\d{12}$`)
	ecsServicePattern   = regexp.MustCompile(`^arn:aws:ecs:[a-z0-9-]+:\d{12}:service/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)
	ecrRepositoryRegexp = regexp.MustCompile(`^\d{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/[a-z0-9._/-]+$`)
	yamlLinePattern     = regexp.MustCompile(`line (\d+)`)
)

var allowedLabels = []string{"dev", "preprod", "prod"}

const defaultVersionPattern = `^v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`

// ParseEnvironments parses the single environments file.
func ParseEnvironments(file string, data []byte) (environments []Environment, problems Problems) {
	p := &parser{file: file}
	defer p.recoverPanic(&problems)
	root := p.load(data, "Environments")
	if root == nil {
		return nil, p.problems
	}
	fields := p.fields(root, "file", "apiVersion", "kind", "environments")
	items := p.sequence(p.require(fields, root, "environments"), "environments")
	for _, item := range items {
		environments = append(environments, p.environment(item))
	}
	return environments, p.problems.Sorted()
}

// ParseService parses one service file.
func ParseService(file string, data []byte) (service Service, problems Problems) {
	p := &parser{file: file}
	defer p.recoverPanic(&problems)
	root := p.load(data, "Service")
	if root == nil {
		return Service{}, p.problems
	}
	fields := p.fields(root, "service", "apiVersion", "kind", "name", "versions", "environments")
	service = Service{File: file, Environments: map[string]ServiceEnv{}}
	service.Name = p.name(p.require(fields, root, "name"), "service name")
	service.Versions = p.versions(fields["versions"])
	envs := p.fields(p.require(fields, root, "environments"), "environments")
	for _, key := range p.orderedKeys(fields["environments"]) {
		p.name(key, "environment name")
		service.Environments[key.Value] = p.serviceEnv(envs[key.Value])
	}
	if node := fields["environments"]; node != nil && len(envs) == 0 {
		p.add(node.Line, "service %q must have at least one environment", service.Name)
	}
	return service, p.problems.Sorted()
}

// ParseJob parses one job file.
func ParseJob(file string, data []byte) (job Job, problems Problems) {
	p := &parser{file: file}
	defer p.recoverPanic(&problems)
	root := p.load(data, "Job")
	if root == nil {
		return Job{}, p.problems
	}
	fields := p.fields(root, "job", "apiVersion", "kind", "job", "title", "environment", "groups", "approval", "notify", "steps")
	job = Job{File: file}
	job.ID = p.name(p.require(fields, root, "job"), "job id")
	job.Title = p.text(p.require(fields, root, "title"), "title")

	environment := p.fields(p.require(fields, root, "environment"), "environment", "labels")
	job.Labels = p.labels(p.require(environment, fields["environment"], "labels"), "environment.labels")

	job.Groups = p.groups(p.require(fields, root, "groups"))

	if node := fields["approval"]; node != nil {
		job.Approval = p.approval(node)
	}
	if node := fields["notify"]; node != nil {
		notify := p.fields(node, "notify", "slack")
		channel := p.text(p.require(notify, node, "slack"), "notify.slack")
		if channel != "" && !strings.HasPrefix(channel, "#") {
			p.add(notify["slack"].Line, "notify.slack %q must start with #", channel)
		}
		job.SlackChannel = channel
	}

	for _, item := range p.sequence(p.require(fields, root, "steps"), "steps") {
		job.Steps = append(job.Steps, p.step(item))
	}
	return job, p.problems.Sorted()
}

// parser accumulates problems for one file.
type parser struct {
	file     string
	problems Problems
}

func (p *parser) add(line int, format string, args ...any) {
	p.problems = append(p.problems, Problem{File: p.file, Line: line, Message: fmt.Sprintf(format, args...)})
}

// recoverPanic turns an unexpected panic into a problem so hostile input can
// never crash the caller.
func (p *parser) recoverPanic(out *Problems) {
	if r := recover(); r != nil {
		p.add(0, "internal parser error: %v", r)
		*out = p.problems.Sorted()
	}
}

// load decodes the document into a yaml.Node tree, rejecting everything that is
// not one mapping with the expected apiVersion and kind. It returns nil when the
// file cannot be examined further.
func (p *parser) load(data []byte, wantKind string) *yaml.Node {
	if len(data) > maxInputBytes {
		p.add(0, "file is %d bytes, larger than the %d byte limit", len(data), maxInputBytes)
		return nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			p.add(1, "file is empty")
		} else {
			p.yamlError(err)
		}
		return nil
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case err == nil:
		line := extra.Line
		if len(extra.Content) > 0 {
			line = extra.Content[0].Line
		}
		p.add(max(line, 1), "more than one YAML document in the file; use exactly one")
		return nil
	case !errors.Is(err, io.EOF):
		p.yamlError(err)
		return nil
	}

	if !p.checkTree(&document, 0) {
		return nil
	}
	root := &document
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		p.add(max(root.Line, 1), "the top level must be a mapping, found %s", describe(root))
		return nil
	}

	return p.checkHeader(root, wantKind)
}

// checkHeader verifies apiVersion and kind. A wrong kind stops parsing because
// every other field would be reported against the wrong schema.
func (p *parser) checkHeader(root *yaml.Node, wantKind string) *yaml.Node {
	values := map[string]*yaml.Node{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		values[root.Content[i].Value] = root.Content[i+1]
	}
	if node := values["apiVersion"]; node == nil {
		p.add(root.Line, "missing required field %q (expected %q)", "apiVersion", apiVersion)
	} else if node.Kind != yaml.ScalarNode || node.Value != apiVersion {
		p.add(node.Line, "unsupported apiVersion %q, expected %q", node.Value, apiVersion)
	}
	node := values["kind"]
	switch {
	case node == nil:
		p.add(root.Line, "missing required field %q (expected %q)", "kind", wantKind)
		return nil
	case node.Kind != yaml.ScalarNode || node.Value != wantKind:
		p.add(node.Line, "wrong kind %q, expected %q", node.Value, wantKind)
		return nil
	}
	return root
}

// checkTree rejects aliases and over-deep nesting. Aliases are refused outright:
// expanding them is how small documents become enormous ones.
func (p *parser) checkTree(n *yaml.Node, depth int) bool {
	if depth > maxNestingDepth {
		p.add(n.Line, "YAML is nested deeper than %d levels", maxNestingDepth)
		return false
	}
	if n.Kind == yaml.AliasNode {
		p.add(n.Line, "YAML alias *%s is not allowed", n.Value)
		return false
	}
	for _, child := range n.Content {
		if !p.checkTree(child, depth+1) {
			return false
		}
	}
	return true
}

func (p *parser) yamlError(err error) {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	line := 1
	if match := yamlLinePattern.FindStringSubmatch(message); match != nil {
		if parsed, convErr := strconv.Atoi(match[1]); convErr == nil && parsed > 0 {
			line = parsed
		}
	}
	p.add(line, "invalid YAML: %s", message)
}

func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		if n.ShortTag() == "!!null" {
			return "nothing"
		}
		return fmt.Sprintf("the value %q", n.Value)
	}
	return "an unsupported value"
}

// fields returns a mapping's values by key, reporting non-mappings, non-scalar
// keys, duplicate keys and keys outside allowed. The result is nil when n is
// nil or not a mapping.
func (p *parser) fields(n *yaml.Node, context string, allowed ...string) map[string]*yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		p.add(n.Line, "%s must be a mapping, found %s", context, describe(n))
		return nil
	}
	result := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			p.add(key.Line, "%s has a key that is not a plain word", context)
			continue
		}
		if _, seen := result[key.Value]; seen {
			p.add(key.Line, "duplicate field %q in %s", key.Value, context)
			continue
		}
		if len(allowed) > 0 && !contains(allowed, key.Value) {
			p.add(key.Line, "unknown field %q in %s (allowed: %s)", key.Value, context, strings.Join(allowed, ", "))
			continue
		}
		result[key.Value] = value
	}
	return result
}

// orderedKeys lists a mapping's key nodes in file order, skipping repeats.
func (p *parser) orderedKeys(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var keys []*yaml.Node
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind == yaml.ScalarNode && !seen[key.Value] {
			seen[key.Value] = true
			keys = append(keys, key)
		}
	}
	return keys
}

// require returns fields[key], reporting it at parent's line when absent.
func (p *parser) require(fields map[string]*yaml.Node, parent *yaml.Node, key string) *yaml.Node {
	if fields == nil {
		return nil
	}
	node, ok := fields[key]
	if !ok {
		line := 0
		if parent != nil {
			line = parent.Line
		}
		p.add(line, "missing required field %q", key)
		return nil
	}
	return node
}

func (p *parser) sequence(n *yaml.Node, context string) []*yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		p.add(n.Line, "%s must be a list, found %s", context, describe(n))
		return nil
	}
	if len(n.Content) == 0 {
		p.add(n.Line, "%s must not be empty", context)
	}
	return n.Content
}

// text reads a scalar as a string; nil or null yields "" (already reported
// as missing when required).
func (p *parser) text(n *yaml.Node, context string) string {
	if n == nil {
		return ""
	}
	if n.Kind != yaml.ScalarNode || n.ShortTag() == "!!null" {
		p.add(n.Line, "%s must be a string, found %s", context, describe(n))
		return ""
	}
	if n.Value == "" {
		p.add(n.Line, "%s must not be empty", context)
	}
	return n.Value
}

func (p *parser) name(n *yaml.Node, context string) string {
	value := p.text(n, context)
	if value != "" && !namePattern.MatchString(value) {
		p.add(n.Line, "%s %q is invalid: use lower-case letters, digits and hyphens, starting with a letter or digit, at most 63 characters", context, value)
	}
	return value
}

func (p *parser) oneOf(n *yaml.Node, context string, allowed []string) string {
	value := p.text(n, context)
	if value != "" && !contains(allowed, value) {
		p.add(n.Line, "%s %q is invalid: must be one of %s", context, value, strings.Join(allowed, ", "))
	}
	return value
}

func (p *parser) labels(n *yaml.Node, context string) []string {
	var labels []string
	for _, item := range p.sequence(n, context) {
		labels = append(labels, p.oneOf(item, "label", allowedLabels))
	}
	return labels
}

func (p *parser) integer(n *yaml.Node, context string, minimum, maximum int) int {
	if n == nil {
		return 0
	}
	var value int
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&value) != nil {
		p.add(n.Line, "%s must be a whole number, found %s", context, describe(n))
		return 0
	}
	if value < minimum || value > maximum {
		p.add(n.Line, "%s is %d, must be between %d and %d", context, value, minimum, maximum)
	}
	return value
}

func (p *parser) duration(n *yaml.Node, context string) time.Duration {
	if n == nil {
		return 0
	}
	value := p.text(n, context)
	if value == "" {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		p.add(n.Line, "%s %q is not a duration such as 15m or 24h", context, value)
		return 0
	}
	if parsed <= 0 {
		p.add(n.Line, "%s %q must be greater than zero", context, value)
	}
	return parsed
}

func (p *parser) matching(n *yaml.Node, context string, pattern *regexp.Regexp, expected string) string {
	value := p.text(n, context)
	if value != "" && !pattern.MatchString(value) {
		p.add(n.Line, "%s %q is invalid: expected %s", context, value, expected)
	}
	return value
}

// s3Location checks s3://<bucket>/<key> with a non-empty bucket and key.
func (p *parser) s3Location(n *yaml.Node, context string) string {
	value := p.text(n, context)
	if value == "" {
		return ""
	}
	rest, ok := strings.CutPrefix(value, "s3://")
	bucket, key, hasKey := strings.Cut(rest, "/")
	if !ok || !hasKey || bucket == "" || key == "" {
		p.add(n.Line, "%s %q is invalid: expected s3://<bucket>/<key>", context, value)
	}
	return value
}

func (p *parser) environment(n *yaml.Node) Environment {
	environment := Environment{Line: n.Line}
	fields := p.fields(n, "environment", "name", "label", "aws_account")
	environment.Name = p.name(p.require(fields, n, "name"), "environment name")
	environment.Label = p.oneOf(p.require(fields, n, "label"), "label", allowedLabels)
	if account := p.require(fields, n, "aws_account"); account != nil {
		if account.Kind == yaml.ScalarNode && account.ShortTag() != "!!str" {
			p.add(account.Line, "aws_account %s must be written as a quoted string such as \"111111111111\"", account.Value)
		} else {
			environment.AWSAccount = p.matching(account, "aws_account", awsAccountPattern, "exactly 12 digits")
		}
	}
	return environment
}

func (p *parser) versions(n *yaml.Node) VersionRule {
	rule := VersionRule{Pattern: defaultVersionPattern, Sort: "semver", Limit: 100}
	if n == nil {
		return rule
	}
	fields := p.fields(n, "versions", "pattern", "sort", "limit")
	if node := fields["pattern"]; node != nil {
		if pattern := p.text(node, "versions.pattern"); pattern != "" {
			if _, err := regexp.Compile(pattern); err != nil {
				p.add(node.Line, "versions.pattern %q is not a valid regular expression: %v", pattern, err)
			}
			rule.Pattern = pattern
		}
	}
	if node := fields["sort"]; node != nil {
		if sortOrder := p.oneOf(node, "versions.sort", []string{"semver", "pushed"}); sortOrder != "" {
			rule.Sort = sortOrder
		}
	}
	if node := fields["limit"]; node != nil {
		rule.Limit = p.integer(node, "versions.limit", 1, 1000)
	}
	return rule
}

func (p *parser) serviceEnv(n *yaml.Node) ServiceEnv {
	env := ServiceEnv{Line: n.Line}
	fields := p.fields(n, "service environment", "ecs_service", "ecr_repository", "env_files", "tomls")
	env.ECSService = p.matching(p.require(fields, n, "ecs_service"), "ecs_service", ecsServicePattern,
		"a full ARN such as arn:aws:ecs:<region>:<account>:service/<cluster>/<service>")
	env.ECRRepository = p.matching(p.require(fields, n, "ecr_repository"), "ecr_repository", ecrRepositoryRegexp,
		"a full URI such as <account>.dkr.ecr.<region>.amazonaws.com/<repository>")
	for _, item := range p.optionalSequence(fields["env_files"], "env_files") {
		itemFields := p.fields(item, "env file", "name", "source_env_path", "destination_env_path")
		file := EnvFile{Line: item.Line}
		file.Name = p.text(p.require(itemFields, item, "name"), "env file name")
		file.Source = p.sourceEnvPath(p.require(itemFields, item, "source_env_path"))
		file.Destination = p.s3Location(p.require(itemFields, item, "destination_env_path"), "destination_env_path")
		env.EnvFiles = append(env.EnvFiles, file)
	}
	for _, item := range p.optionalSequence(fields["tomls"], "tomls") {
		itemFields := p.fields(item, "toml", "name", "source_toml_path", "destination_toml_path")
		toml := Toml{Line: item.Line}
		toml.Name = p.text(p.require(itemFields, item, "name"), "toml name")
		toml.Source = p.s3Location(p.require(itemFields, item, "source_toml_path"), "source_toml_path")
		toml.Destination = p.s3Location(p.require(itemFields, item, "destination_toml_path"), "destination_toml_path")
		env.Tomls = append(env.Tomls, toml)
	}
	return env
}

// optionalSequence is sequence for optional fields, where an absent field is fine.
func (p *parser) optionalSequence(n *yaml.Node, context string) []*yaml.Node {
	if n == nil {
		return nil
	}
	return p.sequence(n, context)
}

// sourceEnvPath checks <owner>/<repo>/<path>: at least three non-empty parts.
func (p *parser) sourceEnvPath(n *yaml.Node) string {
	value := p.text(n, "source_env_path")
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "/")
	valid := len(parts) >= 3
	for _, part := range parts {
		valid = valid && part != ""
	}
	if !valid {
		p.add(n.Line, "source_env_path %q is invalid: expected <owner>/<repo>/<path>", value)
	}
	return value
}

// groups reads the groups mapping, keeping file order.
func (p *parser) groups(n *yaml.Node) []Group {
	fields := p.fields(n, "groups")
	if n != nil && len(fields) == 0 && n.Kind == yaml.MappingNode {
		p.add(n.Line, "groups must not be empty")
	}
	var groups []Group
	for _, key := range p.orderedKeys(n) {
		node := fields[key.Value]
		if node == nil {
			continue
		}
		group := Group{ID: p.name(key, "group id"), Line: key.Line}
		groupFields := p.fields(node, "group "+key.Value, "label", "services")
		group.Label = p.text(p.require(groupFields, node, "label"), "group label")
		for _, service := range p.sequence(p.require(groupFields, node, "services"), "group services") {
			group.Services = append(group.Services, p.name(service, "service name"))
		}
		groups = append(groups, group)
	}
	return groups
}

func (p *parser) approval(n *yaml.Node) *Approval {
	fields := p.fields(n, "approval", "when", "expires")
	approval := &Approval{Expires: 24 * time.Hour}
	when := p.fields(p.require(fields, n, "when"), "approval.when", "labels")
	approval.Labels = p.labels(p.require(when, fields["when"], "labels"), "approval.when.labels")
	if node := fields["expires"]; node != nil {
		if expires := p.duration(node, "approval.expires"); expires > 0 {
			approval.Expires = expires
		}
	}
	return approval
}

func (p *parser) step(n *yaml.Node) Step {
	step := Step{Line: n.Line}
	fields := p.fields(n, "step", "name", "use", "group", "max_parallel", "timeout", "needs")
	step.Name = p.name(p.require(fields, n, "name"), "step name")
	step.Use = p.oneOf(p.require(fields, n, "use"), "step use", []string{"copy-config", "ecs-deploy"})
	step.Group = p.name(p.require(fields, n, "group"), "step group")
	step.MaxParallel = p.integer(fields["max_parallel"], "max_parallel", 1, int(^uint(0)>>1))
	if node := fields["timeout"]; node != nil {
		step.Timeout = p.duration(node, "timeout")
		if step.Use != "ecs-deploy" {
			p.add(node.Line, "timeout is only allowed on ecs-deploy steps, but step %q uses %q", step.Name, step.Use)
		}
	}
	for _, item := range p.optionalSequence(fields["needs"], "needs") {
		step.Needs = append(step.Needs, p.need(item))
	}
	return step
}

// need reads a needs item: a bare step name or a {step, scope} mapping.
func (p *parser) need(n *yaml.Node) Need {
	if n.Kind == yaml.ScalarNode {
		return Need{Step: p.name(n, "needs step"), Scope: "step"}
	}
	fields := p.fields(n, "needs item", "step", "scope")
	need := Need{Scope: "step"}
	need.Step = p.name(p.require(fields, n, "step"), "needs step")
	if node := fields["scope"]; node != nil {
		if scope := p.oneOf(node, "needs scope", []string{"step", "service"}); scope != "" {
			need.Scope = scope
		}
	}
	return need
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
