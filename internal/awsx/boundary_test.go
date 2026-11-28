package awsx_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath  = "github.com/rcss-labs/yamdrake"
	deployPkg   = modulePath + "/internal/awsx/deploy"
	purposeFile = "internal/awsx/purpose.go" // the one file that may spell the deploy purpose
)

type listedPackage struct {
	importPath, dir string
	goFiles         []string
}

// moduleRoot is the directory of the module, where go list runs.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// trackSources lists every directory of the module's Go code. go list runs in
// a subprocess, which the test cache cannot see; reading the directories here
// makes the cache notice a file added or removed anywhere, so a cached "ok"
// never hides a new import or reference.
func trackSources(t *testing.T, root string) {
	t.Helper()
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_, err = os.ReadDir(path)
			}
			return err
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
}

// dependencies lists the packages of this module that the command at pkg is
// built from (go list -deps, without test files), with their Go files.
func dependencies(t *testing.T, root, pkg string) []listedPackage {
	t.Helper()
	trackSources(t, root)
	// pkg is always a fixed package path written in this file.
	cmd := exec.Command("go", "list", "-deps", "-f", `{{.ImportPath}}|{{.Dir}}|{{join .GoFiles ","}},{{join .CgoFiles ","}}`, pkg) //nolint:gosec
	cmd.Dir = root
	// The production build: Linux, no cgo (as the Dockerfile builds it).
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v: %s", pkg, err, stderr.String())
	}
	var packages []listedPackage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || (parts[0] != modulePath && !strings.HasPrefix(parts[0], modulePath+"/")) {
			continue
		}
		p := listedPackage{importPath: parts[0], dir: parts[1]}
		if parts[2] != "" {
			for _, name := range strings.Split(parts[2], ",") {
				if name != "" {
					p.goFiles = append(p.goFiles, name)
				}
			}
		}
		packages = append(packages, p)
	}
	return packages
}

func contains(packages []listedPackage, importPath string) bool {
	for _, p := range packages {
		if p.importPath == importPath {
			return true
		}
	}
	return false
}

// The orchestrator must not be able to obtain deployment credentials: the
// package that builds them is not part of its binary.
func TestOrchestratorCannotImportDeployCredentials(t *testing.T) {
	root := moduleRoot(t)
	orchestrator := dependencies(t, root, "./cmd/orchestrator")
	if contains(orchestrator, deployPkg) {
		t.Errorf("cmd/orchestrator depends on %s: the orchestrator binary must not be able to build deployment credentials", deployPkg)
	}
	// Controls: the check looks at the right things. The orchestrator does use
	// the discovery package, and the runner does use the deploy package.
	if !contains(orchestrator, modulePath+"/internal/awsx/discovery") {
		t.Error("cmd/orchestrator does not depend on internal/awsx/discovery: the dependency list is not what the test assumes")
	}
	if runner := dependencies(t, root, "./cmd/runner"); !contains(runner, deployPkg) {
		t.Error("cmd/runner does not depend on internal/awsx/deploy: deployment credentials are not wired into the runner")
	}
}

// Not importing the deploy package is not enough: the orchestrator could still
// read the deployment ciphertext and open it with the deployment purpose. No
// package it is built from may do either.
func TestOrchestratorDependenciesNeverOpenTheDeploySecret(t *testing.T) {
	root := moduleRoot(t)
	var scanned int
	for _, p := range dependencies(t, root, "./cmd/orchestrator") {
		for _, name := range p.goFiles {
			path := filepath.Join(p.dir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			scanned++
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatalf("relative path of %s: %v", path, err)
			}
			for _, problem := range deploySecretReferences(fset, file, filepath.ToSlash(relative) == purposeFile) {
				t.Errorf("%s (built into cmd/orchestrator): %s", relative, problem)
			}
		}
	}
	if scanned < 20 {
		t.Errorf("scanned only %d files: the dependency list is not what the test assumes", scanned)
	}
}

// deploySecretReferences reports what in file would let the orchestrator open
// the deployment secret: any use of the deploy purpose constant except as the
// purpose of an Encrypt or seal* call (a Decrypt, a helper that opens, a copy
// in a variable), the purpose string outside its definition, a reader of the
// deploy ciphertext column, a query that selects it, or a wildcard select or
// RETURNING on aws_accounts, which would read it too. Writing it (Encrypt,
// INSERT) is allowed: the administration API seals a deployment key it never
// reads back.
func deploySecretReferences(fset *token.FileSet, file *ast.File, isPurposeFile bool) []string {
	var problems []string
	report := func(pos token.Pos, what string) {
		problems = append(problems, fset.Position(pos).String()+": "+what)
	}
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			if value == "aws-deploy" && !isPurposeFile {
				report(n.Pos(), `the literal "aws-deploy" outside internal/awsx/purpose.go`)
			}
			for _, problem := range queryProblems(value) {
				report(n.Pos(), problem)
			}
		case *ast.Ident:
			switch {
			case n.Name == "DeploySecret" || n.Name == "readDeploySecret":
				report(n.Pos(), "a reference to the deploy-secret reader "+n.Name)
			case n.Name == "PurposeDeploy" && !isPurposeFile && !sealsWithPurpose(stack):
				report(n.Pos(), "PurposeDeploy used other than as the purpose of an Encrypt or seal call")
			}
		}
		return true
	})
	return problems
}

// sealsWithPurpose reports whether the innermost call around the end of stack
// is an Encrypt or a seal* helper.
func sealsWithPurpose(stack []ast.Node) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		call, ok := stack[i].(*ast.CallExpr)
		if !ok {
			continue
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		case *ast.Ident:
			name = fun.Name
		}
		return name == "Encrypt" || strings.HasPrefix(strings.ToLower(name), "seal")
	}
	return false
}

var (
	wildcardSelect    = regexp.MustCompile(`(?is)\bselect\s+\*`)
	wildcardReturning = regexp.MustCompile(`(?is)\breturning\s+\*`)
)

// queryProblems looks at one SQL string literal.
func queryProblems(sql string) []string {
	lower := strings.ToLower(sql)
	var problems []string
	if strings.Contains(lower, "aws_accounts") && (wildcardSelect.MatchString(sql) || wildcardReturning.MatchString(sql)) {
		problems = append(problems, "a wildcard SELECT or RETURNING on aws_accounts would read deploy_secret_ciphertext")
	}
	if !strings.Contains(lower, "deploy_secret_ciphertext") {
		return problems
	}
	if !strings.Contains(lower, "insert into") {
		return append(problems, "a query that is not an INSERT mentions deploy_secret_ciphertext")
	}
	if i := strings.Index(lower, "returning"); i >= 0 && strings.Contains(lower[i:], "deploy_secret_ciphertext") {
		problems = append(problems, "an INSERT returns deploy_secret_ciphertext")
	}
	return problems
}

// The scanner must see what it is meant to see, or the check above is vacuous.
func TestDeploySecretScannerFlagsReadsAndPassesWrites(t *testing.T) {
	cases := []struct {
		name, source string
		want         int
	}{
		{"decrypt with the constant", `package p; func f() { c.Decrypt(ctx, awsx.PurposeDeploy, b) }`, 1},
		{"decrypt with the string", `package p; func f() { c.Decrypt(ctx, "aws-deploy", b) }`, 1},
		{"decrypt with a local alias is not seen, the string is", `package p; const x = "aws-deploy"`, 1},
		{"store reader", `package p; func f() { store.DeploySecret(ctx, db, id) }`, 1},
		{"select of the column", `package p; const q = "SELECT deploy_secret_ciphertext FROM aws_accounts"`, 1},
		{"column passed to a helper", `package p; var c = "deploy_secret_ciphertext"`, 1},
		{"encrypt with the constant", `package p; func f() { c.Encrypt(ctx, awsx.PurposeDeploy, b) }`, 0},
		{"insert of the column", `package p; const q = "INSERT INTO aws_accounts (deploy_secret_ciphertext) VALUES ($1)"`, 0},
		{"helper that opens with the constant", `package p; func f() { openAWSSecret(ctx, awsx.PurposeDeploy, b) }`, 1},
		{"method helper that opens with the constant", `package p; func f() { s.open(ctx, awsx.PurposeDeploy, b) }`, 1},
		{"constant stored in a variable", `package p; var purpose = awsx.PurposeDeploy`, 1},
		{"seal helper with the constant", `package p; func f() { s.sealAWSSecret(ctx, awsx.PurposeDeploy, b) }`, 0},
		{"select star", `package p; const q = "SELECT * FROM aws_accounts WHERE id = $1"`, 1},
		{"select star multi-line", "package p; const q = `SELECT\n  *\n FROM aws_accounts`", 1},
		{"returning star", `package p; const q = "INSERT INTO aws_accounts (name) VALUES ($1) RETURNING *"`, 1},
		{"insert returning the column", `package p; const q = "INSERT INTO aws_accounts (deploy_secret_ciphertext) VALUES ($1) RETURNING deploy_secret_ciphertext"`, 1},
		{"insert returning other columns", `package p; const q = "INSERT INTO aws_accounts (deploy_secret_ciphertext) VALUES ($1) RETURNING id, name"`, 0},
		{"select star of another table", `package p; const q = "SELECT * FROM users"`, 0},
		{"decrypt with the discovery purpose", `package p; func f() { c.Decrypt(ctx, awsx.PurposeDiscovery, b) }`, 0},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "p.go", c.source, 0)
		if err != nil {
			t.Fatalf("%s: parse: %v", c.name, err)
		}
		if got := deploySecretReferences(fset, file, false); len(got) != c.want {
			t.Errorf("%s: %d problems %v, want %d", c.name, len(got), got, c.want)
		}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "purpose.go", `package awsx; const PurposeDeploy = "aws-deploy"`, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := deploySecretReferences(fset, file, true); len(got) != 0 {
		t.Errorf("the purpose definition is flagged: %v", got)
	}
}
