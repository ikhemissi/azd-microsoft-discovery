package config

import "testing"

func TestServiceDefaultsAndExplicitRegistry(t *testing.T) {
	root := t.TempDir()
	document := applicationDocument(t, root)
	services := document["services"].(map[string]any)
	tool := services["tool"].(map[string]any)
	tool["project"] = "."
	tool["language"] = "docker"
	tool["docker"] = map[string]any{"registry": "example.azurecr.io"}
	delete(tool, "config")
	writeFile(t, root, "Dockerfile", "FROM scratch\n")
	agent := services["agent"].(map[string]any)
	agentConfig := agent["config"].(map[string]any)
	delete(agentConfig, "definition")
	project, err := Parse(root, document, noValues)
	if err != nil {
		t.Fatal(err)
	}
	if project.Services["tool"].Tool.Definition != "tool.yaml" ||
		project.Services["tool"].Tool.Image.Mode != "build" ||
		project.Services["tool"].Tool.Image.InfraNode != "worker" ||
		project.Services["agent"].Agent.Definition != "agent.yaml" {
		t.Fatal("service defaults were not applied")
	}
	if _, exists := tool["config"]; exists {
		t.Fatal("defaults mutated the input document")
	}
	delete(tool, "docker")
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("build accepted an implicit registry")
	}
	tool["docker"] = map[string]any{"registry": "example.azurecr.io"}
	if _, err := Parse(root, document, func(name string) (string, bool) {
		return "different.azurecr.io", name == "AZURE_CONTAINER_REGISTRY_ENDPOINT"
	}); err == nil {
		t.Fatal("core registry override conflict was hidden")
	}
	writeFile(t, root, "tool.yaml", "name: example-tool\nversion: 1.0.0\ninfra:\n  - name: one\n    image: {acr: first}\n  - name: two\n    image: {acr: second}\n")
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("ambiguous infra target was guessed")
	}
	tool["config"] = map[string]any{"image": map[string]any{"infraNode": "two"}}
	if _, err := Parse(root, document, noValues); err != nil {
		t.Fatalf("explicit infra selection failed: %v", err)
	}
}

func TestAgentDefaultsWithoutBindings(t *testing.T) {
	root := t.TempDir()
	document := agentDocument(t, root)
	writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\nmodel: {id: research-chat}\ninstructions: Keep instructions unchanged.\n")
	document["services"].(map[string]any)["agent"] = map[string]any{"host": AgentHost}
	project, err := Parse(root, document, noValues)
	if err != nil {
		t.Fatal(err)
	}
	if project.Services["agent"].Agent.Definition != "agent.yaml" || len(project.Services["agent"].Agent.Bindings) != 0 {
		t.Fatal("agent defaults not applied")
	}
	document["services"].(map[string]any)["agent"].(map[string]any)["config"] = map[string]any{"definition": ""}
	if _, err := Parse(root, document, noValues); err == nil {
		t.Fatal("explicit invalid value was replaced with a default")
	}
}
