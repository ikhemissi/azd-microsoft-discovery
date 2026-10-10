package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func loadProject(ctx context.Context, environment string, local bool) (*config.Project, error) {
	root, err := azdext.GetProjectDir()
	if err != nil {
		return nil, fmt.Errorf("locate azure.yaml: %w", err)
	}
	document, err := config.ReadDocument(filepath.Join(root, "azure.yaml"))
	if err != nil {
		return nil, err
	}
	if local {
		return config.Parse(root, document, os.LookupEnv)
	}
	client, err := azdext.NewAzdClient()
	if err != nil {
		return nil, fmt.Errorf("connect to azd: run through azd discovery, or use --local for process-environment-only validation: %w", err)
	}
	defer client.Close()
	return config.Load(ctx, client, environment, false)
}
