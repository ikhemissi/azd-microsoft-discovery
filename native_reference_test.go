package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeReferencePackage(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_NATIVE_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_NATIVE_TESTS=1 with this extension installed")
	}
	if os.Getenv("AZD_CONFIG_DIR") == "" {
		t.Fatal("native tests require isolated AZD_CONFIG_DIR")
	}
	root := t.TempDir()
	image := "example.azurecr.io/demo@sha256:" + strings.Repeat("a", 64)
	for name, content := range map[string]string{
		"azure.yaml": `name: native-reference
discovery:
  schemaVersion: "1.0"
  workspaceResourceId: ${WORKSPACE}
  projectResourceId: ${PROJECT}
  nodePoolResourceId: ${NODEPOOL}
services:
  simulation:
    host: discovery.tool
    project: ./worker
    config:
      definition: ./tool.yaml
      image:
        mode: reference
        infraNode: worker
        reference: ` + image + "\n",
		"worker/tool.yaml":   "name: native-reference\nversion: 1.0.0\ninfra:\n  - name: worker\n    image:\n      acr: old:tag\n",
		"worker/Dockerfile":  "THIS FILE MUST NOT BE BUILT\n",
		".azure/config.json": `{"defaultEnvironment":"demo"}`,
		".azure/demo/.env":   "AZURE_ENV_NAME=\"demo\"\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("azd", "package", "simulation", "--cwd", root, "--environment", "demo", "--no-prompt")
	command.Env = append(os.Environ(), "AZD_CONTAINER_RUNTIME=unavailable-runtime")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), image) {
		t.Fatalf("reference packaging did not skip container runtime or preserve image: %v\n%s", err, output)
	}
}
