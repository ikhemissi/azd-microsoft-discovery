package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func documentationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "experiments", "lysozyme-water")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, directory, "Dockerfile", "FROM scratch AS discovery\n")
	writeFile(t, directory, "tool.cpu.yaml",
		"name: documentation-tool\nversion: 1.0.0\ninfra:\n  - name: worker\n    image: {acr: original}\n")
	writeFile(t, directory, "agent.yaml",
		"name: researcher\nkind: prompt\nmodel: {id: '{{OLD-MODEL}}'}\ninstructions: Preserve prompts.\n"+
			"discoveryExtensions:\n  tools:\n    - toolId: '{{gromacsToolId}}'\n")
	return root
}

func documentValues(name string) (string, bool) {
	values := map[string]string{
		"DISCOVERY_WORKSPACE_ID":               workspace,
		"DISCOVERY_PROJECT_ID":                 workspace + "/projects/research",
		"DISCOVERY_NODE_POOL_ID":               scope + "/supercomputers/cpu/nodePools/worker",
		"AZURE_CONTAINER_REGISTRY_ENDPOINT":    "example.azurecr.io",
		"DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME": "chat",
		"DISCOVERY_TOOL_IMAGE_DIGEST":          "example.azurecr.io/tool@sha256:" + strings.Repeat("a", 64),
	}
	value, exists := values[name]
	return value, exists
}

func TestPublishedAzureYAMLExamples(t *testing.T) {
	for _, name := range []string{"gromacs-build"} {
		t.Run(name, func(t *testing.T) {
			document, err := ReadDocument(filepath.Join("..", "..", "examples", name, "azure.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			root := documentationFixture(t)
			if _, err := ParseForPackage(root, document, noValues); err != nil {
				t.Fatalf("pre-provision Package example: %v", err)
			}
			if _, err := Parse(root, document, documentValues); err != nil {
				t.Fatalf("provisioned example: %v", err)
			}
		})
	}
}

func TestREADMEYAMLExamples(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	base, err := ReadDocument("../../examples/gromacs-build/azure.yaml")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, block := range strings.Split(string(data), "```yaml\n")[1:] {
		source, _, found := strings.Cut(block, "```")
		if !found {
			t.Fatal("unterminated YAML example")
		}
		snippet, err := ParseDocument([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		services := base["services"].(map[string]any)
		if settings, exists := snippet["config"]; exists {
			tool := services["simulation"].(map[string]any)
			delete(tool, "docker")
			delete(tool, "language")
			tool["config"] = settings
		} else if agent, exists := snippet["researcher"]; exists {
			services["researcher"] = agent
		} else {
			t.Fatal("unrecognized README YAML example; add validation for its shape")
		}
		if _, err := Parse(documentationFixture(t), base, documentValues); err != nil {
			t.Fatalf("README example failed runtime validation: %v", err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no README YAML examples validated")
	}
}
