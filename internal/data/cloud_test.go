package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
)

const subscription = "00000000-0000-0000-0000-000000000001"
const armScope = "/subscriptions/" + subscription + "/resourceGroups/demo/providers/"
const projectID = armScope + "Microsoft.Discovery/workspaces/workspace/projects/project"
const registrationID = armScope + "Microsoft.Discovery/storageContainers/inputs"
const accountID = armScope + "Microsoft.Storage/storageAccounts/demostore"
const blobID = accountID + "/blobServices/default/containers/inputs"

type fakeCredential struct{ scopes *[]string }

func (f fakeCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if f.scopes != nil {
		*f.scopes = append(*f.scopes, options.Scopes...)
	}
	return azcore.AccessToken{Token: "offline", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestStorageResolverScopeAssociationAndVerifiedEndpoint(t *testing.T) {
	for _, failure := range []string{"none", "association", "account", "endpoint", "scope", "missing", "type"} {
		t.Run(failure, func(t *testing.T) {
			project := &config.Project{Discovery: config.Settings{ProjectResourceID: projectID, Data: &config.Data{
				Sources: map[string]config.DataSource{"source": {StorageContainerResourceID: registrationID,
					BlobContainerResourceID: blobID, Prefix: "approved"}},
				Datasets: map[string]config.Dataset{"example": {Source: "source", Namespace: "experiment", Version: "v1"}},
			}}}
			if failure == "type" {
				project.Discovery.ProjectResourceID = registrationID
			}
			client := discovery.NewClient(fakeCredential{})
			mutations := 0
			client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					mutations++
					t.Fatal("resolver mutated Terraform infrastructure")
				}
				status := 200
				resource := &sourceResource{ID: request.URL.Path}
				resource.Properties.ProvisioningState = "Succeeded"
				switch request.URL.Path {
				case projectID:
					resource.Properties.StorageContainerIDs = []string{registrationID}
					if failure == "association" {
						resource.Properties.StorageContainerIDs = nil
					}
				case registrationID:
					resource.Properties.StorageStore.Kind = "AzureStorageBlob"
					resource.Properties.StorageStore.StorageAccountID = accountID
					if failure == "account" {
						resource.Properties.StorageStore.StorageAccountID += "-other"
					}
					if failure == "missing" {
						status = 404
					}
				case accountID:
					if request.URL.Query().Get("api-version") != "2023-05-01" {
						t.Fatal("wrong storage ARM version")
					}
					resource.Properties.PrimaryEndpoints.Blob = "https://demostore.blob.core.windows.net/"
					if failure == "endpoint" {
						resource.Properties.PrimaryEndpoints.Blob = "https://untrusted.invalid/?secret=blocked"
					}
				default:
					t.Fatalf("unexpected resolver request: %s", request.URL.Path)
				}
				raw, err := json.Marshal(resource)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
			})
			selected := subscription
			if failure == "scope" {
				selected = "00000000-0000-0000-0000-000000000002"
			}
			destination, err := Resolve(t.Context(), client, project, selected, "example")
			if failure == "none" {
				if err != nil || destination.URI != "https://demostore.blob.core.windows.net/inputs/approved/experiment/v1/" ||
					destination.ContainerID != blobID {
					t.Fatalf("bad verified destination: %+v (%v)", destination, err)
				}
			} else if err == nil {
				t.Fatal("invalid destination accepted")
			}
			if mutations != 0 {
				t.Fatal("resolver made writes")
			}
		})
	}
}

func TestAzureBlobSDKConditionalWriteAndSanitizedErrors(t *testing.T) {
	scopes := []string{}
	puts := 0
	httpClient := &http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
		status, content := 201, ""
		if request.Method == http.MethodPut {
			puts++
			if request.Header.Get("If-None-Match") != "*" {
				t.Fatal("SDK upload did not enforce no overwrite")
			}
			if puts > 1 {
				status = 412
				content = `<Error><Code>ConditionNotMet</Code><Message>confidential customer path</Message></Error>`
			}
		} else if request.Method == http.MethodGet {
			status = 403
			content = `<Error><Code>AuthorizationPermissionMismatch</Code><Message>signed url and confidential filename</Message></Error>`
		} else {
			t.Fatalf("unexpected SDK request: %s", request.Method)
		}
		headers := http.Header{"Content-Type": []string{"application/xml"}, "Etag": []string{`"offline"`}}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(content)), Request: request}, nil
	})}
	client, err := container.NewClient("https://demostore.blob.core.windows.net/inputs", fakeCredential{&scopes},
		&container.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: httpClient, Retry: policy.RetryOptions{MaxRetries: -1}}})
	if err != nil {
		t.Fatal(err)
	}
	remote := &AzureBlobs{client}
	if err := remote.Create(t.Context(), "approved/data", strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := remote.Create(t.Context(), "approved/data", strings.NewReader("replacement")); !errors.Is(err, ErrExists) {
		t.Fatalf("collision not preserved: %v", err)
	}
	if _, err := remote.Read(t.Context(), "approved/private"); err == nil || strings.Contains(err.Error(), "confidential") ||
		strings.Contains(err.Error(), "signed") || strings.Contains(err.Error(), "private") {
		t.Fatal("storage failure was hidden or leaked sensitive details")
	}
	if len(scopes) == 0 || scopes[0] != "https://storage.azure.com/.default" {
		t.Fatalf("wrong Blob token audience: %v", scopes)
	}
}
