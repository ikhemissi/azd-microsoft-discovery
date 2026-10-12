package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/google/uuid"
)

const BookshelfVersion = "2026-06-01"

var knowledgeBaseName = regexp.MustCompile(`^[A-Za-z0-9-]{1,24}$`)

type StorageAssetReference struct {
	ID string `json:"id"`
}

type KnowledgeBaseDefinition struct {
	StorageAssetReferences []StorageAssetReference `json:"storageAssetReferences"`
	Description            string                  `json:"description"`
	CopilotInstruction     string                  `json:"copilotInstruction"`
}

type KnowledgeBase struct {
	KnowledgeBaseDefinition
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	BookshelfName     string       `json:"bookshelfName"`
	ProvisioningState string       `json:"provisioningState"`
	Status            string       `json:"status"`
	LastIndexingRun   *IndexingRun `json:"lastIndexingRun,omitempty"`
}

type IndexingRun struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

type IndexingRequest struct {
	ProjectID  string `json:"projectId,omitempty"`
	NodePoolID string `json:"nodePoolId,omitempty"`
}

type KnowledgeBaseOperation struct {
	ID             string       `json:"id"`
	Status         string       `json:"status"`
	OperationType  string       `json:"operationType"`
	IndexingResult *IndexingRun `json:"indexingResult,omitempty"`
}

func (c *Client) ResolveBookshelfByName(ctx context.Context, subscription, resourceGroup, name string) (*Resource, error) {
	id, err := uuid.Parse(subscription)
	if err != nil || len(subscription) != 36 || id == uuid.Nil {
		return nil, errors.New("Bookshelf lookup requires a selected subscription GUID")
	}
	for _, part := range []string{resourceGroup, name} {
		if part == "" || strings.ContainsAny(part, "/\\?# \r\n\t") || part == "." || part == ".." {
			return nil, errors.New("Bookshelf lookup requires explicit resource-group and resource names without path separators")
		}
	}
	resourceID := "/subscriptions/" + id.String() + "/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.Discovery/bookshelves/" + name
	return c.ResolveBookshelf(ctx, resourceID)
}

// ResolveBookshelf only reads Terraform-owned infrastructure. Trust the URI
// returned by ARM, not a hostname constructed from the user's Bookshelf name.
func (c *Client) ResolveBookshelf(ctx context.Context, resourceID string) (*Resource, error) {
	c.bookshelfEndpoint = ""
	if strings.ContainsAny(resourceID, "?# \r\n\t") {
		return nil, errors.New("invalid Bookshelf ARM ID")
	}
	id, err := arm.ParseResourceID(resourceID)
	if err != nil || !strings.EqualFold(id.ResourceType.String(), "Microsoft.Discovery/bookshelves") ||
		id.SubscriptionID == "" || id.ResourceGroupName == "" {
		return nil, errors.New("expected a resource-group-scoped Discovery Bookshelf ARM ID")
	}
	book, err := c.Ready(ctx, resourceID)
	if err != nil {
		return nil, fmt.Errorf("Bookshelf prerequisite: %w", err)
	}
	if book.Name != id.Name {
		return nil, errors.New("Bookshelf readback name does not match its ARM ID")
	}
	endpoint, err := url.Parse(book.Properties.BookshelfURI)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") ||
		!strings.HasSuffix(endpoint.Host, ".discovery.azure.com") {
		return nil, errors.New("Bookshelf readback has no trusted public-cloud data-plane URI")
	}
	c.bookshelfEndpoint = strings.TrimSuffix(endpoint.String(), "/")
	return book, nil
}

func (c *Client) knowledgeBaseURL(name string) (string, error) {
	if c.bookshelfEndpoint == "" {
		return "", errors.New("resolve and verify the Bookshelf before accessing its knowledge bases")
	}
	if !knowledgeBaseName.MatchString(name) {
		return "", errors.New("knowledge-base name must be 1-24 alphanumeric/hyphen characters")
	}
	return c.bookshelfEndpoint + "/knowledgeBases/" + url.PathEscape(name) + "?api-version=" + BookshelfVersion, nil
}

func (c *Client) KnowledgeBase(ctx context.Context, name, bookshelf string) (*KnowledgeBase, error) {
	address, err := c.knowledgeBaseURL(name)
	if err != nil {
		return nil, err
	}
	response, err := c.Request(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	var resource KnowledgeBase
	if err := json.Unmarshal(response.Body, &resource); err != nil {
		return nil, errors.New("invalid GA knowledge-base readback")
	}
	if resource.Name != name || resource.BookshelfName != bookshelf {
		return nil, errors.New("knowledge-base readback identity mismatch")
	}
	return &resource, nil
}

func (c *Client) ReadyKnowledgeBase(ctx context.Context, name, bookshelf string) (*KnowledgeBase, error) {
	for attempt := 0; ; attempt++ {
		resource, err := c.KnowledgeBase(ctx, name, bookshelf)
		if err != nil {
			return nil, err
		}
		done, err := Terminal(resource.ProvisioningState)
		if err != nil || done {
			return resource, err
		}
		if err := c.Sleep(ctx, retryDelay(nil, attempt)); err != nil {
			return nil, fmt.Errorf("KB readiness interrupted; resume the existing update: %w", err)
		}
	}
}

func VerifyKnowledgeBase(resource *KnowledgeBase, expected KnowledgeBaseDefinition) error {
	if resource == nil || resource.ProvisioningState != "Succeeded" {
		return errors.New("knowledge-base readback is not Succeeded")
	}
	if resource.Description != expected.Description {
		return errors.New("knowledge-base readback mismatch: description")
	}
	if resource.CopilotInstruction != expected.CopilotInstruction {
		return errors.New("knowledge-base readback mismatch: copilotInstruction")
	}
	// Source order is not semantically meaningful; duplicates are never accepted.
	actual := map[string]bool{}
	for _, reference := range resource.StorageAssetReferences {
		if actual[strings.ToLower(reference.ID)] {
			return errors.New("knowledge-base readback has duplicate storageAssetReferences")
		}
		actual[strings.ToLower(reference.ID)] = true
	}
	if len(actual) != len(expected.StorageAssetReferences) {
		return errors.New("knowledge-base readback mismatch: storageAssetReferences")
	}
	for _, reference := range expected.StorageAssetReferences {
		if !actual[strings.ToLower(reference.ID)] {
			return errors.New("knowledge-base readback mismatch: storageAssetReferences")
		}
	}
	return nil
}

func (c *Client) ValidateKnowledgeBaseSources(ctx context.Context, definition KnowledgeBaseDefinition) error {
	if len(definition.StorageAssetReferences) == 0 {
		return errors.New("knowledge base requires at least one existing storage asset")
	}
	seen := map[string]bool{}
	// Validate every declaration before performing prerequisite reads.
	for _, reference := range definition.StorageAssetReferences {
		if strings.ContainsAny(reference.ID, "?# \r\n\t") {
			return errors.New("invalid source storage asset ARM ID")
		}
		id, err := arm.ParseResourceID(reference.ID)
		if err != nil || !strings.EqualFold(id.ResourceType.String(), "Microsoft.Discovery/storageContainers/storageAssets") ||
			id.SubscriptionID == "" || id.ResourceGroupName == "" {
			return errors.New("KB source must be a registered Discovery storage asset ARM ID, not a Blob URL or local folder")
		}
		key := strings.ToLower(reference.ID)
		if seen[key] {
			return errors.New("duplicate KB source storage asset")
		}
		seen[key] = true
	}
	for _, reference := range definition.StorageAssetReferences {
		if _, err := c.Ready(ctx, reference.ID); err != nil {
			return fmt.Errorf("knowledge-base source prerequisite: %w", err)
		}
	}
	return nil
}

// ReconcileKnowledgeBase is configuration-only. It returns the operation URL
// so the lifecycle can persist it before polling; it never starts indexing.
func (c *Client) ReconcileKnowledgeBase(ctx context.Context, name, bookshelf string, definition KnowledgeBaseDefinition) (string, error) {
	address, err := c.knowledgeBaseURL(name)
	if err != nil {
		return "", err
	}
	if err := c.ValidateKnowledgeBaseSources(ctx, definition); err != nil {
		return "", err
	}
	remote, err := c.KnowledgeBase(ctx, name, bookshelf)
	if err == nil && VerifyKnowledgeBase(remote, definition) == nil {
		return "", nil
	}
	if err != nil {
		var response *HTTPError
		if !errors.As(err, &response) || response.Status != http.StatusNotFound {
			return "", err
		}
	}
	response, err := c.Request(ctx, http.MethodPatch, address, definition)
	if err != nil {
		return "", err
	}
	operation, err := c.Operation(response, c.bookshelfEndpoint)
	if err == nil && operation != "" {
		err = c.ValidateBookshelfOperationURL(operation)
	}
	return operation, err
}

func (c *Client) ValidateBookshelfOperationURL(address string) error {
	u, err := c.ValidateURL(address)
	if err != nil {
		return err
	}
	book, err := url.Parse(c.bookshelfEndpoint)
	if err != nil || book.Host == "" || u.Host != book.Host {
		return errors.New("operation URL escaped the verified Bookshelf endpoint")
	}
	return nil
}

func (c *Client) ValidateKnowledgeBaseOperationURL(name, address string) error {
	if err := c.ValidateBookshelfOperationURL(address); err != nil {
		return err
	}
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	prefix := "/knowledgeBases/" + name + "/operations/"
	id := strings.TrimPrefix(u.Path, prefix)
	if !knowledgeBaseName.MatchString(name) || !strings.HasPrefix(u.Path, prefix) ||
		id == "" || id == "." || id == ".." || strings.Contains(id, "/") {
		return errors.New("indexing operation URL does not belong to the selected knowledge base")
	}
	return nil
}

func (c *Client) StartKnowledgeBaseIndexing(ctx context.Context, name string, request IndexingRequest) (string, string, error) {
	address, err := c.knowledgeBaseURL(name)
	if err != nil {
		return "", "", err
	}
	for _, field := range []struct{ value, resourceType string }{
		{request.ProjectID, "Microsoft.Discovery/workspaces/projects"},
		{request.NodePoolID, "Microsoft.Discovery/supercomputers/nodePools"},
	} {
		if field.value == "" {
			continue
		}
		id, err := arm.ParseResourceID(field.value)
		if err != nil || id.SubscriptionID == "" || id.ResourceGroupName == "" ||
			!strings.EqualFold(id.ResourceType.String(), field.resourceType) || strings.ContainsAny(field.value, "?# \r\n\t") {
			return "", "", errors.New("indexing project/node pool must be valid ARM references when supplied")
		}
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return "", "", err
	}
	parsed.Path += ":startIndexing"
	response, err := c.Request(ctx, http.MethodPost, parsed.String(), request)
	if err != nil {
		return "", "", err
	}
	operation, err := c.Operation(response, c.bookshelfEndpoint)
	if err != nil || operation == "" {
		return "", "", errors.Join(errors.New("indexing acknowledgement requires a verifiable operation URL"), err)
	}
	operationURL, err := url.Parse(operation)
	endpointURL, endpointErr := url.Parse(c.bookshelfEndpoint)
	if err != nil || endpointErr != nil || operationURL.Host != endpointURL.Host {
		return "", "", errors.New("indexing operation escaped the verified Bookshelf endpoint")
	}
	var acknowledgement struct {
		ID     string       `json:"id"`
		Status string       `json:"status"`
		Result *IndexingRun `json:"result"`
	}
	if err := json.Unmarshal(response.Body, &acknowledgement); err != nil ||
		acknowledgement.ID == "" || acknowledgement.Status == "" {
		return "", "", errors.New("invalid GA indexing acknowledgement; submission may have succeeded")
	}
	if err := c.ValidateKnowledgeBaseOperationURL(name, operation); err != nil {
		return "", "", err
	}
	if path.Base(operationURL.Path) != acknowledgement.ID {
		return "", "", errors.New("indexing acknowledgement/operation URL ID mismatch; submission may have succeeded")
	}
	runID := ""
	if acknowledgement.Result != nil {
		runID = acknowledgement.Result.RunID
	}
	return operation, runID, nil
}

func (c *Client) PollKnowledgeBaseIndexing(ctx context.Context, address string) (*KnowledgeBaseOperation, error) {
	for attempt := 0; ; attempt++ {
		u, err := c.ValidateURL(address)
		if err != nil {
			return nil, err
		}
		book, err := url.Parse(c.bookshelfEndpoint)
		if err != nil || book.Host == "" || u.Host != book.Host ||
			!strings.Contains(u.Path, "/operations/") {
			return nil, errors.New("indexing polling URL must belong to the verified Bookshelf")
		}
		response, err := c.Request(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		var operation KnowledgeBaseOperation
		if err := json.Unmarshal(response.Body, &operation); err != nil ||
			operation.ID == "" || operation.OperationType != "Indexing" || operation.ID != path.Base(u.Path) {
			return nil, errors.New("invalid GA knowledge-base indexing operation response")
		}
		done := false
		if operation.Status != "NotStarted" {
			done, err = Terminal(operation.Status)
		}
		if err != nil {
			return &operation, err
		}
		if done {
			return &operation, nil
		}
		if err := c.Sleep(ctx, max(retryDelay(response.Header, attempt), time.Second)); err != nil {
			return nil, fmt.Errorf("knowledge-base indexing interrupted; resume the existing operation: %w", err)
		}
	}
}

func VerifyKnowledgeBaseIndexing(resource *KnowledgeBase, operation *KnowledgeBaseOperation, acknowledgedRunID string) error {
	if resource == nil || operation == nil || operation.Status != "Succeeded" ||
		resource.ProvisioningState != "Succeeded" || resource.LastIndexingRun == nil {
		return errors.New("knowledge-base indexing completion has no verified run readback")
	}
	run := resource.LastIndexingRun
	if run.RunID == "" || run.Status != "Succeeded" {
		return errors.New("knowledge-base lastIndexingRun is not a successful identified run")
	}
	if acknowledgedRunID != "" && run.RunID != acknowledgedRunID {
		return errors.New("knowledge-base lastIndexingRun differs from the acknowledged run")
	}
	if operation.IndexingResult != nil && operation.IndexingResult.RunID != run.RunID {
		return errors.New("knowledge-base indexing operation/readback run ID mismatch")
	}
	return nil
}
