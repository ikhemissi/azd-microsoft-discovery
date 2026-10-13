package config

import (
	"reflect"
	"testing"
)

func agentKnowledgeBaseDocument(t *testing.T, root string) map[string]any {
	t.Helper()
	document := agentDocument(t, root)
	document["services"].(map[string]any)["literature"] = map[string]any{
		"host":   KnowledgeBaseHost,
		"config": map[string]any{"bookshelfName": "${BOOKSHELF}", "sources": []any{map[string]any{"storageAssetResourceId": "${PAPERS}"}}},
	}
	agent := document["services"].(map[string]any)["agent"].(map[string]any)
	agent["uses"] = []any{"literature"}
	agent["config"].(map[string]any)["knowledgeBases"] = []any{map[string]any{"service": "literature"}}
	return document
}

func attachmentValues(name string) (string, bool) {
	values := map[string]string{
		"BOOKSHELF": "literature", "PAPERS": scope + "/storageContainers/data/storageAssets/papers",
		"AZURE_SUBSCRIPTION_ID": "00000000-0000-0000-0000-000000000001", "AZURE_RESOURCE_GROUP": "demo",
	}
	value, exists := values[name]
	return value, exists
}

func TestManagedKBAttachmentConfigurationAndDeferredProvisioning(t *testing.T) {
	root := t.TempDir()
	document := agentKnowledgeBaseDocument(t, root)
	before, err := ReadDocument(root + "/agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, packageOnly := range []bool{true, false} {
		var parsed *Project
		if packageOnly {
			parsed, err = ParseForPackage(root, document, noValues)
		} else {
			parsed, err = Parse(root, document, attachmentValues)
		}
		if err != nil || len(parsed.Services["agent"].Agent.KnowledgeBases) != 1 ||
			parsed.Services["agent"].Agent.KnowledgeBases[0].Service != "literature" {
			t.Fatalf("KB attachment rejected or changed: %v", err)
		}
		if !reflect.DeepEqual(before, parsed.Services["agent"].Definition) {
			t.Fatal("preflight injected a native KB ID or changed source instructions")
		}
	}
}

func TestManagedKBAttachmentRequiresDeclaredUniqueKBDependencies(t *testing.T) {
	for _, scenario := range []string{"missing", "not in uses", "wrong host", "duplicate", "output override", "native", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			document := agentKnowledgeBaseDocument(t, root)
			services := document["services"].(map[string]any)
			agent := services["agent"].(map[string]any)
			settings := agent["config"].(map[string]any)
			switch scenario {
			case "missing":
				delete(services, "literature")
			case "not in uses":
				delete(agent, "uses")
			case "wrong host":
				services["external"] = map[string]any{"host": "containerapp"}
				settings["knowledgeBases"] = []any{map[string]any{"service": "external"}}
				agent["uses"] = []any{"external"}
			case "duplicate":
				settings["knowledgeBases"] = []any{map[string]any{"service": "literature"}, map[string]any{"service": "literature"}}
			case "output override":
				settings["knowledgeBases"] = []any{map[string]any{"service": "literature", "output": "resourceId"}}
			case "native":
				writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\ninstructions: Preserve instructions.\ndiscoveryExtensions:\n  knowledgeBases:\n    - knowledgeBaseId: /bookshelves/book/knowledgeBases/papers\n")
			case "cycle":
				services["literature"].(map[string]any)["uses"] = []any{"agent"}
			}
			if _, err := ParseForPackage(root, document, noValues); err == nil {
				t.Fatal("unsafe KB attachment configuration passed preflight")
			}
		})
	}
}
