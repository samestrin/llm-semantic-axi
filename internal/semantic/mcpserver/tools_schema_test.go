package mcpserver

import (
	"encoding/json"
	"testing"
)

// TestToolSchemas_NoJSONOrMinProperty defends against advertising output
// modes (json, min) the server cannot produce: the server always runs the CLI
// with --axi, so a client setting these would be silently ignored.
func TestToolSchemas_NoJSONOrMinProperty(t *testing.T) {
	defs := GetToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("GetToolDefinitions() returned no tools")
	}
	for _, def := range defs {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatalf("%s: invalid schema: %v", def.Name, err)
		}
		for _, banned := range []string{"json", "min"} {
			if _, ok := schema.Properties[banned]; ok {
				t.Errorf("%s advertises unsupported property %q", def.Name, banned)
			}
		}
	}
}
