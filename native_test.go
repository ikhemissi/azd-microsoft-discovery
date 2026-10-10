package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const nativeWorkspace = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery/workspaces/demo"

const nativeAgentConfiguration = `name: native-readonly
discovery:
  schemaVersion: "1.0"
  workspaceResourceId: ${WORKSPACE_ID}
  projectResourceId: ${PROJECT_ID}
services:
  researcher:
    host: discovery.agent
    config:
      model:
        deploymentName: ${MODEL_NAME}
`

const nativeAgentDefinition = "name: researcher\nkind: prompt\ninstructions: Preserve {{runtime}} and ${LITERAL}.\n"

func TestNativeReadOnlyCommands(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_NATIVE_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_NATIVE_TESTS=1 with this build installed in an isolated AZD_CONFIG_DIR")
	}
	if os.Getenv("AZD_CONFIG_DIR") == "" {
		t.Fatal("native tests require an isolated AZD_CONFIG_DIR")
	}
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("azure.yaml", nativeAgentConfiguration)
	write("agent.yaml", nativeAgentDefinition)
	write(".azure/config.json", `{"defaultEnvironment":"demo"}`)
	write(".azure/demo/.env", "AZURE_ENV_NAME=\"demo\"\nWORKSPACE_ID=\""+nativeWorkspace+
		"\"\nPROJECT_ID=\""+nativeWorkspace+"/projects/research\"\nMODEL_NAME=\"chat\"\n")
	// azd 1.35.0 maintains this advisory lock even when only reading values.
	write(".azure/demo/.env.lock", "")
	before := snapshot(t, root)
	for _, command := range []string{"plan", "doctor"} {
		process := exec.Command("azd", "discovery", command, "--cwd", root, "--output", "json", "--no-prompt")
		if command == "plan" {
			process.Args = append(process.Args, "--environment", "demo")
		}
		process.Env = append(os.Environ(), "AZD_COLLECT_TELEMETRY=no")
		var stdout, stderr bytes.Buffer
		process.Stdout, process.Stderr = &stdout, &stderr
		if err := process.Run(); err != nil {
			t.Fatalf("native %s failed: %v\n%s\n%s", command, err, stdout.String(), stderr.String())
		}
		var result struct {
			Status              string `json:"status"`
			Source              string `json:"source"`
			DeploymentSupported bool   `json:"deploymentSupported"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("native JSON stdout is contaminated: %v\n%s", err, stdout.String())
		}
		if result.Status != "valid" || result.Source != "azd" || !result.DeploymentSupported {
			t.Fatalf("unexpected native report: %+v", result)
		}
	}
	after := snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		for path, content := range after {
			if previous, exists := before[path]; !exists || previous != content {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("created/changed fixture path: %s", relative)
			}
		}
		t.Fatal("read-only commands mutated the fixture project/environment")
	}
	write(".azure/demo/.env", "WORKSPACE_ID=\""+nativeWorkspace+"\"\n")
	before = snapshot(t, root)
	process := exec.Command("azd", "discovery", "doctor", "--cwd", root, "--environment", "demo", "--output", "json", "--no-prompt")
	process.Env = append(os.Environ(), "AZD_COLLECT_TELEMETRY=no")
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	if err := process.Run(); err == nil {
		t.Fatal("incomplete environment must not produce successful validation")
	}

	var failure struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(&stdout)
	if err := decoder.Decode(&failure); err != nil || failure.Status != "blocked" {
		t.Fatalf("malformed environment did not produce a blocker report: %v", err)
	}
	trailing, err := io.ReadAll(io.MultiReader(decoder.Buffered(), &stdout))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(trailing)) != "" {
		expected := "ERROR: Discovery configuration is blocked; see the report for details"
		if strings.TrimSpace(string(trailing)) != expected {
			t.Fatalf("unexpected trailing native stdout: %s", trailing)
		}
	}
	after = snapshot(t, root)
	envPath := filepath.Join(root, ".azure", "demo", ".env")
	if after[envPath] != before[envPath] {
		repaired := strings.ReplaceAll(after[envPath], "AZURE_ENV_NAME=\"demo\"\n", "")
		if repaired != before[envPath] {
			t.Fatalf("unexpected environment repair: %s", after[envPath])
		}
		after[envPath] = before[envPath]
	}
	configPath := filepath.Join(root, ".azure", "demo", "config.json")
	if _, exists := before[configPath]; !exists && after[configPath] == "{}" {
		// azd 1.35.0 materializes an empty environment config when saving
		// its repaired AZURE_ENV_NAME. No other content or files are allowed.
		delete(after, configPath)
	}
	if !reflect.DeepEqual(before, after) {
		for path, content := range after {
			if original, exists := before[path]; !exists || content != original {
				t.Logf("changed fixture path %s: before=%q after=%q", path, original, content)
			}
		}
		t.Fatal("native failure mutated files outside the documented environment metadata repair")
	}
}

func TestStandaloneReadOnlyCommands(t *testing.T) {
	if os.Getenv("AZD_DISCOVERY_NATIVE_TESTS") != "1" {
		t.Skip("set AZD_DISCOVERY_NATIVE_TESTS=1 to build and exercise the standalone executable")
	}
	binary := filepath.Join(t.TempDir(), "discovery")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build standalone extension: %v\n%s", err, output)
	}
	root := t.TempDir()
	t.Setenv("AZD_EXEC_PROJECT_DIR", root)
	t.Setenv("AZD_SERVER", "")
	content := strings.NewReplacer(
		"${WORKSPACE_ID}", nativeWorkspace,
		"${PROJECT_ID}", nativeWorkspace+"/projects/research",
		"${MODEL_NAME}", "chat",
	).Replace(nativeAgentConfiguration)
	path := filepath.Join(root, "azure.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte(nativeAgentDefinition), 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		command string
		blocked bool
	}{{"plan", false}, {"doctor", false}, {"doctor", true}} {
		if scenario.blocked {
			if err := os.WriteFile(path, []byte("name: blocked\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		before := snapshot(t, root)
		process := exec.Command(binary, scenario.command, "--local", "--cwd", root, "--output", "json")
		var stdout, stderr bytes.Buffer
		process.Stdout, process.Stderr = &stdout, &stderr
		err := process.Run()
		if (err != nil) != scenario.blocked {
			t.Fatalf("unexpected standalone exit: %v\n%s", err, stderr.String())
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("standalone JSON is contaminated: %v\n%s", err, stdout.String())
		}
		expected := "valid"
		if scenario.blocked {
			expected = "blocked"
		}
		if result.Status != expected {
			t.Fatalf("expected status %s, got %s", expected, result.Status)
		}
		if !reflect.DeepEqual(before, snapshot(t, root)) {
			t.Fatal("standalone inspection modified the project")
		}
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			files[path] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
