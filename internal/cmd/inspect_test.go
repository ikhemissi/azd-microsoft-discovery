package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func TestReadOnlyReports(t *testing.T) {
	for _, command := range []string{"plan", "doctor"} {
		t.Run(command, func(t *testing.T) {
			root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
			project := &config.Project{Name: "example", Services: map[string]*config.Service{
				"agent": {Name: "agent", Host: config.AgentHost, Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "chat"}}},
			}}
			var calls int
			root.AddCommand(newInspectCommand(command, extension, func(ctx context.Context, environment string, local bool) (*config.Project, error) {
				calls++
				if !local {
					t.Fatal("local selection not propagated")
				}
				return project, nil
			}))
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{command, "--local", "--output", "json"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var result report
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Status != "valid" || !result.DeploymentSupported ||
				result.ValidationScope != "local" || result.Checks[1].State != "notChecked" ||
				result.Services[0].Action != "unresolved" {
				t.Fatalf("misleading report: %+v", result)
			}
			if !strings.Contains(stderr.String(), "local validation only") {
				t.Fatal("missing readiness warning")
			}
		})
	}
}

func TestBlockerProducesJSON(t *testing.T) {
	root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
	root.SilenceErrors, root.SilenceUsage = true, true
	root.AddCommand(newInspectCommand("doctor", extension, func(context.Context, string, bool) (*config.Project, error) {
		return nil, errors.New("configuration blocker")
	}))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"doctor", "--output", "json"})
	if err := root.Execute(); err == nil {
		t.Fatal("blocker returned success")
	}
	var result report
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Status != "blocked" {
		t.Fatalf("invalid failure report: %s (%v)", output.String(), err)
	}
}

func TestInspectionRejectsUnsupportedInputBeforeLoad(t *testing.T) {
	for _, args := range [][]string{
		{"plan", "unexpected"}, {"plan", "--output", "yaml"},
		{"plan", "--local", "--environment", "demo"},
	} {
		root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
		root.SilenceErrors, root.SilenceUsage = true, true
		root.AddCommand(newInspectCommand("plan", extension, func(context.Context, string, bool) (*config.Project, error) {
			t.Fatal("invalid invocation reached project loader")
			return nil, nil
		}))
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("invalid invocation accepted: %v", args)
		}
	}
}

func TestMetadataContainsSelfContainedSchemas(t *testing.T) {
	root := NewRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"metadata"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(output.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	configuration, ok := metadata["configuration"].(map[string]any)
	if !ok {
		t.Fatal("missing configuration metadata")
	}
	for _, kind := range []string{"project", "service"} {
		schema := configuration[kind].(map[string]any)
		if schema["$defs"] == nil || schema["$ref"] != "#/$defs/"+kind {
			t.Fatalf("schema %s is not self contained", kind)
		}
	}
	if strings.Contains(output.String(), `"prompt"`) || strings.Contains(output.String(), `"context"`) {
		t.Fatal("scaffold example commands leaked into public metadata")
	}
}

func TestLocalLoadDoesNotWriteState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AZD_EXEC_PROJECT_DIR", root)
	const workspace = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery/workspaces/demo"
	content := "name: demo\ndiscovery:\n  schemaVersion: \"1.0\"\n  workspaceResourceId: " + workspace +
		"\n  projectResourceId: " + workspace + "/projects/research\nservices:\n  agent:\n    host: discovery.agent\n    config:\n      model:\n        deploymentName: chat\n"
	if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("name: researcher\nkind: prompt\ninstructions: Keep unchanged.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProject(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != "agent.yaml" || entries[1].Name() != "azure.yaml" {
		t.Fatalf("unexpected project mutations: %v (%v)", entries, err)
	}
}
