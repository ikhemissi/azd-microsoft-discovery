// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/project"
	"github.com/spf13/cobra"
)

func newListenCommand() *cobra.Command {
	return azdext.NewListenCommand(project.Register)
}
