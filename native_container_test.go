package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeContainerPackage(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_CONTAINER_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_CONTAINER_TESTS=1 with Podman and this extension installed")
	}
	if os.Getenv("AZD_CONFIG_DIR") == "" {
		t.Fatal("native tests require isolated AZD_CONFIG_DIR")
	}
	root := t.TempDir()
	image := "discovery-native-" + strings.ToLower(filepath.Base(filepath.Dir(root))) + ":local"
	t.Cleanup(func() {
		// Packaging can fail after creating an image, before returning artifacts.
		if output, err := exec.Command("podman", "image", "rm", "--ignore", image).CombinedOutput(); err != nil {
			t.Errorf("cleanup of fixture image failed: %v %s", err, output)
		}
	})
	for name, content := range map[string]string{
		"azure.yaml": `name: native-container
requiredVersions:
  azd: ">=1.35.0"
discovery:
  schemaVersion: "1.0"
  workspaceResourceId: ${DISCOVERY_WORKSPACE_ID}
  projectResourceId: ${DISCOVERY_PROJECT_ID}
  nodePoolResourceId: ${DISCOVERY_NODE_POOL_ID}
services:
  simulation:
    host: discovery.tool
    project: ./worker
    language: docker
    docker:
      path: ./container/Containerfile
      target: discovery
      platform: linux/amd64
      registry: ${AZURE_CONTAINER_REGISTRY_ENDPOINT}
      image: IMAGE_NAME
      tag: local
  researcher:
    host: discovery.agent
    project: ./agent
    uses: [simulation]
    config:
      model:
        deploymentName: ${DISCOVERY_CHAT_MODEL_DEPLOYMENT_NAME}
      bindings:
        toolId:
          service: simulation
          output: resourceId
`,
		"worker/container/Containerfile": "FROM scratch AS discovery\nCOPY payload /payload\nFROM scratch AS unrelated\nCOPY must-not-build /missing\n",
		"worker/payload":                 "offline Discovery lifecycle fixture\n",
		"worker/tool.yaml":               "name: native-fixture\nversion: 1.0.0\ninfra:\n  - name: worker\n    image:\n      acr: example.azurecr.io/tool:initial\n",
		"agent/agent.yaml":               "name: native-researcher\nkind: prompt\nmodel: {id: '{{OLD-MODEL}}'}\ninstructions: Preserve {{runtime}}.\ndiscoveryExtensions:\n  tools:\n    - toolId: '{{toolId}}'\n",
		".azure/config.json":             `{"defaultEnvironment":"demo"}`,
		".azure/demo/.env":               "AZURE_ENV_NAME=\"demo\"\n",
	} {
		content = strings.ReplaceAll(content, "IMAGE_NAME", strings.TrimSuffix(image, ":local"))
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	process := exec.Command("azd", "package", "simulation", "--cwd", root, "--environment", "demo", "--no-prompt")
	process.Env = append(os.Environ(), "AZD_CONTAINER_RUNTIME=podman", "AZD_COLLECT_TELEMETRY=no",
		"REGISTRY_AUTH_FILE="+filepath.Join(root, "registry-auth.json"))
	output, err := process.CombinedOutput()
	if err != nil {
		t.Fatalf("native offline package failed: %v\n%s", err, output)
	}
	t.Logf("native package output:\n%s", output)
	inspect := exec.Command("podman", "image", "inspect", image, "--format", "{{.Os}}/{{.Architecture}}")
	inspected, err := inspect.CombinedOutput()
	if err != nil || strings.TrimSpace(string(inspected)) != "linux/amd64" {
		t.Fatalf("native package did not build requested platform/image: %v %s", err, inspected)
	}
	agent := exec.Command("azd", "package", "researcher", "--cwd", root, "--environment", "demo", "--no-prompt")
	agent.Env = append(os.Environ(), "AZD_CONTAINER_RUNTIME=unavailable-discovery-fixture", "AZD_COLLECT_TELEMETRY=no",
		"REGISTRY_AUTH_FILE="+filepath.Join(root, "registry-auth.json"))
	if output, err := agent.CombinedOutput(); err != nil {
		t.Fatalf("native agent package required model/tool outputs or a container runtime: %v\n%s", err, output)
	} else {
		t.Logf("native direct-model agent package output:\n%s", output)
	}
}
