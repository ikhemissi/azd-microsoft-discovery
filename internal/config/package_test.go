package config

import "testing"

func TestPackageDefersOnlyInfrastructure(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	document["discovery"].(map[string]any)["workspaceResourceId"] = "${WORKSPACE}"
	document["discovery"].(map[string]any)["projectResourceId"] = "${PROJECT}"
	document["services"].(map[string]any)["agent"].(map[string]any)["config"].(map[string]any)["model"].(map[string]any)["deploymentName"] = "${MODEL}"
	document["services"].(map[string]any)["tool"].(map[string]any)["config"].(map[string]any)["image"].(map[string]any)["reference"] = "${IMMUTABLE_IMAGE}"
	if _, err := ParseForPackage(root, document, noValues); err != nil {
		t.Fatalf("package requires provisioned model outputs: %v", err)
	}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("deployment accepted unresolved infrastructure")
	}
	document["discovery"].(map[string]any)["workspaceResourceId"] = "malformed"
	if _, err := ParseForPackage(root, document, noValues); err == nil {
		t.Fatal("package swallowed malformed literal ID")
	}
}
