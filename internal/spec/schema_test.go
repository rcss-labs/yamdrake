package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var schemaKinds = map[string]string{
	"Environments": "environments",
	"Service":      "service",
	"Job":          "job",
}

func loadSchema(t *testing.T, kind string) map[string]any {
	t.Helper()
	path := filepath.Join("..", "..", "schemas", schemaKinds[kind]+".schema.json")
	data, err := os.ReadFile(path) //nolint:gosec // fixed schema path inside the repository
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return schema
}

// resolveRef follows a local "#/$defs/<name>" reference.
func resolveRef(t *testing.T, root, node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	name, found := strings.CutPrefix(ref, "#/$defs/")
	defs, _ := root["$defs"].(map[string]any)
	target, _ := defs[name].(map[string]any)
	if !found || target == nil {
		t.Fatalf("unresolvable $ref %q", ref)
	}
	return target
}

// schemaPaths lists every property path in a schema using the notation of
// KnownFields: ".name" for properties, "[]" for list items and ".*" for the
// single patternProperties entry that stands for free-form map keys.
// oneOf and anyOf branches are walked at the same path.
func schemaPaths(t *testing.T, root, node map[string]any, prefix string, out map[string]bool) {
	node = resolveRef(t, root, node)
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, child := range properties {
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			out[path] = true
			schemaPaths(t, root, child.(map[string]any), path, out)
		}
	}
	if patterns, ok := node["patternProperties"].(map[string]any); ok {
		if len(patterns) != 1 {
			t.Errorf("%s: patternProperties must have exactly one entry, has %d", prefix, len(patterns))
		}
		for _, child := range patterns {
			path := prefix + ".*"
			out[path] = true
			schemaPaths(t, root, child.(map[string]any), path, out)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		schemaPaths(t, root, items, prefix+"[]", out)
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if branches, ok := node[key].([]any); ok {
			for _, branch := range branches {
				schemaPaths(t, root, branch.(map[string]any), prefix, out)
			}
		}
	}
}

func TestSchemasListEveryYAMLField(t *testing.T) {
	for kind := range schemaKinds {
		t.Run(kind, func(t *testing.T) {
			schema := loadSchema(t, kind)
			inSchema := map[string]bool{}
			schemaPaths(t, schema, schema, "", inSchema)

			known := KnownFields(kind)
			if len(known) == 0 {
				t.Fatal("KnownFields returned nothing")
			}
			inParser := map[string]bool{}
			for _, path := range known {
				inParser[path] = true
				if !inSchema[path] {
					t.Errorf("parser accepts %q but the schema has no property at that path", path)
				}
			}
			var extra []string
			for path := range inSchema {
				if !inParser[path] {
					extra = append(extra, path)
				}
			}
			sort.Strings(extra)
			for _, path := range extra {
				t.Errorf("schema has property %q that the parser does not accept", path)
			}
		})
	}
}

// strictObjects reports every object schema that does not set additionalProperties to false.
func strictObjects(node any, where string, problems *[]string) {
	switch value := node.(type) {
	case map[string]any:
		_, hasProperties := value["properties"]
		_, hasPatterns := value["patternProperties"]
		if hasProperties || hasPatterns || value["type"] == "object" {
			if value["additionalProperties"] != false {
				*problems = append(*problems, where)
			}
		}
		for key, child := range value {
			strictObjects(child, where+"/"+key, problems)
		}
	case []any:
		for i, child := range value {
			strictObjects(child, fmt.Sprintf("%s/%d", where, i), problems)
		}
	}
}

func TestSchemasAreValidJSONWithStrictObjects(t *testing.T) {
	for kind, file := range schemaKinds {
		t.Run(kind, func(t *testing.T) {
			schema := loadSchema(t, kind)
			if got, want := schema["$schema"], "https://json-schema.org/draft/2020-12/schema"; got != want {
				t.Errorf("$schema = %v, want %q", got, want)
			}
			if got, want := schema["$id"], "https://yumdrake.dev/schemas/"+file+".schema.json"; got != want {
				t.Errorf("$id = %v, want %q", got, want)
			}
			var problems []string
			strictObjects(schema, "#", &problems)
			for _, where := range problems {
				t.Errorf("object schema at %s does not set additionalProperties to false", where)
			}
		})
	}
}
