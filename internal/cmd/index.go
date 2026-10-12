package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/project"
	"github.com/spf13/cobra"
)

type indexRunner func(context.Context, string, string, azdext.ProgressReporter) (*azdext.ServiceDeployResult, error)

func indexService(ctx context.Context, environment, service string, progress azdext.ProgressReporter) (*azdext.ServiceDeployResult, error) {
	client, err := azdext.NewAzdClient()
	if err != nil {
		return nil, fmt.Errorf("connect to azd: run indexing through azd discovery index: %w", err)
	}
	defer client.Close()
	return project.NewWithClient(client, environment).Index(ctx, service, progress)
}

func newIndexCommand(extension *azdext.ExtensionContext, run indexRunner) *cobra.Command {
	command := &cobra.Command{
		Use:   "index <service>",
		Short: "Index an existing Discovery knowledge-base service",
		Long:  "Verify the existing KB definition and registered sources, then start or resume indexing. Does not create/update KBs, upload files, grant roles, or choose a node pool. Indexing can incur Azure costs.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			progress := func(message string) {
				command.PrintErrln(message)
			}
			result, err := run(command.Context(), extension.Environment, args[0], progress)
			if err != nil {
				return err
			}
			if result == nil || len(result.Artifacts) != 1 || result.Artifacts[0] == nil ||
				result.Artifacts[0].Metadata["indexingStatus"] != "Succeeded" || result.Artifacts[0].Metadata["runId"] == "" {
				return fmt.Errorf("indexing returned no verified successful run")
			}
			outputs := result.Artifacts[0].Metadata
			if extension.OutputFormat == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(outputs)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Knowledge base %s indexed successfully (run %s).\n", outputs["name"], outputs["runId"])
			return err
		},
	}
	return azdext.RegisterFlagOptions(command, azdext.FlagOptions{
		Name: "output", AllowedValues: []string{"json", "default"},
	})
}
