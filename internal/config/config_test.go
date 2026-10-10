package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const scope = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery"
const workspace = scope + "/workspaces/demo"

func noValues(string) (string, bool) { return "", false }

func agentDocument(t *testing.T, root string) map[string]any {
	t.Helper()
	writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\nmodel: {id: chat}\ninstructions: Keep unchanged.\n")
	document, err := ParseDocument([]byte(`
name: demo
discovery:
  schemaVersion: "1.0"
  workspaceResourceId: ` + workspace + `
  projectResourceId: ` + workspace + `/projects/research
services:
  agent:
    host: discovery.agent
    config:
      model:
        deploymentName: chat
`))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func applicationDocument(t *testing.T, root string) map[string]any {
	t.Helper()
	document := agentDocument(t, root)
	document["discovery"].(map[string]any)["nodePoolResourceId"] = scope + "/supercomputers/cpu/nodePools/workers"
	services := document["services"].(map[string]any)
	services["tool"] = map[string]any{
		"host": ToolHost,
		"config": map[string]any{
			"definition": "tool.yaml",
			"image": map[string]any{
				"mode": "reference", "infraNode": "worker",
				"reference": "example.azurecr.io/demo@sha256:" + strings.Repeat("a", 64),
			},
		},
	}
	services["agent"] = map[string]any{
		"host": AgentHost, "uses": []any{"tool"},
		"config": map[string]any{
			"definition": "agent.yaml",
			"model":      map[string]any{"deploymentName": "chat"},
			"bindings": map[string]any{
				"toolId": map[string]any{"service": "tool", "output": "resourceId"},
			},
		},
	}
	writeFile(t, root, "tool.yaml", `
name: example-tool
version: 1.0.0
infra:
  - name: worker
    image:
      acr: example.azurecr.io/demo:initial
code_environments:
  - command: 'python "/{{scriptName}}" > "{{output_directory}}/logs.out"'
`)
	writeFile(t, root, "agent.yaml", `
name: researcher
kind: prompt
model:
  id: '{{CHAT-MODEL}}'
instructions: |
  Preserve ${LITERAL} and {{instructional braces}}.
  Ask for approval before execution.
discoveryExtensions:
  humanInTheLoop: true
  tools:
    - toolId: '{{toolId}}'
`)
	return document
}

func TestAgentDefaultsAndScope(t *testing.T) {
	root := t.TempDir()
	project, err := Parse(root, agentDocument(t, root), noValues)
	if err != nil {
		t.Fatal(err)
	}
	if project.Discovery.OperationTimeoutSeconds != 1800 || project.Discovery.NodePoolResourceID != "" {
		t.Fatal("agent-only workflow should apply timeout without requiring compute")
	}
	if Alias("simulation-tool", "resourceId") != "SERVICE_SIMULATION_TOOL_RESOURCE_ID" {
		t.Fatal("incorrect alias normalization")
	}
}

func TestApplicationPreservesDefinitions(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	before, err := ReadDocument(filepath.Join(root, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := Parse(root, document, noValues)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, project.Services["agent"].Definition) {
		t.Fatal("validation changed the agent definition")
	}
	command := project.Services["tool"].Definition["code_environments"].([]any)[0].(map[string]any)["command"]
	if command != `python "/{{scriptName}}" > "{{output_directory}}/logs.out"` {
		t.Fatal("runtime placeholders changed")
	}
}

func TestInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		change  func(map[string]any)
		message string
	}{
		{"schema version", func(d map[string]any) { d["discovery"].(map[string]any)["schemaVersion"] = "2.0" }, "schema"},
		{"unknown key", func(d map[string]any) { d["discovery"].(map[string]any)["invented"] = true }, "schema"},
		{"timeout ceiling", func(d map[string]any) { d["discovery"].(map[string]any)["operationTimeoutSeconds"] = 7201 }, "schema"},
		{"wrong workspace type", func(d map[string]any) {
			d["discovery"].(map[string]any)["workspaceResourceId"] = scope + "/tools/wrong"
		}, "resource type"},
		{"project mismatch", func(d map[string]any) {
			d["discovery"].(map[string]any)["projectResourceId"] = scope + "/workspaces/other/projects/test"
		}, "must belong"},
		{"invalid model name", func(d map[string]any) {
			d["services"].(map[string]any)["agent"].(map[string]any)["config"].(map[string]any)["model"].(map[string]any)["deploymentName"] = workspace + "/chatModelDeployments/chat"
		}, "deployment name"},
		{"unsupported host", func(d map[string]any) {
			d["services"].(map[string]any)["agent"].(map[string]any)["host"] = "discovery.invalid"
		}, "unsupported"},
		{"missing dependency", func(d map[string]any) {
			d["services"].(map[string]any)["agent"].(map[string]any)["uses"] = []any{"absent"}
		}, "missing service"},
		{"cycle", func(d map[string]any) {
			d["services"].(map[string]any)["agent"].(map[string]any)["uses"] = []any{"agent"}
		}, "cycle"},
		{"alias collision", func(d map[string]any) {
			s := d["services"].(map[string]any)
			s["agent-a"] = s["agent"]
			s["agent_a"] = s["agent"]
			delete(s, "agent")
		}, "collides"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			document := agentDocument(t, root)
			test.change(document)
			_, err := Parse(root, document, noValues)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q error, got %v", test.message, err)
			}
		})
	}
}

func TestBindingValidation(t *testing.T) {
	for _, scenario := range []string{"wrong output", "missing uses", "unused binding", "partial placeholder", "missing binding"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			document := applicationDocument(t, root)
			agent := document["services"].(map[string]any)["agent"].(map[string]any)
			bindings := agent["config"].(map[string]any)["bindings"].(map[string]any)
			switch scenario {
			case "wrong output":
				bindings["toolId"].(map[string]any)["output"] = "deploymentName"
			case "missing uses":
				delete(agent, "uses")
			case "unused binding":
				bindings["unused"] = map[string]any{"service": "tool", "output": "resourceId"}
			case "partial placeholder":
				data, err := os.ReadFile(filepath.Join(root, "agent.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, root, "agent.yaml", strings.ReplaceAll(string(data), "'{{toolId}}'", "'prefix-{{toolId}}'"))
			case "missing binding":
				delete(bindings, "toolId")
			}
			if _, err := Parse(root, document, noValues); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestEnvironmentExpansion(t *testing.T) {
	root := t.TempDir()
	document := agentDocument(t, root)
	document["discovery"].(map[string]any)["workspaceResourceId"] = "${WORKSPACE}"
	project, err := Parse(root, document, func(name string) (string, bool) {
		return workspace, name == "WORKSPACE"
	})
	if err != nil || project.Discovery.WorkspaceResourceID != workspace {
		t.Fatalf("expansion failed: %v", err)
	}
	if document["discovery"].(map[string]any)["workspaceResourceId"] != "${WORKSPACE}" {
		t.Fatal("expansion mutated the input")
	}
	for _, value := range []string{"", "${SECOND}", "${MALFORMED"} {
		_, err := Parse(root, document, func(string) (string, bool) { return value, true })
		if err == nil {
			t.Fatalf("unresolved value %q accepted", value)
		}
	}
}

func TestBookshelfConfiguration(t *testing.T) {
	root := t.TempDir()
	document := agentDocument(t, root)
	document["services"].(map[string]any)["literature"] = map[string]any{
		"host": BookshelfHost,
		"config": map[string]any{
			"mode": "reference", "resourceId": scope + "/bookshelves/literature", "knowledgeBaseId": "opaque-kb",
			"indexing": map[string]any{"nodePoolResourceId": scope + "/supercomputers/index/nodePools/workers"},
		},
	}
	project, err := Parse(root, document, noValues)
	if err == nil || project != nil {
		t.Fatal("deferred Bookshelf host accepted")
	}
	config := document["services"].(map[string]any)["literature"].(map[string]any)["config"].(map[string]any)
	config["mode"] = "managed"
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("unsupported ownership accepted")
	}
}

func TestOCIReferences(t *testing.T) {
	for _, value := range []string{
		"example.azurecr.io/demo:latest", "demo@sha256:" + strings.Repeat("a", 64),
		"https://example.azurecr.io/demo@sha256:" + strings.Repeat("a", 64),
		"example.azurecr.io/demo:latest@sha256:" + strings.Repeat("a", 64),
		"example.azurecr.io/Uppercase@sha256:" + strings.Repeat("a", 64),
	} {
		if err := validateImage(value); err == nil {
			t.Errorf("invalid OCI reference accepted: %s", value)
		}
	}
	if err := validateImage("example.azurecr.io/demo@sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestToolPrerequisitesAndShapes(t *testing.T) {
	for _, scenario := range []string{"missing compute", "build flags in reference", "missing node", "duplicate node", "unsupported image"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			document := applicationDocument(t, root)
			tool := document["services"].(map[string]any)["tool"].(map[string]any)
			switch scenario {
			case "missing compute":
				delete(document["discovery"].(map[string]any), "nodePoolResourceId")
			case "build flags in reference":
				tool["language"] = "docker"
			case "missing node":
				tool["config"].(map[string]any)["image"].(map[string]any)["infraNode"] = "absent"
			case "duplicate node":
				writeFile(t, root, "tool.yaml", "name: example-tool\ninfra:\n  - name: worker\n    image: {acr: example}\n  - name: worker\n    image: {acr: example}\n")
			case "unsupported image":
				writeFile(t, root, "tool.yaml", "name: example-tool\ninfra:\n  - name: worker\n    image: {acr: example, alternate: unknown}\n")
			}
			if _, err := Parse(root, document, noValues); err == nil {
				t.Fatal("invalid tool configuration accepted")
			}
		})
	}
}

func TestKnowledgeBaseBindings(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	services := document["services"].(map[string]any)
	services["literature"] = map[string]any{
		"host":   BookshelfHost,
		"config": map[string]any{"mode": "reference", "resourceId": scope + "/bookshelves/literature", "knowledgeBaseId": "opaque"},
	}
	agent := services["agent"].(map[string]any)
	agent["uses"] = []any{"tool", "literature"}
	settings := agent["config"].(map[string]any)
	settings["knowledgeBases"] = []any{map[string]any{"service": "literature", "output": "knowledgeBaseId"}}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("deferred knowledge-base bindings accepted")
	}
	settings["knowledgeBases"] = []any{map[string]any{"service": "literature", "output": "resourceId"}}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("wrong KB output type accepted")
	}
	settings["knowledgeBases"] = []any{
		map[string]any{"service": "literature", "output": "knowledgeBaseId"},
		map[string]any{"service": "literature", "output": "knowledgeBaseId"},
	}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("duplicate KB attachment accepted")
	}
}
