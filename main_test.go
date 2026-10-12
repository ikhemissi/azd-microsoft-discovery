package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestManifestAgainstPinnedSDKSchema(t *testing.T) {
	directory, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/azure/azure-dev/cli/azd").Output()
	if err != nil {
		t.Fatalf("locate pinned SDK schema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(directory)), "extensions", "extension.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	document, err := config.ReadDocument("extension.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://schemas.azd-discovery.invalid/extension.json"
	if err := compiler.AddResource(location, schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(document); err != nil {
		t.Fatal(err)
	}
	capabilities := document["capabilities"].([]any)
	for _, capability := range capabilities {
		if capability != "custom-commands" && capability != "metadata" && capability != "lifecycle-events" && capability != "service-target-provider" {
			t.Fatal("manifest advertises an unimplemented capability")
		}
	}
	if providers, ok := document["providers"].([]any); !ok || len(providers) != 3 {
		t.Fatal("manifest must register tool, agent, and knowledge-base targets")
	}
}
