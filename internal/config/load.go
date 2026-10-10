package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
)

// Load re-reads raw Project RPC configuration and current environment at each stage.
func Load(ctx context.Context, client *azdext.AzdClient, environment string, packageOnly bool) (*Project, error) {
	root, err := azdext.GetProjectDir()
	if err != nil {
		return nil, err
	}
	document, err := ReadDocument(filepath.Join(root, "azure.yaml"))
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("azd client is required")
	}
	for _, section := range []string{"discovery", "services"} {
		response, err := client.Project().GetConfigSection(ctx, &azdext.GetProjectConfigSectionRequest{Path: section})
		if err != nil {
			return nil, fmt.Errorf("read %s configuration through azd: %w", section, err)
		}
		if response == nil || !response.Found || response.Section == nil {
			return nil, fmt.Errorf("azure.yaml is missing the %s configuration section", section)
		}
		document[section] = response.Section.AsMap()
	}
	values := map[string]string{}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("configuration contains unsupported values")
	}
	if environment != "" || strings.Contains(string(data), "${") {
		response, err := client.Environment().GetValues(ctx, &azdext.GetEnvironmentRequest{Name: environment})
		if err != nil {
			return nil, fmt.Errorf("read azd environment: select an existing environment: %w", err)
		}
		if response == nil {
			return nil, errors.New("azd returned no environment values")
		}
		for _, pair := range response.KeyValues {
			if pair != nil {
				values[pair.Key] = pair.Value
			}
		}
	}
	lookup := func(name string) (string, bool) {
		if value, ok := values[name]; ok {
			return value, true
		}
		return os.LookupEnv(name)
	}
	if packageOnly {
		return ParseForPackage(root, document, lookup)
	}
	return Parse(root, document, lookup)
}
