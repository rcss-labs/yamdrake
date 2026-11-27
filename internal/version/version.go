// Package version holds build metadata that is injected at link time with -ldflags -X.
package version

// Version and Commit are overridden by the Makefile build via -ldflags "-X ...".
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns the version and commit in the form "<version> (<commit>)".
func String() string {
	return Version + " (" + Commit + ")"
}
