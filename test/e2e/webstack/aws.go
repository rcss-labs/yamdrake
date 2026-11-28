package main

// The stack for the browser test that runs real steps against Floci
// (web/e2e/aws-runs.spec.ts): two AWS accounts (dev-1, prod-1) seeded with
// awstest.SeedStack, a fake GitHub holding an inventory that points at them, a
// job that needs approval in prod, and the production runner binary with no fake
// step. The fake-step stack (runs.spec.ts) is untouched by any of this.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rcss-labs/yumdrake/test/awstest"
	"github.com/rcss-labs/yumdrake/test/e2e/fakegithub"
)

const (
	awsJobPath     = "jobs/deploy.yaml"
	awsJobTitle    = "Deploy everything"
	awsCommit      = "5555555555555555555555555555555555555555"
	awsConfigRepo  = "acme/config"
	awsKeyPassword = "test" // the secret access key of a Floci account; Floci checks only the access key id
)

// Config files in the config repository. The values are never shown by the
// application; the browser test looks for the key names only.
var (
	configWebEnv    = []byte("API_KEY=aws-e2e-new-key\nLOG_LEVEL=info\nNEW_FLAG=1\n")
	configAPIEnv    = []byte("PORT=8080\nDB_PASSWORD=aws-e2e-db-password\n")
	configDevBranch = []byte("API_KEY=aws-e2e-dev-key\nLOG_LEVEL=debug\n")
)

// awsWorld is the Floci world of one stack.
type awsWorld struct {
	endpoint string
	opts     awstest.StackOptions
	stack    *awstest.Stack
}

// seedAWS seeds a world of random accounts and names into the Floci at endpoint.
// The ECS tasks it starts are removed by make floci-clean; local images it built are removed when the stack stops.
func seedAWS(endpoint string, cleanup *cleaner) (*awsWorld, error) {
	program := &awstest.Program{}
	cleanup.add(program.Close)
	world := &awsWorld{endpoint: endpoint, opts: awstest.StackOptions{
		Suffix:          awstest.RandomSuffix(),
		DevAccount:      awstest.RandomAccountID(),
		ProdAccount:     awstest.RandomAccountID(),
		PlatformAccount: awstest.RandomAccountID(),
	}}
	err := program.Run(func() {
		world.stack = awstest.SeedStack(awstest.NewWorld(endpoint, program), world.opts)
	})
	if err != nil {
		return nil, fmt.Errorf("seed Floci: %w", err)
	}
	return world, nil
}

func (w *awsWorld) environment(name string) *awstest.Environment { return w.stack.Environments[name] }

// loadAWSFixture puts the inventory and job on the platform repository and the
// env files on the config repository of the fake GitHub.
func loadAWSFixture(gh *fakegithub.Server, w *awsWorld) error {
	dir, err := os.MkdirTemp("", "webstack-aws-fixture-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	write := func(name, content string) error {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o600)
	}

	var environments strings.Builder
	environments.WriteString("apiVersion: yumdrake/v1\nkind: Environments\nenvironments:\n")
	for _, name := range []string{awstest.DevEnvironment, awstest.ProdEnvironment} {
		e := w.environment(name)
		fmt.Fprintf(&environments, "  - name: %s\n    label: %s\n    aws_account: %q\n", e.Name, e.Label, e.Account)
	}
	if err := write("environments.yaml", environments.String()); err != nil {
		return err
	}
	for _, service := range awstest.Services {
		var b strings.Builder
		fmt.Fprintf(&b, "apiVersion: yumdrake/v1\nkind: Service\nname: %s\nversions:\n  pattern: '^v?\\d+\\.\\d+\\.\\d+$'\n  sort: semver\n  limit: 100\nenvironments:\n", service)
		for _, name := range []string{awstest.DevEnvironment, awstest.ProdEnvironment} {
			e := w.environment(name)
			s := e.Services[service]
			fmt.Fprintf(&b, "  %s:\n    ecs_service: %s\n    ecr_repository: %s\n", name, s.ServiceARN, s.RepositoryURI)
			if service == "web" || service == "api" {
				fmt.Fprintf(&b, "    env_files:\n      - name: %s.env\n        source_env_path: %s/envs/%s/%s.env\n        destination_env_path: s3://%s/%s.env\n",
					service, awsConfigRepo, name, service, e.DestinationBucket, service)
			}
			if service == "web" {
				fmt.Fprintf(&b, "    tomls:\n      - name: web.toml\n        source_toml_path: s3://%s/web.toml\n        destination_toml_path: s3://%s/web.toml\n", e.SourceBucket, e.DestinationBucket)
			}
		}
		if err := write("services/"+service+".yaml", b.String()); err != nil {
			return err
		}
	}
	job := `apiVersion: yumdrake/v1
kind: Job
job: deploy
title: "` + awsJobTitle + `"
environment:
  labels: [dev, prod]
approval:
  when:
    labels: [prod]
groups:
  all:
    label: Everything
    services: [web, api, worker]
steps:
  - name: config
    use: copy-config
    group: all
  - name: roll
    use: ecs-deploy
    group: all
    needs:
      - step: config
        scope: service
`
	if err := write(awsJobPath, job); err != nil {
		return err
	}
	if err := gh.LoadDir(repo, awsCommit, dir); err != nil {
		return fmt.Errorf("load the platform repository: %w", err)
	}
	gh.SetHead(repo, branch, awsCommit)

	files := func(web, api []byte) map[string][]byte {
		out := map[string][]byte{}
		for _, name := range []string{awstest.DevEnvironment, awstest.ProdEnvironment} {
			out["envs/"+name+"/web.env"] = web
			out["envs/"+name+"/api.env"] = api
		}
		return out
	}
	gh.AddCommit(awsConfigRepo, "c0nf1g-dev", files(configDevBranch, configAPIEnv))
	gh.SetHead(awsConfigRepo, "dev", "c0nf1g-dev")
	// One branch per version, as the demo has. The running version holds what
	// the destination buckets hold now; the newer ones hold new files.
	gh.AddCommit(awsConfigRepo, "c0nf1g-v1", files(awstest.DestinationWebEnv, awstest.DestinationAPIEnv))
	gh.SetHead(awsConfigRepo, "v"+awstest.RunningVersion, "c0nf1g-v1")
	for i, version := range []string{awstest.NewVersion, awstest.CrashVersion} {
		sha := fmt.Sprintf("c0nf1g-v%d", i+2)
		gh.AddCommit(awsConfigRepo, sha, files(configWebEnv, configAPIEnv))
		gh.SetHead(awsConfigRepo, "v"+version, sha)
	}
	return nil
}

// awsKey is the connection of an account: its id is its access key id.
func awsKey(accountID string) map[string]any {
	return map[string]any{"method": "key", "access_key_id": accountID, "secret_access_key": awsKeyPassword}
}
