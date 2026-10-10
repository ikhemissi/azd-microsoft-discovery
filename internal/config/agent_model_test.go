package config

import (
	"strings"
	"testing"
)

func TestDirectAgentModelConfiguration(t *testing.T) {
	for _, manifest := range []string{
		"",
		"model: {id: '{{OLD-MODEL}}'}\n",
		"model: {id: old-deployment}\n",
	} {
		t.Run(manifest, func(t *testing.T) {
			root := t.TempDir()
			document := agentDocument(t, root)
			services := document["services"].(map[string]any)
			services["agent"] = map[string]any{
				"host": AgentHost, "config": map[string]any{
					"model": map[string]any{"deploymentName": "${MODEL_NAME}"},
				},
			}
			writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\n"+manifest+"instructions: Preserve ${LITERAL}.\n")
			if _, err := ParseForPackage(root, document, noValues); err != nil {
				t.Fatalf("package required provisioning outputs: %v", err)
			}
			if _, err := Parse(root, document, noValues); err == nil {
				t.Fatal("deployment accepted an unresolved name")
			}
			project, err := Parse(root, document, func(name string) (string, bool) {
				return "gpt-5-4", name == "MODEL_NAME"
			})
			if err != nil {
				t.Fatal(err)
			}
			if project.Services["agent"].Agent.Model.DeploymentName != "gpt-5-4" ||
				project.Services["agent"].Definition["instructions"] != "Preserve ${LITERAL}." {
				t.Fatal("direct name or instructions changed")
			}
		})
	}
}

func TestDirectAgentModelRejectsInvalidNames(t *testing.T) {
	for _, name := range []string{"", " ", workspace + "/chatModelDeployments/chat", "../chat", "chat/name", "{{MODEL}}", "name?query", "chat name"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			document := agentDocument(t, root)
			writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\ninstructions: Keep unchanged.\n")
			document["services"].(map[string]any)["agent"] = map[string]any{
				"host": AgentHost, "config": map[string]any{"model": map[string]any{"deploymentName": name}},
			}
			if _, err := ParseForPackage(root, document, noValues); err == nil {
				t.Fatal("package accepted an invalid literal deployment name")
			}
		})
	}
}

func TestContainerToolType(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	project, err := Parse(root, document, noValues)
	if err != nil || project.Services["tool"].Tool.Type != "container" {
		t.Fatalf("container default missing: %v", err)
	}
	settings := document["services"].(map[string]any)["tool"].(map[string]any)["config"].(map[string]any)
	for _, kind := range []string{"container", "mcp", "openapi"} {
		settings["type"] = kind
		_, err := Parse(root, document, noValues)
		if kind == "container" && err != nil {
			t.Fatal(err)
		}
		if kind != "container" && (err == nil || !strings.Contains(err.Error(), "schema")) {
			t.Fatalf("unimplemented tool type %s accepted: %v", kind, err)
		}
	}
}
