package config

import (
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/schemas"
)

func TestRemovedModelServiceRejected(t *testing.T) {
	root := t.TempDir()
	document := agentDocument(t, root)
	document["services"].(map[string]any)["chat"] = map[string]any{
		"host":   "discovery.model",
		"config": map[string]any{"resourceId": workspace + "/chatModelDeployments/chat"},
	}
	if _, err := Parse(root, document, noValues); err == nil || !strings.Contains(err.Error(), "unsupported Discovery host") {
		t.Fatalf("removed host accepted: %v", err)
	}
	if outputs := Outputs("discovery.model"); len(outputs) != 0 {
		t.Fatal("removed host still advertises exports")
	}
	if _, err := schemas.Entry("model"); err == nil {
		t.Fatal("removed model service schema still exists")
	}
	schema, err := schemas.Compile("service")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{"resourceId": workspace + "/chatModelDeployments/chat"}); err == nil {
		t.Fatal("metadata accepts removed model reference configuration")
	}
}

func TestModelServiceBindingsRejected(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	bindings := document["services"].(map[string]any)["agent"].(map[string]any)["config"].(map[string]any)["bindings"].(map[string]any)
	bindings["toolId"].(map[string]any)["output"] = "deploymentName"
	if _, err := ParseForPackage(root, document, noValues); err == nil {
		t.Fatal("model deploymentName binding accepted")
	}
}
