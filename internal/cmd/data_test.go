package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/data"
)

func TestDataCLIValidationAndAuthorizationBeforeCloudAccess(t *testing.T) {
	for _, scenario := range []string{"local", "missing confirmation", "invalid arguments", "local environment"} {
		t.Run(scenario, func(t *testing.T) {
			root, extension := azdext.NewExtensionRootCommand(azdext.ExtensionCommandOptions{Name: "discovery"})
			root.SilenceErrors, root.SilenceUsage = true, true
			directory := t.TempDir()
			payload := []byte("trusted inputs")
			sum := sha256.Sum256(payload)
			manifest, err := json.Marshal(data.Manifest{SchemaVersion: "1.0", Files: []data.Entry{{Path: "data.txt", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}}})
			if err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string][]byte{"data.txt": payload, "bundle.yaml": manifest} {
				if err := os.WriteFile(filepath.Join(directory, name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			loads := 0
			root.AddCommand(newDataCommand(extension, dataDependencies{
				load: func(ctx context.Context, env string, local bool) (*config.Project, string, error) {
					loads++
					if !local {
						t.Fatal("invalid command reached loader")
					}
					return &config.Project{Root: directory, Discovery: config.Settings{OperationTimeoutSeconds: 10, Data: &config.Data{
						Datasets: map[string]config.Dataset{"example": {Directory: ".", Manifest: "bundle.yaml"}},
					}}}, "", nil
				},
				connect: func(context.Context, string, *config.Project, string) (data.Destination, data.BlobStore, error) {
					t.Fatal("local validation connected to Azure")
					return data.Destination{}, nil, nil
				},
			}))
			args := []string{"data", "validate", "example", "--local", "--output", "json"}
			switch scenario {
			case "missing confirmation":
				args = []string{"data", "upload", "example", "--no-prompt"}
			case "invalid arguments":
				args = append(args, "unexpected")
			case "local environment":
				args = append(args, "--environment", "wrong")
			}
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetArgs(args)
			err = root.Execute()
			if scenario == "local" {
				var result data.Result
				if err != nil || json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "validated" ||
					result.BlobVerified || result.ConversationAttached {
					t.Fatalf("misleading validation result: %s (%v)", output.String(), err)
				}
			} else if err == nil || loads != 0 {
				t.Fatal("invalid/unapproved command reached work")
			}
		})
	}
}
