package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/data"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/project"
	"github.com/spf13/cobra"
)

type dataDependencies struct {
	load    func(context.Context, string, bool) (*config.Project, string, error)
	connect func(context.Context, string, *config.Project, string) (data.Destination, data.BlobStore, error)
}

func loadDataProject(ctx context.Context, environment string, local bool) (*config.Project, string, error) {
	if local {
		root, err := azdext.GetProjectDir()
		if err != nil {
			return nil, "", err
		}
		document, err := config.ReadDocument(filepath.Join(root, "azure.yaml"))
		if err != nil {
			return nil, "", err
		}
		parsed, err := config.ParseForPackage(root, document, os.LookupEnv)
		return parsed, "", err
	}
	client, err := azdext.NewAzdClient()
	if err != nil {
		return nil, "", errors.New("connect to azd, or use data validate --local for process-environment-only validation")
	}
	defer client.Close()
	selected, err := project.ResolveEnvironment(ctx, client, environment)
	if err != nil {
		return nil, "", err
	}
	parsed, err := config.Load(ctx, client, selected, true)
	return parsed, selected, err
}

func connectData(ctx context.Context, environment string, parsed *config.Project, dataset string) (data.Destination, data.BlobStore, error) {
	client, err := azdext.NewAzdClient()
	if err != nil {
		return data.Destination{}, nil, errors.New("data upload requires an azd connection")
	}
	defer client.Close()
	return data.Connect(ctx, client, environment, parsed, dataset)
}

func newDataCommand(extension *azdext.ExtensionContext, dependencies dataDependencies) *cobra.Command {
	group := &cobra.Command{Use: "data", Short: "Prepare, validate, upload and locate reusable experiment bundles"}
	for _, action := range []string{"prepare", "validate", "upload"} {
		var local, dryRun, confirmed bool
		command := &cobra.Command{
			Use: action + " <dataset>", Args: cobra.ExactArgs(1),
			Short: "Validate a local declared bundle and its SHA-256 checksums",
			RunE: func(command *cobra.Command, args []string) (err error) {
				if local && command.Flags().Changed("environment") {
					return errors.New("--local does not load azd environments; omit --environment")
				}
				if action == "upload" && !dryRun && !confirmed {
					return errors.New("upload writes require explicit --confirm; inspect --dry-run first")
				}
				parsed, environment, err := dependencies.load(command.Context(), extension.Environment, local)
				if err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(command.Context(), time.Duration(parsed.Discovery.OperationTimeoutSeconds)*time.Second)
				defer cancel()
				var bundle *data.Bundle
				if action == "prepare" {
					bundle, err = data.Prepare(ctx, parsed, args[0])
				} else {
					bundle, err = data.Validate(ctx, parsed, args[0])
				}
				if err != nil {
					return err
				}
				defer func() { err = errors.Join(err, bundle.Root.Close()) }()
				result := data.Report(bundle, data.Destination{}, "validated")
				if action == "prepare" {
					result.Status = "prepared"
				}
				if action == "upload" {
					destination, remote, err := dependencies.connect(ctx, environment, parsed, args[0])
					if err != nil {
						return err
					}
					result, err = data.Upload(ctx, parsed, environment, bundle, destination, remote, dryRun)
					if err != nil {
						return err
					}
				}
				if extension.OutputFormat == "json" {
					return json.NewEncoder(command.OutOrStdout()).Encode(result)
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "Dataset %s: %s; %d files, %d bytes; content SHA-256 %s\n",
					result.Dataset, result.Status, result.Files, result.Bytes, result.ContentSHA256); err != nil {
					return err
				}
				if result.BlobPrefixURI != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "Blob prefix: %s\n", result.BlobPrefixURI); err != nil {
						return err
					}
					if result.AssetVerified {
						if _, err := fmt.Fprintf(command.OutOrStdout(), "Verified IaC asset: %s\nDiscovery resource URI: %s\n", result.StorageAssetResourceID, result.ResourceURI); err != nil {
							return err
						}
					}
					_, err = fmt.Fprintln(command.OutOrStdout(), "Attach the Discovery asset to the intended conversation/task separately; conversationAttached=false.")
				}
				return err
			},
		}
		if action != "upload" {
			command.Flags().BoolVar(&local, "local", false, "Use only local configuration/process variables; no azd or Azure access")
			if action == "prepare" {
				command.Short = "Create the local checksum manifest without overwrite or execution"
			}
		} else {
			command.Short = "Upload a verified bundle without overwrite, asset creation or conversation attachment"
			command.Flags().BoolVar(&dryRun, "dry-run", false, "Verify local inputs and read destination prerequisites without writes or recovery state")
			command.Flags().BoolVar(&confirmed, "confirm", false, "Explicitly authorize Blob writes to the declared immutable destination")
		}
		group.AddCommand(azdext.RegisterFlagOptions(command, azdext.FlagOptions{Name: "output", AllowedValues: []string{"json", "default"}}))
	}
	group.AddCommand(newDataShowCommand(extension, dependencies))
	return group
}

func newDataShowCommand(extension *azdext.ExtensionContext, dependencies dataDependencies) *cobra.Command {
	var agent string
	command := &cobra.Command{
		Use: "show <dataset>", Args: cobra.ExactArgs(1),
		Short: "Verify an IaC-owned experiment asset and show its runtime resource locator",
		Long:  "Read project/storage/asset prerequisites and return an asset ID and directory resource URI. Does not require local files, create resources, attach conversations, invoke agents, verify payload integrity or write recovery state.",
		RunE: func(command *cobra.Command, args []string) error {
			parsed, environment, err := dependencies.load(command.Context(), extension.Environment, false)
			if err != nil {
				return err
			}
			if parsed.Discovery.Data == nil || parsed.Discovery.Data.Datasets[args[0]].StorageAssetResourceID == "" {
				return fmt.Errorf("data show requires a declared dataset with an IaC-owned storageAssetResourceId")
			}
			if agent != "" {
				if _, err := data.AgentTools(parsed, agent); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(command.Context(), time.Duration(parsed.Discovery.OperationTimeoutSeconds)*time.Second)
			defer cancel()
			destination, _, err := dependencies.connect(ctx, environment, parsed, args[0])
			if err != nil {
				return err
			}
			result, err := data.Describe(parsed, args[0], agent, destination)
			if err != nil {
				return err
			}
			if extension.OutputFormat == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "Dataset %s: IaC asset verified\nAsset ID: %s\nResource URI: %s\nBlob prefix: %s\n",
				result.Dataset, result.StorageAssetResourceID, result.ResourceURI, result.BlobPrefixURI); err != nil {
				return err
			}
			if result.AgentService != "" {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "Configured agent: %s (%s); tool services: %v\n",
					result.AgentService, result.AgentName, result.ToolServices); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), result.NextStep)
			return err
		},
	}
	command.Flags().StringVar(&agent, "agent", "", "Show the configured agent/tool association for manual conversation use; does not invoke or bind a dataset to that agent")
	return azdext.RegisterFlagOptions(command, azdext.FlagOptions{Name: "output", AllowedValues: []string{"json", "default"}})
}
