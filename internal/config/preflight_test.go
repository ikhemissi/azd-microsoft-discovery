package config

import (
	"strings"
	"testing"
)

func TestDuplicateManagedTargetsRejected(t *testing.T) {
	for _, source := range []string{"tool", "agent"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			document := applicationDocument(t, root)
			services := document["services"].(map[string]any)
			services[source+"-copy"] = services[source]
			if _, err := Parse(root, document, noValues); err == nil || !strings.Contains(err.Error(), "same Discovery target") {
				t.Fatalf("duplicate managed target accepted: %v", err)
			}
		})
	}
}

func TestAgentPreflightRejectsUnsupportedFields(t *testing.T) {
	for _, extra := range []string{
		"temperature: 0.2\n", "displayName: Assistant\n", "description: [invalid]\n",
		"discoveryExtensions:\n  knowledgeBases: []\n",
		"discoveryExtensions:\n  tools: [{toolId: 42}]\n",
	} {
		t.Run(extra, func(t *testing.T) {
			root := t.TempDir()
			document := agentDocument(t, root)
			document["services"].(map[string]any)["agent"] = map[string]any{
				"host": AgentHost, "config": map[string]any{"model": map[string]any{"deploymentName": "${MODEL}"}}}
			writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\ninstructions: Keep unchanged.\n"+extra)
			if _, err := ParseForPackage(root, document, noValues); err == nil {
				t.Fatal("unsupported request shape passed Package")
			}
		})
	}
}
