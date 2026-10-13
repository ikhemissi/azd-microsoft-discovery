package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeKnowledgeBasePackage(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_NATIVE_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_NATIVE_TESTS=1 with this extension installed")
	}
	if os.Getenv("AZD_CONFIG_DIR") == "" {
		t.Fatal("native tests require isolated AZD_CONFIG_DIR")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"azure.yaml": `name: native-knowledgebase
infra:
  provider: terraform
discovery:
  schemaVersion: "1.0"
  workspaceResourceId: ${WORKSPACE}
  projectResourceId: ${PROJECT}
services:
  literature:
    host: discovery.knowledgebase
    config:
      bookshelfName: ${BOOKSHELF}
      sources:
        - storageAssetResourceId: ${ASSET}
  researcher:
    host: discovery.agent
    uses: [literature]
    config:
      model:
        deploymentName: ${MODEL_NAME}
      knowledgeBases:
        - service: literature
`,
		"agent.yaml":         "name: researcher\nkind: prompt\ninstructions: Cite the indexed literature.\n",
		".azure/config.json": `{"defaultEnvironment":"demo"}`,
		".azure/demo/.env":   "AZURE_ENV_NAME=\"demo\"\n",
	} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("azd", "package", "literature", "--cwd", root, "--environment", "demo", "--no-prompt")
	command.Env = append(os.Environ(), "AZD_CONTAINER_RUNTIME=unavailable-runtime", "AZD_COLLECT_TELEMETRY=no")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("KB packaging required provisioned outputs or container/Azure access: %v\n%s", err, output)
	}
	command = exec.Command("azd", "package", "researcher", "--cwd", root, "--environment", "demo", "--no-prompt")
	command.Env = append(os.Environ(), "AZD_CONTAINER_RUNTIME=unavailable-runtime", "AZD_COLLECT_TELEMETRY=no")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("KB-dependent agent packaging required cloud outputs or readback: %v\n%s", err, output)
	}
	scope := strings.TrimSuffix(nativeWorkspace, "/workspaces/demo")
	values := "AZURE_ENV_NAME=\"demo\"\nAZURE_SUBSCRIPTION_ID=\"00000000-0000-0000-0000-000000000001\"\n" +
		"AZURE_RESOURCE_GROUP=\"demo\"\nWORKSPACE=\"" + nativeWorkspace + "\"\nPROJECT=\"" + nativeWorkspace +
		"/projects/research\"\nBOOKSHELF=\"literature\"\nASSET=\"" + scope + "/storageContainers/data/storageAssets/papers\"\nMODEL_NAME=\"chat\"\n"
	if err := os.WriteFile(filepath.Join(root, ".azure/demo/.env"), []byte(values), 0600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("azd", "discovery", "plan", "--cwd", root, "--environment", "demo", "--output", "json", "--no-prompt")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("native KB metadata/config did not validate: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var report struct {
		Status   string `json:"status"`
		Services []struct {
			Host   string `json:"host"`
			Intent string `json:"intent"`
		} `json:"services"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Status != "valid" ||
		len(report.Services) != 2 || report.Services[0].Host != "discovery.knowledgebase" ||
		report.Services[0].Intent != "reconcileAndIndexKnowledgeBase" || report.Services[1].Host != "discovery.agent" {
		t.Fatalf("unexpected native KB plan: %s (%v)", stdout.String(), err)
	}
}
