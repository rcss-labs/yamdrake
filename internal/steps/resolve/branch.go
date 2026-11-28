package resolve

import "strings"

// BranchFor returns the env-file branch of a version: the version with a
// leading "v", added when it is not already there.
func BranchFor(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

// fallbackBranch is the branch a dev environment reads when the version has
// no branch of its own.
const fallbackBranch = "dev"

// ChooseBranch applies the env-file branch rule. versionBranchExists says
// whether BranchFor(version) exists in the config repository; label is the
// environment's label.
//
//   - The version branch exists: ok, fallback false and branch "" (the caller
//     keeps BranchFor(version); this function does not know the version).
//   - It is missing in a "dev" environment: branch "dev", fallback true, ok.
//   - It is missing anywhere else ("preprod", "prod", an unknown label): ok is
//     false. Nothing falls back to dev outside dev.
func ChooseBranch(versionBranchExists bool, label string) (branch string, fallback bool, ok bool) {
	switch {
	case versionBranchExists:
		return "", false, true
	case label == "dev":
		return fallbackBranch, true, true
	default:
		return "", false, false
	}
}
