package manifest

import (
	"bytes"
	"sort"
	"strings"
)

// EnvKeyDiff compares two dotenv files and returns the names whose value was
// added, removed or changed. It never returns, logs or errors with a value.
//
// Lines are KEY=value with an optional "export " prefix; blank lines and
// "#" comments are ignored, quotes are not interpreted, the first "=" splits
// key from value, and for a duplicated key the last line wins. A line that
// cannot be read as a key is ignored.
func EnvKeyDiff(previous, next []byte) KeyDiff {
	before, after := parseEnv(previous), parseEnv(next)
	diff := KeyDiff{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for key, value := range after {
		old, existed := before[key]
		switch {
		case !existed:
			diff.Added = append(diff.Added, key)
		case old != value:
			diff.Changed = append(diff.Changed, key)
		}
	}
	for key := range before {
		if _, stillThere := after[key]; !stillThere {
			diff.Removed = append(diff.Removed, key)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Changed)
	return diff
}

func parseEnv(content []byte) map[string]string {
	values := map[string]string{}
	for _, line := range bytes.Split(content, []byte("\n")) {
		text := strings.TrimSpace(string(line)) // also drops a trailing \r
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(text, "export "); ok {
			text = strings.TrimSpace(rest)
		}
		key, value, found := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			continue
		}
		values[key] = value
	}
	return values
}
