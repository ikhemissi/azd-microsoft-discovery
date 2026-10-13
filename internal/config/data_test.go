package config

import "testing"

func TestDataConfigurationIsDeferredAndDoesNotReadLocalBundles(t *testing.T) {
	root := t.TempDir()
	document := agentDocument(t, root)
	document["discovery"].(map[string]any)["data"] = map[string]any{
		"sources": map[string]any{"inputs": map[string]any{
			"storageContainerResourceId": "${DISCOVERY_INPUTS_ID}", "blobContainerResourceId": "${BLOB_INPUTS_ID}", "prefix": "approved",
		}},
		"datasets": map[string]any{"experiment": map[string]any{
			"source": "inputs", "directory": "./not-prepared-yet", "namespace": "experiment", "version": "v1", "overwrite": false,
			"storageAssetResourceId": "${EXPERIMENT_ASSET_ID}",
		}},
	}
	project, err := ParseForPackage(root, document, noValues)
	if err != nil || project.Discovery.Data.Datasets["experiment"].Manifest != "bundle.yaml" {
		t.Fatalf("declared data blocked packaging or lost defaults: %v", err)
	}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("unresolved destination accepted during deployment")
	}
	values := func(name string) (string, bool) {
		switch name {
		case "DISCOVERY_INPUTS_ID":
			return scope + "/storageContainers/inputs", true
		case "BLOB_INPUTS_ID":
			return "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Storage/storageAccounts/account/blobServices/default/containers/inputs", true
		case "EXPERIMENT_ASSET_ID":
			return scope + "/storageContainers/inputs/storageAssets/experiment-v1", true
		}
		return "", false
	}
	if _, err := Parse(root, document, values); err != nil {
		t.Fatal(err)
	}
	data := document["discovery"].(map[string]any)["data"].(map[string]any)
	dataset := data["datasets"].(map[string]any)["experiment"].(map[string]any)
	for _, field := range []string{"overwrite", "source", "namespace", "version"} {
		old := dataset[field]
		if field == "overwrite" {
			dataset[field] = true
		} else {
			dataset[field] = "../bad"
		}
		if _, err := Parse(root, document, values); err == nil {
			t.Fatalf("unsafe data %s accepted", field)
		}
		dataset[field] = old
	}
	for _, asset := range []string{workspace, "https://example.invalid/asset", scope + "/storageContainers/foreign/storageAssets/experiment-v1"} {
		dataset["storageAssetResourceId"] = asset
		if _, err := Parse(root, document, values); err == nil {
			t.Fatal("invalid or foreign asset declaration accepted")
		}
	}
}
