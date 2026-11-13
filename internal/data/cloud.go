package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/project"
)

type sourceResource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties struct {
		ProvisioningState   string   `json:"provisioningState"`
		Path                string   `json:"path"`
		StorageContainerIDs []string `json:"storageContainerIds"`
		StorageStore        struct {
			Kind             string `json:"kind"`
			StorageAccountID string `json:"storageAccountId"`
		} `json:"storageStore"`
		PrimaryEndpoints struct {
			Blob string `json:"blob"`
		} `json:"primaryEndpoints"`
	} `json:"properties"`
}

func readSourceResource(ctx context.Context, client *discovery.Client, id, version string) (*sourceResource, error) {
	response, err := client.Request(ctx, http.MethodGet, discovery.ARMEndpoint+id+"?api-version="+version, nil)
	if err != nil {
		return nil, err
	}
	var resource sourceResource
	if json.Unmarshal(response.Body, &resource) != nil || !strings.EqualFold(resource.ID, id) {
		return nil, errors.New("data prerequisite readback identity is invalid")
	}
	if resource.Properties.ProvisioningState != "Succeeded" {
		return nil, errors.New("data prerequisite is not Succeeded; provision it through Terraform first")
	}
	return &resource, nil
}

func Resolve(ctx context.Context, client *discovery.Client, project *config.Project, selectedSubscription,
	name string) (Destination, error) {
	if project.Discovery.Data == nil {
		return Destination{}, errors.New("no data configuration")
	}
	dataset, exists := project.Discovery.Data.Datasets[name]
	if !exists {
		return Destination{}, errors.New("dataset is not declared")
	}
	source := project.Discovery.Data.Sources[dataset.Source]
	for _, field := range []struct{ value, kind string }{
		{project.Discovery.ProjectResourceID, "Microsoft.Discovery/workspaces/projects"},
		{source.StorageContainerResourceID, "Microsoft.Discovery/storageContainers"},
		{source.BlobContainerResourceID, "Microsoft.Storage/storageAccounts/blobServices/containers"},
	} {
		id, err := arm.ParseResourceID(field.value)
		if err != nil || !strings.EqualFold(id.ResourceType.String(), field.kind) ||
			!strings.EqualFold(id.SubscriptionID, selectedSubscription) || id.ResourceGroupName == "" ||
			strings.ContainsAny(field.value, "?# \r\n\t") {
			return Destination{}, errors.New("data/project resource type or selected subscription scope is invalid")
		}
	}
	projectResource, err := readSourceResource(ctx, client, project.Discovery.ProjectResourceID, discovery.ARMVersion)
	if err != nil {
		return Destination{}, err
	}
	associated := false
	for _, id := range projectResource.Properties.StorageContainerIDs {
		associated = associated || strings.EqualFold(id, source.StorageContainerResourceID)
	}
	if !associated {
		return Destination{}, errors.New("Discovery storage registration is not associated with the configured project")
	}
	registration, err := readSourceResource(ctx, client, source.StorageContainerResourceID, discovery.ARMVersion)
	if err != nil {
		return Destination{}, err
	}
	blobID, err := arm.ParseResourceID(source.BlobContainerResourceID)
	if err != nil {
		return Destination{}, err
	}
	if blobID.Parent == nil || blobID.Parent.Name != "default" || blobID.Parent.Parent == nil {
		return Destination{}, errors.New("expected the default Blob service and an existing Blob container")
	}
	accountID := blobID.Parent.Parent.String()
	if registration.Properties.StorageStore.Kind != "AzureStorageBlob" ||
		!strings.EqualFold(registration.Properties.StorageStore.StorageAccountID, accountID) {
		return Destination{}, errors.New("Blob container is not backed by the registered Discovery storage account")
	}
	account, err := readSourceResource(ctx, client, accountID, "2023-05-01")
	if err != nil {
		return Destination{}, err
	}
	endpoint, err := url.Parse(account.Properties.PrimaryEndpoints.Blob)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") ||
		endpoint.Host != strings.ToLower(blobID.Parent.Parent.Name)+".blob.core.windows.net" {
		return Destination{}, errors.New("storage account has no verified public-cloud Blob endpoint")
	}
	for _, part := range []string{source.Prefix, dataset.Namespace, dataset.Version} {
		if err := config.ValidateDataPath(part); err != nil {
			return Destination{}, err
		}
	}
	prefix := source.Prefix + "/" + dataset.Namespace + "/" + dataset.Version
	base := strings.TrimSuffix(endpoint.String(), "/") + "/" + url.PathEscape(blobID.Name)
	destination := Destination{ContainerID: source.BlobContainerResourceID, Prefix: prefix, URI: base + "/" + prefix + "/"}
	if dataset.StorageAssetResourceID != "" {
		assetID, err := arm.ParseResourceID(dataset.StorageAssetResourceID)
		if err != nil || assetID.Parent == nil || !strings.EqualFold(assetID.ResourceType.String(), "Microsoft.Discovery/storageContainers/storageAssets") ||
			!strings.EqualFold(assetID.Parent.String(), source.StorageContainerResourceID) ||
			strings.ContainsAny(dataset.StorageAssetResourceID, "?# \r\n\t") {
			return Destination{}, errors.New("dataset asset does not belong to the declared Discovery storage container")
		}
		asset, err := readSourceResource(ctx, client, dataset.StorageAssetResourceID, discovery.ARMVersion)
		if err != nil {
			return Destination{}, err
		}
		expectedPath := blobID.Name + "/" + prefix + "/"
		if asset.Name != assetID.Name || asset.Properties.Path != expectedPath {
			return Destination{}, errors.New("dataset asset readback must identify the selected asset and the exact Blob container/prefix with a trailing directory slash")
		}
		// The official addressing contract uses the registration name and its
		// account-relative asset path, including the physical Blob container.
		registrationID, err := arm.ParseResourceID(source.StorageContainerResourceID)
		if err != nil || registration.Name != registrationID.Name {
			return Destination{}, errors.New("Discovery storage container readback name is invalid")
		}
		destination.StorageAssetID = asset.ID
		destination.ResourceURI = (&url.URL{Scheme: "discovery", Host: "resources",
			Path: "/" + registration.Name + "/paths/" + asset.Properties.Path}).String()
	}
	return destination, nil
}

type AzureBlobs struct{ client *container.Client }

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Blob operation interrupted; resume the same immutable upload")
	}
	var response *azcore.ResponseError
	if errors.As(err, &response) {
		if response.ErrorCode == "BlobNotFound" {
			return ErrMissing
		}
		if response.StatusCode == http.StatusPreconditionFailed || response.ErrorCode == "BlobAlreadyExists" {
			return ErrExists
		}
		return fmt.Errorf("Blob request failed (HTTP %d); verify authorized data access and networking", response.StatusCode)
	}
	return errors.New("Blob transport failed; upload completion is not established")
}

func Connect(ctx context.Context, client *azdext.AzdClient, environment string, projectConfig *config.Project,
	dataset string) (Destination, BlobStore, error) {
	values, err := client.Environment().GetValues(ctx, &azdext.GetEnvironmentRequest{Name: environment})
	if err != nil {
		return Destination{}, nil, errors.New("cannot read selected environment subscription")
	}
	subscription := ""
	for _, pair := range values.GetKeyValues() {
		if pair.GetKey() == "AZURE_SUBSCRIPTION_ID" {
			subscription = pair.GetValue()
		}
	}
	credential, err := project.CredentialForEnvironment(ctx, client, environment)
	if err != nil {
		return Destination{}, nil, err
	}
	destination, err := Resolve(ctx, discovery.NewClient(credential), projectConfig, subscription, dataset)
	if err != nil {
		return Destination{}, nil, err
	}
	source := projectConfig.Discovery.Data.Sources[projectConfig.Discovery.Data.Datasets[dataset].Source]
	containerURL := strings.TrimSuffix(destination.URI, "/"+destination.Prefix+"/")
	blobClient, err := container.NewClient(containerURL, credential, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Retry: policy.RetryOptions{MaxRetries: 3, RetryDelay: time.Second, MaxRetryDelay: 10 * time.Second, TryTimeout: 120 * time.Second},
			Transport: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("Blob redirects are not permitted")
			}},
		},
	})
	if err != nil {
		return Destination{}, nil, errors.New("cannot construct a verified Blob client")
	}
	if _, err := blobClient.GetProperties(ctx, nil); err != nil {
		return Destination{}, nil, storageError(err)
	}
	destination.ContainerID = source.BlobContainerResourceID
	return destination, &AzureBlobs{blobClient}, nil
}

func (a *AzureBlobs) List(ctx context.Context, prefix string) ([]string, error) {
	pager := a.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix})
	names := []string{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, storageError(err)
		}
		if page.Segment == nil {
			return nil, errors.New("invalid Blob listing response")
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name == nil || len(names) >= 1003 {
				return nil, errors.New("upload prefix has invalid or excessive existing objects")
			}
			names = append(names, *item.Name)
		}
	}
	return names, nil
}

func (a *AzureBlobs) Read(ctx context.Context, name string) (io.ReadCloser, error) {
	response, err := a.client.NewBlobClient(name).DownloadStream(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	return response.Body, nil
}

func (a *AzureBlobs) Create(ctx context.Context, name string, reader io.Reader) error {
	_, err := a.client.NewBlockBlobClient(name).UploadStream(ctx, reader, &blockblob.UploadStreamOptions{
		BlockSize: 4 << 20, Concurrency: 1,
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: to.Ptr(azcore.ETagAny)}},
	})
	return storageError(err)
}
