package spec

// KnownFields lists every key the parser accepts for a file kind
// ("Environments", "Service" or "Job") as dotted paths, one entry per key
// including keys that only hold other keys. It returns nil for an unknown kind.
//
// Path notation:
//   - "." joins a key to its parent: "versions.pattern".
//   - "[]" follows a key whose value is a list; the keys of its items come
//     next: "environments[].name", "steps[].needs[].scope".
//   - "*" stands for a free-form map key (a service's environment name, a
//     job's group id) and is itself a path: "environments.*" is the block of
//     one environment, "environments.*.ecs_service" a key inside it.
//
// A "needs" item may also be a bare step name; only its mapping form has keys.
// The result is built from the same field tables the parser enforces.
func KnownFields(kind string) []string {
	var paths []string
	under := func(prefix string, keys []string) {
		for _, key := range keys {
			paths = append(paths, prefix+key)
		}
	}

	under("", headerFields)
	switch kind {
	case "Environments":
		under("", environmentsFile)
		under("environments[].", environmentFields)
	case "Service":
		under("", serviceFile)
		under("versions.", versionsFields)
		paths = append(paths, "environments.*")
		under("environments.*.", serviceEnvFields)
		under("environments.*.env_files[].", envFileFields)
		under("environments.*.tomls[].", tomlFields)
	case "Job":
		under("", jobFile)
		under("environment.", jobEnvironment)
		paths = append(paths, "groups.*")
		under("groups.*.", groupFields)
		under("approval.", approvalFields)
		under("approval.when.", approvalWhen)
		under("notify.", notifyFields)
		under("steps[].", stepFields)
		under("steps[].needs[].", needFields)
	default:
		return nil
	}
	return paths
}
