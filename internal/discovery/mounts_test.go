package discovery

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func TestToolRuntimeMountGuidanceAndOutputConfigurationArePreserved(t *testing.T) {
	original := map[string]any{
		"name": "reusable-tool", "version": "1.0.0",
		"infra": []any{map[string]any{"name": "worker", "image": map[string]any{"acr": "original"}, "compute": map[string]any{"pool_size": float64(1)}}},
		"code_environments": []any{map[string]any{
			"language": "python3", "command": `python3 "/{{scriptName}}" > "{{output_directory}}/logs.out"`,
			"description": "GetResourceContext, inputMounts to /input, outputMounts for /output; never execute uploaded scripts.",
			"infra_node":  "worker",
		}},
		"output_mount_configurations": []any{map[string]any{
			"mount_path": "/output", "auto_promote": false, "output_name": "ReviewedResults",
		}},
	}
	before, err := Clone(original)
	if err != nil {
		t.Fatal(err)
	}
	service := &config.Service{Definition: original, Tool: &config.Tool{Image: config.Image{InfraNode: "worker"}}}
	image := "example.azurecr.io/reusable@sha256:" + strings.Repeat("a", 64)
	adapted, err := ToolDefinition(service, image)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := Clone(original)
	if err != nil {
		t.Fatal(err)
	}
	expected["infra"].([]any)[0].(map[string]any)["image"].(map[string]any)["acr"] = image
	if !reflect.DeepEqual(adapted, expected) || !reflect.DeepEqual(original, before) {
		t.Fatal("runtime guidance, output mount settings or source definition changed during deployment")
	}
}
