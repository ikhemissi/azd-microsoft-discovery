package config

import (
	"strings"
	"testing"
)

func knowledgeBaseDocument() map[string]any {
	return map[string]any{
		"name": "demo",
		"discovery": map[string]any{"schemaVersion": "1.0", "workspaceResourceId": workspace,
			"projectResourceId": workspace + "/projects/research"},
		"services": map[string]any{"papers": map[string]any{
			"host": KnowledgeBaseHost, "config": map[string]any{
				"bookshelfName": "literature",
				"sources":       []any{map[string]any{"storageAssetResourceId": scope + "/storageContainers/data/storageAssets/papers"}},
			},
		}},
	}
}

func scopeValues(name string) (string, bool) {
	values := map[string]string{"AZURE_SUBSCRIPTION_ID": "00000000-0000-0000-0000-000000000003", "AZURE_RESOURCE_GROUP": "selected"}
	value, exists := values[name]
	return value, exists
}

func TestKnowledgeBaseDefaultsOverridesAndDeferredOutputs(t *testing.T) {
	root := t.TempDir()
	document := knowledgeBaseDocument()
	project, err := Parse(root, document, scopeValues)
	if err != nil {
		t.Fatal(err)
	}
	kb := project.Services["papers"].KnowledgeBase
	if kb.Name != "papers" || kb.Autoindex == nil || !*kb.Autoindex || kb.ResourceGroup != "selected" ||
		kb.SubscriptionID != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("KB defaults did not follow selected environment: %+v", kb)
	}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("KB scope fell back to unrelated workspace scope")
	}
	options := document["services"].(map[string]any)["papers"].(map[string]any)["config"].(map[string]any)
	options["autoindex"] = false
	options["subscriptionId"] = "00000000-0000-0000-0000-000000000001"
	options["resourceGroup"] = "override"
	project, err = Parse(root, document, scopeValues)
	if err != nil || *project.Services["papers"].KnowledgeBase.Autoindex ||
		project.Services["papers"].KnowledgeBase.ResourceGroup != "override" {
		t.Fatalf("explicit KB options lost: %v", err)
	}
	delete(options, "subscriptionId")
	delete(options, "resourceGroup")
	options["bookshelfName"] = "${BOOKSHELF}"
	options["sources"] = []any{map[string]any{"storageAssetResourceId": "${SOURCE}"}}
	if _, err := ParseForPackage(root, document, noValues); err != nil {
		t.Fatalf("Package required provisioned KB inputs: %v", err)
	}
	if _, err := Parse(root, document, scopeValues); err == nil {
		t.Fatal("Deploy accepted missing KB provisioning outputs")
	}
}

func TestKnowledgeBaseInvalidConfiguration(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"name":         func(c map[string]any) { c["name"] = strings.Repeat("x", 25) },
		"bookshelf":    func(c map[string]any) { c["bookshelfName"] = "../other" },
		"subscription": func(c map[string]any) { c["subscriptionId"] = "00000000-0000-0000-0000-000000000000" },
		"group":        func(c map[string]any) { c["resourceGroup"] = "unsafe/group" },
		"no sources":   func(c map[string]any) { c["sources"] = []any{} },
		"blob": func(c map[string]any) {
			c["sources"] = []any{map[string]any{"storageAssetResourceId": "https://blob.example/papers"}}
		},
		"duplicate": func(c map[string]any) {
			c["sources"] = []any{map[string]any{"storageAssetResourceId": scope + "/storageContainers/data/storageAssets/papers"},
				map[string]any{"storageAssetResourceId": strings.ToUpper(scope + "/storageContainers/data/storageAssets/papers")}}
		},
		"identity": func(c map[string]any) { c["userAssignedIdentity"] = "not-supported" },
		"pool":     func(c map[string]any) { c["nodePoolResourceId"] = workspace },
		"bool":     func(c map[string]any) { c["autoindex"] = "false" },
	} {
		t.Run(name, func(t *testing.T) {
			document := knowledgeBaseDocument()
			change(document["services"].(map[string]any)["papers"].(map[string]any)["config"].(map[string]any))
			if _, err := Parse(t.TempDir(), document, scopeValues); err == nil {
				t.Fatal("invalid KB input accepted")
			}
		})
	}
	document := knowledgeBaseDocument()
	services := document["services"].(map[string]any)
	services["second"] = map[string]any{"host": KnowledgeBaseHost, "config": map[string]any{
		"name": "papers", "bookshelfName": "literature", "sources": []any{map[string]any{
			"storageAssetResourceId": scope + "/storageContainers/data/storageAssets/papers"}},
	}}
	if _, err := Parse(t.TempDir(), document, scopeValues); err == nil {
		t.Fatal("duplicate managed KB accepted")
	}
	delete(services, "second")
	services["papers"].(map[string]any)["language"] = "docker"
	if _, err := Parse(t.TempDir(), document, scopeValues); err == nil {
		t.Fatal("KB accepted Docker build settings")
	}
}
