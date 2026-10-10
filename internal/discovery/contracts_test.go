package discovery

import (
	"encoding/json"
	"os"
	"testing"
)

// This independently authored fixture pins the adapter's contract, not a live
// Discovery certification. It is deliberately not an echo of the request.
func TestAgentAdapterAgainstIndependentReadback(t *testing.T) {
	data, err := os.ReadFile("testdata/agent-readback.json")
	if err != nil {
		t.Fatal(err)
	}
	var remote Agent
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	payload, err := Payload(map[string]any{
		"name": "researcher", "kind": "prompt", "model": map[string]any{"id": "chat"},
		"description": "Offline contract fixture", "instructions": "Keep {{runtime}} and ${LITERAL}.",
		"discoveryExtensions": map[string]any{
			"humanInTheLoop": "Enabled",
			"tools": []any{map[string]any{
				"toolId":       "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery/tools/demo-tool",
				"confirmation": "Enabled",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgent(&remote, payload); err != nil {
		t.Fatal(err)
	}
	remote.FoundryDetails.Versions.Latest.Definition["model"] = "unexpected"
	if err := VerifyAgent(&remote, payload); err == nil {
		t.Fatal("independent readback mismatch was accepted")
	}
}
