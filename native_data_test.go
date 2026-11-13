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

func TestNativeDataValidation(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_NATIVE_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_NATIVE_TESTS=1 with a development extension installed")
	}
	if os.Getenv("AZD_CONFIG_DIR") == "" {
		t.Fatal("native tests require isolated AZD_CONFIG_DIR")
	}
	root := t.TempDir()
	source := filepath.Join("examples", "reusable-data")
	for _, name := range []string{"azure.yaml", "bundles/experiment-a/input.txt", "bundles/experiment-a/bundle.yaml",
		"bundles/experiment-b/input.txt", "bundles/experiment-b/bundle.yaml"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	toolDir := filepath.Join(root, "experiments", "lysozyme-water")
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "tool.cpu.yaml"), []byte("name: reusable-tool\nversion: 1.0.0\ninfra:\n  - name: worker\n    image: {acr: original}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "agent.yaml"), []byte(
		"name: researcher\nkind: prompt\ninstructions: Use the attached experiment with explicit runtime mounts.\n"+
			"discoveryExtensions:\n  tools:\n    - toolId: '{{gromacsToolId}}'\n      confirmation: Enabled\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"experiment-a", "experiment-b"} {
		command := exec.Command("azd", "discovery", "data", "validate", name, "--local", "--cwd", root, "--output", "json", "--no-prompt")
		command.Env = append(os.Environ(), "AZD_COLLECT_TELEMETRY=no")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("native data validation failed: %v\n%s\n%s", err, stdout.String(), stderr.String())
		}
		var result struct {
			Status               string `json:"status"`
			BlobVerified         bool   `json:"blobVerified"`
			ConversationAttached bool   `json:"conversationAttached"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Status != "validated" || result.BlobVerified || result.ConversationAttached {
			t.Fatalf("native data result is misleading: %s (%v)", stdout.String(), err)
		}
	}
	if err := os.Remove(filepath.Join(root, "bundles", "experiment-a", "bundle.yaml")); err != nil {
		t.Fatal(err)
	}
	prepare := exec.Command("azd", "discovery", "data", "prepare", "experiment-a", "--local", "--cwd", root, "--output", "json", "--no-prompt")
	prepare.Env = append(os.Environ(), "AZD_COLLECT_TELEMETRY=no")
	var prepared bytes.Buffer
	prepare.Stdout = &prepared
	if err := prepare.Run(); err != nil {
		t.Fatalf("native preparation failed: %v\n%s", err, prepared.String())
	}
	var preparation struct {
		Status        string `json:"status"`
		ContentSHA256 string `json:"contentSha256"`
	}
	if err := json.Unmarshal(prepared.Bytes(), &preparation); err != nil || preparation.Status != "prepared" || preparation.ContentSHA256 == "" {
		t.Fatalf("native preparation did not generate verified checksums: %s (%v)", prepared.String(), err)
	}
	if _, err := os.Stat(filepath.Join(root, ".azure")); !os.IsNotExist(err) {
		t.Fatal("local validation wrote environment/recovery state")
	}
	command := exec.Command("azd", "discovery", "data", "upload", "experiment-a", "--cwd", root, "--no-prompt")
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "explicit --confirm") {
		t.Fatalf("native unapproved upload was not blocked: %v\n%s", err, output)
	}
}
