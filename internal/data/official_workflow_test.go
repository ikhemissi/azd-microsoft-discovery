package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
)

const assetID = registrationID + "/storageAssets/experiment-v1"

func TestIaCAssetReadbackMapsExactlyToDatasetDirectory(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign parent", "wrong path", "file asset", "wrong name", "missing", "denied", "not ready", "traversal"} {
		t.Run(scenario, func(t *testing.T) {
			dataset := config.Dataset{Source: "inputs", Namespace: "experiment", Version: "v1", StorageAssetResourceID: assetID}
			if scenario == "foreign parent" {
				dataset.StorageAssetResourceID = armScope + "Microsoft.Discovery/storageContainers/foreign/storageAssets/experiment-v1"
			}
			project := &config.Project{Discovery: config.Settings{ProjectResourceID: projectID, Data: &config.Data{
				Sources: map[string]config.DataSource{"inputs": {
					StorageContainerResourceID: registrationID, BlobContainerResourceID: blobID, Prefix: "approved",
				}},
				Datasets: map[string]config.Dataset{"example": dataset},
			}}}
			client := discovery.NewClient(fakeCredential{})
			requests := []string{}
			client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					t.Fatal("IaC-owned asset was mutated")
				}
				requests = append(requests, request.URL.Path)
				resource := &sourceResource{ID: request.URL.Path}
				resource.Properties.ProvisioningState = "Succeeded"
				status := 200
				switch request.URL.Path {
				case projectID:
					resource.Properties.StorageContainerIDs = []string{registrationID}
				case registrationID:
					resource.Name = "inputs"
					resource.Properties.StorageStore.Kind = "AzureStorageBlob"
					resource.Properties.StorageStore.StorageAccountID = accountID
				case accountID:
					resource.Properties.PrimaryEndpoints.Blob = "https://demostore.blob.core.windows.net/"
				case assetID:
					if request.URL.Query().Get("api-version") != discovery.ARMVersion {
						t.Fatal("asset read did not use the GA API")
					}
					resource.Name = "experiment-v1"
					resource.Properties.Path = "inputs/approved/experiment/v1/"
					switch scenario {
					case "wrong path":
						resource.Properties.Path = "other/approved/experiment/v1/"
					case "file asset":
						resource.Properties.Path = strings.TrimSuffix(resource.Properties.Path, "/")
					case "wrong name":
						resource.Name = "different"
					case "missing":
						status = 404
					case "denied":
						status = 403
					case "not ready":
						resource.Properties.ProvisioningState = "Creating"
					case "traversal":
						resource.Properties.Path = "inputs/approved/../experiment/v1/"
					}
				default:
					t.Fatalf("unexpected asset prerequisite request: %s", request.URL.Path)
				}
				encoded, err := json.Marshal(resource)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: request}, nil
			})
			destination, err := Resolve(t.Context(), client, project, subscription, "example")
			if scenario != "valid" {
				if err == nil {
					t.Fatal("unverified or mismatched asset was accepted")
				}
				return
			}
			if err != nil || destination.StorageAssetID != assetID ||
				destination.ResourceURI != "discovery://resources/inputs/paths/inputs/approved/experiment/v1/" || len(requests) != 4 {
				t.Fatalf("official asset mapping failed: %+v (%v)", destination, err)
			}
			result, err := Describe(project, "example", "", destination)
			if err != nil || !result.AssetVerified || result.BlobVerified || result.ConversationAttached {
				t.Fatalf("read-only handoff claimed upload/attachment: %+v (%v)", result, err)
			}
		})
	}
}

func TestPreparationCreatesChecksumsWithoutOverwriteOrRecoveryState(t *testing.T) {
	project, oldBundle, _ := fixture(t)
	manifestPath := filepath.Join(project.Root, "inputs", "bundle.yaml")
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if prepared, err := Prepare(t.Context(), project, "example"); err == nil {
		prepared.Root.Close()
		t.Fatal("preparation overwrote an existing manifest")
	}
	unchanged, err := os.ReadFile(manifestPath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatal("existing manifest changed")
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	bundle, err := Prepare(t.Context(), project, "example")
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Root.Close()
	if len(bundle.Entries) != len(oldBundle.Entries) || bundle.Fingerprint == "" {
		t.Fatal("generated manifest did not describe the payload and itself")
	}
	again, err := Validate(t.Context(), project, "example")
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project.Root, ".azure", "demo", "discovery")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation wrote cloud recovery state")
	}
}

func TestPreparationRejectsUnsafeInputsBeforeWritingManifest(t *testing.T) {
	for _, scenario := range []string{"symlink", "case collision", "empty", "cancelled", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			project, _, _ := fixture(t)
			manifestPath := filepath.Join(project.Root, "inputs", "bundle.yaml")
			if err := os.Remove(manifestPath); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch scenario {
			case "symlink":
				if err := os.Symlink("/etc/passwd", filepath.Join(project.Root, "inputs", "escape")); err != nil {
					t.Fatal(err)
				}
			case "case collision":
				if err := os.WriteFile(filepath.Join(project.Root, "inputs", "EXPERIMENT.json"), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.Remove(filepath.Join(project.Root, "inputs", "experiment.json")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "oversized":
				if err := os.Truncate(filepath.Join(project.Root, "inputs", "experiment.json"), (1<<30)+1); err != nil {
					t.Fatal(err)
				}
			}
			if bundle, err := Prepare(ctx, project, "example"); err == nil {
				bundle.Root.Close()
				t.Fatal("unsafe preparation succeeded")
			}
			if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed preparation left a manifest")
			}
		})
	}
}
