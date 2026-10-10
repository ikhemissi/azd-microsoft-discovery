package discovery

import (
	"reflect"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func TestAgentDirectModelOverridesOnlyDeploymentCopy(t *testing.T) {
	for _, original := range []any{nil, map[string]any{"id": "{{OLD-MODEL}}"}, map[string]any{"id": "old"}} {
		service := &config.Service{
			Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "gpt-5-4"}},
			Definition: map[string]any{
				"name": "researcher", "kind": "prompt", "instructions": "Preserve {{runtime}} and ${LITERAL}.",
			},
		}
		if original != nil {
			service.Definition["model"] = original
		}
		before, err := Clone(service.Definition)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := AgentDefinition(service, nil)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := Payload(definition)
		if err != nil || payload.FoundryDetails.Definition["model"] != "gpt-5-4" {
			t.Fatalf("direct model adapter failed: %v", err)
		}
		if !reflect.DeepEqual(before, service.Definition) ||
			definition["instructions"] != service.Definition["instructions"] {
			t.Fatal("source definition or instructions changed")
		}
	}
}
