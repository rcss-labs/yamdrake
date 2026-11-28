package versions

import (
	"strconv"
	"strings"
)

// semver is a parsed tag: major.minor.patch with an optional pre-release.
type semver struct {
	core       [3]uint64
	prerelease []string
}

// parseSemver accepts an optional leading "v", three numeric parts, an
// optional "-pre.release" and ignored "+build" metadata.
func parseSemver(tag string) (semver, bool) {
	rest := strings.TrimPrefix(tag, "v")
	rest, _, _ = strings.Cut(rest, "+")
	rest, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out semver
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semver{}, false
		}
		out.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		out.prerelease = strings.Split(pre, ".")
	}
	return out, true
}

// CompareSemver orders two tags as semantic versions and returns -1, 0 or 1.
// A leading "v" is ignored and a pre-release sorts before its release.
// A tag that is not a version sorts before every version and compares equal
// to every other non-version, so a stable sort keeps their order.
func CompareSemver(a, b string) int {
	left, leftOK := parseSemver(a)
	right, rightOK := parseSemver(b)
	switch {
	case !leftOK && !rightOK:
		return 0
	case !leftOK:
		return -1
	case !rightOK:
		return 1
	}
	for i := range left.core {
		if c := compareUint(left.core[i], right.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(left.prerelease) == 0 && len(right.prerelease) == 0:
		return 0
	case len(left.prerelease) == 0:
		return 1
	case len(right.prerelease) == 0:
		return -1
	}
	return comparePrerelease(left.prerelease, right.prerelease)
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePrerelease follows semver 2.0: numeric identifiers compare as
// numbers and sort before alphanumeric ones; a longer list wins a shared prefix.
func comparePrerelease(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aErr := strconv.ParseUint(a[i], 10, 64)
		bn, bErr := strconv.ParseUint(b[i], 10, 64)
		var c int
		switch {
		case aErr == nil && bErr == nil:
			c = compareUint(an, bn)
		case aErr == nil:
			c = -1
		case bErr == nil:
			c = 1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	return compareUint(uint64(len(a)), uint64(len(b)))
}
