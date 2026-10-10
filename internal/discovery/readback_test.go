package discovery

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func omittedDescriptionReadback(t *testing.T) *Agent {
	t.Helper()
	// Synthetic identifiers/text preserve the reported GET's Succeeded/version-1
	// shape without copying credentials or project data.
	data, err := os.ReadFile("testdata/agent-readback-description-omitted.json")
	if err != nil {
		t.Fatal(err)
	}
	var agent Agent
	if err := json.Unmarshal(data, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.FoundryDetails.Versions.Latest.Description != nil {
		t.Fatal("fixture must omit the description")
	}
	return &agent
}

func TestAgentDescriptionReadbackContract(t *testing.T) {
	payload := contractPayload(t)
	for _, test := range []struct {
		name        string
		description *string
		matches     bool
	}{
		{name: "omitted", matches: true},
		{name: "returned matching", description: &payload.FoundryDetails.Description, matches: true},
		{name: "returned different", description: new("different-description")},
		{name: "returned explicitly empty", description: new("")},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := omittedDescriptionReadback(t)
			agent.FoundryDetails.Versions.Latest.Description = test.description
			err := VerifyAgent(agent, payload)
			if test.matches {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "foundryDetails.versions.latest.description") {
				t.Fatalf("authoritative description mismatch not diagnosed: %v", err)
			}
		})
	}
}

func TestAgentReadbackStrictFieldDiagnostics(t *testing.T) {
	payload := contractPayload(t)
	for _, test := range []struct {
		name, field string
		mutate      func(*Agent)
	}{
		{"name", "name", func(agent *Agent) { agent.Name = "unexpected" }},
		{"state", "provisioningState", func(agent *Agent) { agent.ProvisioningState = "Failed" }},
		{"missing versions", "foundryDetails.versions.latest", func(agent *Agent) { agent.FoundryDetails.Versions = nil }},
		{"missing version", "foundryDetails.versions.latest.version", func(agent *Agent) { agent.FoundryDetails.Versions.Latest.Version = "" }},
		{"blank version", "foundryDetails.versions.latest.version", func(agent *Agent) { agent.FoundryDetails.Versions.Latest.Version = " " }},
		{"model", "foundryDetails.versions.latest.definition.model", func(agent *Agent) { agent.FoundryDetails.Versions.Latest.Definition["model"] = "different-model" }},
		{"instructions", "foundryDetails.versions.latest.definition.instructions", func(agent *Agent) {
			agent.FoundryDetails.Versions.Latest.Definition["instructions"] = "sensitive-prompt-mismatch"
		}},
		{"kind", "foundryDetails.versions.latest.definition.kind", func(agent *Agent) { agent.FoundryDetails.Versions.Latest.Definition["kind"] = "hosted" }},
		{"definition missing", "foundryDetails.versions.latest.definition", func(agent *Agent) { agent.FoundryDetails.Versions.Latest.Definition = nil }},
		{"tool ID", "tools[0].toolId", func(agent *Agent) { agent.Tools.([]any)[0].(map[string]any)["toolId"] = "sensitive-tool-id" }},
		{"confirmation", "tools[0].confirmation", func(agent *Agent) { agent.Tools.([]any)[0].(map[string]any)["confirmation"] = "Disabled" }},
		{"missing confirmation", "tools[0].confirmation", func(agent *Agent) { delete(agent.Tools.([]any)[0].(map[string]any), "confirmation") }},
		{"tool count", "tools.length", func(agent *Agent) { agent.Tools = []any{} }},
		{"human confirmation", "humanInTheLoop", func(agent *Agent) { agent.HumanInTheLoop = "Disabled" }},
		{"extension flag", "discoveryExtensions.featureFlag", func(agent *Agent) { agent.DiscoveryExtensions["featureFlag"] = true }},
		{"extensions missing", "discoveryExtensions", func(agent *Agent) { agent.DiscoveryExtensions = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := omittedDescriptionReadback(t)
			test.mutate(agent)
			err := VerifyAgent(agent, payload)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected field-specific %s failure, got %v", test.field, err)
			}
			if strings.Contains(err.Error(), "sensitive-prompt-mismatch") || strings.Contains(err.Error(), "sensitive-tool-id") {
				t.Fatal("mismatch diagnostics leaked values")
			}
		})
	}
	if err := VerifyAgent(nil, payload); err == nil {
		t.Fatal("nil readback accepted")
	}
}
