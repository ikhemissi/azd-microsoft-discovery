// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"github.com/ikhemissi/azd-microsoft-discovery/schemas"
	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"
)

func newMetadataCommand(rootCmd *cobra.Command) *cobra.Command {
	command := azdext.NewMetadataCommand("1.0", "ikhemissi.azd.discovery", func() *cobra.Command {
		return rootCmd
	})
	command.Args = cobra.NoArgs
	command.RunE = func(command *cobra.Command, _ []string) error {
		generated := azdext.GenerateExtensionMetadata("1.0", "ikhemissi.azd.discovery", rootCmd)
		project, err := metadataSchema("project")
		if err != nil {
			return err
		}
		service, err := metadataSchema("service")
		if err != nil {
			return err
		}
		generated.Configuration = &extensions.ConfigurationMetadata{Project: project, Service: service}
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(generated)
	}
	return command
}

func metadataSchema(name string) (*jsonschema.Schema, error) {
	document, err := schemas.Entry(name)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}
