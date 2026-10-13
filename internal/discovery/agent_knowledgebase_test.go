package discovery

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func TestAgentKBAttachmentUsesServiceIDAtNativePayloadLocation(t *testing.T) {
	for _, id := range []string{"server-returned-opaque-id", "/bookshelves/literature/knowledgeBases/papers",
		"/bookshelves/literature/knowledgeBases/papers/versions/server-returned-version"} {
		t.Run(id, func(t *testing.T) {
			service := &config.Service{
				Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "chat"},
					KnowledgeBases: []config.KnowledgeBaseBinding{{Service: "literature"}}},
				Definition: map[string]any{"name": "researcher", "kind": "prompt", "instructions": "Preserve {{runtime}} and ${LITERAL}.",
					"discoveryExtensions": map[string]any{"humanInTheLoop": "Enabled"}},
			}
			before, err := Clone(service.Definition)
			if err != nil {
				t.Fatal(err)
			}
			definition, err := AgentDefinition(service, map[string]map[string]string{"literature": {"knowledgeBaseId": id}})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := Payload(definition)
			if err != nil || len(payload.KnowledgeBases) != 1 || payload.KnowledgeBases[0].KnowledgeBaseID != id {
				t.Fatalf("service-returned attachment ID was changed or guessed: %+v (%v)", payload, err)
			}
			if !reflect.DeepEqual(before, service.Definition) || payload.FoundryDetails.Definition["instructions"] != before["instructions"] {
				t.Fatal("attachment adaptation changed source definitions or prompts")
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["knowledgeBases"] == nil ||
				wire["discoveryExtensions"].(map[string]any)["knowledgeBases"] != nil ||
				payload.FoundryDetails.Definition["knowledgeBases"] != nil {
				t.Fatal("attachment leaked into the Foundry definition or pass-through extensions")
			}
		})
	}
}

// This fixture models the documented payload, not a live GA attachment certification.
func TestAgentKBAttachmentIndependentReadbackAndMismatchChecks(t *testing.T) {
	encoded, err := os.ReadFile("testdata/agent-kb-readback.json")
	if err != nil {
		t.Fatal(err)
	}
	var remote Agent
	if err := json.Unmarshal(encoded, &remote); err != nil {
		t.Fatal(err)
	}
	expected := &AgentPayload{Name: "researcher", HumanInTheLoop: "Enabled",
		KnowledgeBases:      []KnowledgeBaseReference{{KnowledgeBaseID: "/bookshelves/literature/knowledgeBases/papers"}},
		DiscoveryExtensions: map[string]any{}, FoundryDetails: FoundryDetails{Definition: map[string]any{
			"kind": "prompt", "model": "chat", "instructions": "Preserve {{runtime}} and ${LITERAL}.",
		}}}
	if err := VerifyAgent(&remote, expected); err != nil {
		t.Fatal(err)
	}
	for _, actual := range [][]KnowledgeBaseReference{
		nil, {{KnowledgeBaseID: "wrong"}}, {{KnowledgeBaseID: ""}},
		{{KnowledgeBaseID: expected.KnowledgeBases[0].KnowledgeBaseID}, {KnowledgeBaseID: expected.KnowledgeBases[0].KnowledgeBaseID}},
	} {
		remote.KnowledgeBases = actual
		if err := VerifyAgent(&remote, expected); err == nil {
			t.Fatal("missing/wrong/duplicate attachment was accepted")
		}
	}
	expected.KnowledgeBases = []KnowledgeBaseReference{{KnowledgeBaseID: "first"}, {KnowledgeBaseID: "second"}}
	remote.KnowledgeBases = []KnowledgeBaseReference{{KnowledgeBaseID: "second"}, {KnowledgeBaseID: "first"}}
	if err := VerifyAgent(&remote, expected); err != nil {
		t.Fatal("server-reordered attachment set was rejected")
	}
}

func TestMissingOrInvalidKBAttachmentIDsCannotProducePayload(t *testing.T) {
	for _, id := range []string{"", "${MISSING}", "{{binding}}", "private\nvalue", strings.Repeat("x", 2049)} {
		service := &config.Service{Agent: &config.Agent{
			Model: &config.AgentModel{DeploymentName: "chat"}, KnowledgeBases: []config.KnowledgeBaseBinding{{Service: "literature"}},
		}, Definition: map[string]any{"name": "researcher", "kind": "prompt", "instructions": "unchanged"}}
		if _, err := AgentDefinition(service, map[string]map[string]string{"literature": {"knowledgeBaseId": id}}); err == nil {
			t.Fatal("an unavailable/unresolved identifier was accepted")
		}
	}
}
