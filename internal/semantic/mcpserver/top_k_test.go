package mcpserver

import (
	"encoding/json"
	"testing"
)

// EST-8278 review: the CLI now rejects `--top 0` (exit 2), so a top_k the MCP
// schema advertised as legal turned into a usage error. Every top_k schema
// must say its minimum, and a non-positive top_k must fall back to the CLI's
// default instead of being forwarded.

func TestTopKSchemasDeclareMinimumOne(t *testing.T) {
	seen := 0
	for _, def := range GetToolDefinitions() {
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatalf("%s: %v", def.Name, err)
		}
		prop, ok := schema.Properties["top_k"]
		if !ok {
			continue
		}
		seen++
		if prop["minimum"] != float64(1) {
			t.Errorf("%s: top_k minimum = %v, want 1", def.Name, prop["minimum"])
		}
	}
	if seen == 0 {
		t.Fatal("no tool declares top_k; the test is not checking anything")
	}
}

func TestNonPositiveTopKIsNotForwarded(t *testing.T) {
	builders := map[string]func(map[string]interface{}) []string{
		"search":        buildSearchArgs,
		"multisearch":   buildMultisearchArgs,
		"memory search": buildMemorySearchArgs,
	}
	for name, build := range builders {
		for _, k := range []float64{0, -3} {
			if argv := build(map[string]interface{}{"query": "q", "queries": []interface{}{"q"}, "top_k": k}); argvHas(argv, "--top") {
				t.Errorf("%s: top_k %v was forwarded: %v", name, k, argv)
			}
		}
		if argv := build(map[string]interface{}{"query": "q", "queries": []interface{}{"q"}, "top_k": float64(5)}); !argvHasFlag(argv, "--top", "5") {
			t.Errorf("%s: top_k 5 was not forwarded: %v", name, argv)
		}
	}
}
