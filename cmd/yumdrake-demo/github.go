package main

import (
	"fmt"

	"github.com/rcss-labs/yamdrake/test/awstest"
	"github.com/rcss-labs/yamdrake/test/e2e/fakegithub"
)

const (
	// demoGitHubToken is the bearer token of the fake GitHub. It is PUBLIC and opens nothing real.
	demoGitHubToken = "yumdrake-demo-fake-github-token" //nolint:gosec // token of the fake GitHub only
	demoOwner       = "demo-org"
	// demoRepo holds the inventory and the jobs; demoConfigRepo the env files of every environment.
	demoRepo       = demoOwner + "/platform"
	demoConfigRepo = demoOwner + "/config"
	demoBranch     = "main"
	// demoMainCommit is the head of main; the inventory is active when it is the active commit.
	demoMainCommit = "1111111111111111111111111111111111111111"
)

// The env files of the config repository. The running version (the one the seeded services run and the
// destination buckets hold) has the old files; the newer versions have new ones, so a deployment shows a
// changed file and its keys. The values are made up.
var (
	configNewWebEnv = []byte("API_KEY=demo-new-key\nLOG_LEVEL=info\nNEW_FLAG=1\n")
	configNewAPIEnv = []byte("PORT=8080\nDB_PASSWORD=demo-db-password\n")
	configDevWebEnv = []byte("API_KEY=demo-dev-key\nLOG_LEVEL=debug\n")
)

// newDemoGitHub builds the fake GitHub (not listening: serve its Handler): the sample data in dataDir as the
// platform repository's main branch, and the config repository with a branch for every version and one for dev.
func newDemoGitHub(dataDir string) (*fakegithub.Server, error) {
	gh := fakegithub.NewUnstarted(demoGitHubToken)
	if err := gh.LoadDir(demoRepo, demoMainCommit, dataDir); err != nil {
		return nil, fmt.Errorf("load the sample data: %w", err)
	}
	gh.SetHead(demoRepo, demoBranch, demoMainCommit)

	files := func(web, api []byte) map[string][]byte {
		out := map[string][]byte{}
		for _, env := range demoEnvironments {
			out["envs/"+env.Name+"/web.env"] = web
			out["envs/"+env.Name+"/api.env"] = api
		}
		return out
	}
	gh.AddCommit(demoConfigRepo, "c0nf1g-dev", files(configDevWebEnv, configNewAPIEnv))
	gh.SetHead(demoConfigRepo, "dev", "c0nf1g-dev")
	gh.AddCommit(demoConfigRepo, "c0nf1g-v1", files(awstest.DestinationWebEnv, awstest.DestinationAPIEnv))
	gh.SetHead(demoConfigRepo, "v"+awstest.RunningVersion, "c0nf1g-v1")
	for i, version := range []string{awstest.NewVersion, awstest.CrashVersion} {
		sha := fmt.Sprintf("c0nf1g-v%d", i+2)
		gh.AddCommit(demoConfigRepo, sha, files(configNewWebEnv, configNewAPIEnv))
		gh.SetHead(demoConfigRepo, "v"+version, sha)
	}
	return gh, nil
}
