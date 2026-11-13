package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/data"
)

func TestDataShowIsReadOnlyAndDoesNotNeedLocalPayloads(t *testing.T) {
	for _, scenario := range []string{"valid", "no asset", "unknown agent", "tool as agent", "connect error"} {
		t.Run(scenario, func(t *testing.T) {
			root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
			root.SilenceErrors, root.SilenceUsage = true, true
			directory := t.TempDir()
			project := &config.Project{Root: directory, Discovery: config.Settings{OperationTimeoutSeconds: 5, Data: &config.Data{
				Datasets: map[string]config.Dataset{"example": {Directory: "not-local", StorageAssetResourceID: "/verified/asset"}},
			}}, Services: map[string]*config.Service{
				"researcher": {Host: config.AgentHost, Agent: &config.Agent{Bindings: map[string]config.Binding{"tool": {Service: "simulation", Output: "resourceId"}}},
					Definition: map[string]any{"name": "research-agent"}},
				"simulation": {Host: config.ToolHost},
			}}
			if scenario == "no asset" {
				project.Discovery.Data.Datasets["example"] = config.Dataset{}
			}
			connects := 0
			root.AddCommand(newDataCommand(extension, dataDependencies{
				load: func(context.Context, string, bool) (*config.Project, string, error) { return project, "selected", nil },
				connect: func(ctx context.Context, environment string, p *config.Project, name string) (data.Destination, data.BlobStore, error) {
					connects++
					if environment != "selected" || name != "example" {
						t.Fatal("handoff lost effective environment/dataset selection")
					}
					if scenario == "connect error" {
						return data.Destination{}, nil, context.DeadlineExceeded
					}
					return data.Destination{StorageAssetID: "/verified/asset",
						ResourceURI: "discovery://resources/inputs/paths/inputs/experiment/", URI: "https://example.blob.core.windows.net/inputs/experiment/"}, nil, nil
				},
			}))
			agent := "researcher"
			if scenario == "unknown agent" {
				agent = "missing"
			}
			if scenario == "tool as agent" {
				agent = "simulation"
			}
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetArgs([]string{"data", "show", "example", "--agent", agent, "--output", "json"})
			err := root.Execute()
			if scenario == "valid" {
				var result data.Handoff
				if err != nil || json.Unmarshal(output.Bytes(), &result) != nil || !result.AssetVerified || result.BlobVerified ||
					result.ConversationAttached || result.AgentName != "research-agent" ||
					len(result.ToolServices) != 1 || result.ToolServices[0] != "simulation" || result.ResourceURI == "" {
					t.Fatalf("handoff was not truthful or lacked runtime references: %s (%v)", output.String(), err)
				}
			} else if err == nil || output.Len() != 0 {
				t.Fatal("invalid handoff looked successful")
			}
			if (scenario == "no asset" || scenario == "unknown agent" || scenario == "tool as agent") && connects != 0 {
				t.Fatal("invalid selection reached cloud access")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("data show wrote local payloads or recovery state")
			}
		})
	}
}

func TestDataPrepareLocalCreatesOnlyManifest(t *testing.T) {
	root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "md.mdp"), []byte("reviewed settings fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root.AddCommand(newDataCommand(extension, dataDependencies{
		load: func(ctx context.Context, environment string, local bool) (*config.Project, string, error) {
			if !local {
				t.Fatal("preparation did not stay local")
			}
			return &config.Project{Root: directory, Discovery: config.Settings{OperationTimeoutSeconds: 5, Data: &config.Data{
				Datasets: map[string]config.Dataset{"example": {Directory: ".", Manifest: "bundle.yaml"}},
			}}}, "", nil
		},
		connect: func(context.Context, string, *config.Project, string) (data.Destination, data.BlobStore, error) {
			t.Fatal("preparation connected to Azure")
			return data.Destination{}, nil, nil
		},
	}))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"data", "prepare", "example", "--local", "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result data.Result
	if json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "prepared" || result.ContentSHA256 == "" ||
		result.AssetVerified || result.BlobVerified || result.ConversationAttached {
		t.Fatal("preparation reported a cloud mutation or omitted computed hashes")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatal("preparation wrote more than its manifest")
	}
	content, err := os.ReadFile(filepath.Join(directory, "bundle.yaml"))
	if err != nil || !strings.Contains(string(content), `"path": "md.mdp"`) {
		t.Fatal("preparation did not include runtime settings")
	}
}
