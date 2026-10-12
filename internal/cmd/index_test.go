package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
)

func TestIndexCommandEnvironmentOutputAndFailures(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "unverified", "invalid args", "invalid output"} {
		t.Run(scenario, func(t *testing.T) {
			root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
			root.SilenceErrors, root.SilenceUsage = true, true
			calls := 0
			root.AddCommand(newIndexCommand(extension, func(ctx context.Context, env, service string, progress azdext.ProgressReporter) (*azdext.ServiceDeployResult, error) {
				calls++
				if env != "selected" || service != "literature" {
					t.Fatal("CLI selection was not passed to indexing")
				}
				progress("Indexing progress")
				if scenario == "failure" {
					return nil, errors.New("explicit indexing failure")
				}
				if scenario == "unverified" {
					return &azdext.ServiceDeployResult{}, nil
				}
				return &azdext.ServiceDeployResult{Artifacts: []*azdext.Artifact{{
					Metadata: map[string]string{"name": "papers", "indexingStatus": "Succeeded", "runId": "run-1"},
				}}}, nil
			}))
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			args := []string{"index", "literature", "--environment", "selected", "--output", "json"}
			if scenario == "invalid args" {
				args = append(args, "extra")
			}
			if scenario == "invalid output" {
				args[len(args)-1] = "yaml"
			}
			root.SetArgs(args)
			err := root.Execute()
			if scenario == "success" {
				var outputs map[string]string
				if err != nil || json.Unmarshal(stdout.Bytes(), &outputs) != nil ||
					outputs["runId"] != "run-1" || stderr.String() != "Indexing progress\n" {
					t.Fatalf("invalid successful output: %s (%v)", stdout.String(), err)
				}
			} else if err == nil || stdout.Len() != 0 {
				t.Fatal("indexing failure looked successful")
			}
			if (scenario == "invalid args" || scenario == "invalid output") && calls != 0 {
				t.Fatal("invalid command reached indexing")
			}
		})
	}
}
