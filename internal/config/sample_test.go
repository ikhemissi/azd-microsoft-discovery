package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSampleDefinitionCompatibility(t *testing.T) {
	source := os.Getenv("DISCOVERY_SAMPLE_DEFINITIONS_DIR")
	if source == "" {
		t.Skip("set DISCOVERY_SAMPLE_DEFINITIONS_DIR to the demo's lysozyme-water directory")
	}
	root := t.TempDir()
	document := applicationDocument(t, root)
	expected := map[string]map[string]any{}
	for target, original := range map[string]string{"tool.yaml": "tool.cpu.yaml", "agent.yaml": "agent.yaml"} {
		data, err := os.ReadFile(filepath.Join(source, original))
		if err != nil {
			t.Fatal(err)
		}
		definition, err := ParseDocument(data)
		if err != nil {
			t.Fatal(err)
		}
		expected[target] = definition
		writeFile(t, root, target, string(data))
	}
	settings := document["services"].(map[string]any)["agent"].(map[string]any)["config"].(map[string]any)
	settings["bindings"] = map[string]any{
		"gromacsToolId": map[string]any{"service": "tool", "output": "resourceId"},
	}
	project, err := Parse(root, document, noValues)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected["tool.yaml"], project.Services["tool"].Definition) ||
		!reflect.DeepEqual(expected["agent.yaml"], project.Services["agent"].Definition) {
		t.Fatal("sample definitions were modified during validation")
	}
}
