// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	rootCmd, extCtx := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{
		Name:    "discovery",
		Use:     "discovery <command> [options]",
		Version: Version,
		Short:   "Build and deploy Microsoft Discovery tools, agents, and knowledge bases",
		Long:    "Community extension for Microsoft Discovery with native tool/agent/KB deployment, explicit indexing, and local plan/doctor checks. Agent KB attachment and live Azure readiness diagnostics are not implemented.",
	})

	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.CompletionOptions = cobra.CompletionOptions{
		DisableDefaultCmd: true,
	}

	rootCmd.SetHelpCommand(&cobra.Command{Hidden: true})

	rootCmd.AddCommand(newListenCommand())
	rootCmd.AddCommand(newInspectCommand("plan", extCtx, loadProject))
	rootCmd.AddCommand(newInspectCommand("doctor", extCtx, loadProject))
	rootCmd.AddCommand(newIndexCommand(extCtx, indexService))
	rootCmd.AddCommand(newVersionCommand(&extCtx.OutputFormat))
	rootCmd.AddCommand(newMetadataCommand(rootCmd))

	return rootCmd
}
