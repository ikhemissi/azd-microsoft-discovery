package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const kbScope = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery"
const bookshelfID = kbScope + "/bookshelves/literature"
const assetID = kbScope + "/storageContainers/data/storageAssets/papers"

func kbFixture(t *testing.T) (*Client, *KnowledgeBase, *int, *int) {
	t.Helper()
	definition := KnowledgeBaseDefinition{
		StorageAssetReferences: []StorageAssetReference{{ID: assetID}},
		Description:            "Scientific papers", CopilotInstruction: "Ground answers in these papers.",
	}
	remote := &KnowledgeBase{KnowledgeBaseDefinition: definition,
		ID: "opaque-ga-kb-id", Name: "papers", BookshelfName: "literature", ProvisioningState: "Succeeded", Status: "NotStarted"}
	patches, posts := 0, 0
	client := NewClient(credential{})
	client.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("api-version") != BookshelfVersion {
			t.Fatal("KB slice did not use June GA")
		}
		var body any
		headers := http.Header{}
		code := 200
		switch request.URL.Path {
		case bookshelfID:
			if request.Method != http.MethodGet {
				t.Fatal("Bookshelf infrastructure was mutated")
			}
			body = map[string]any{"id": bookshelfID, "name": "literature", "properties": map[string]any{
				"provisioningState": "Succeeded", "bookshelfUri": "https://literature.discovery.azure.com",
				"workloadIdentities": map[string]any{"identity-one": nil, "identity-two": nil},
			}}
		case assetID:
			if request.Method != http.MethodGet {
				t.Fatal("Terraform-owned storage registration was mutated")
			}
			body = map[string]any{"id": assetID, "properties": map[string]any{"provisioningState": "Succeeded"}}
		case "/knowledgeBases/papers":
			if request.Method == http.MethodPatch {
				patches++
				data := json.NewDecoder(request.Body)
				var desired map[string]any
				if err := data.Decode(&desired); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(desired)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "userAssignedIdentity") || strings.Contains(string(encoded), "version") {
					t.Fatal("optional identity or KB version was invented")
				}
				if err := json.Unmarshal(encoded, &remote.KnowledgeBaseDefinition); err != nil {
					t.Fatal(err)
				}
				code = 201
			}
			body = remote
		case "/knowledgeBases/papers:startIndexing":
			posts++
			body = map[string]any{"id": "index-op", "status": "Running", "result": map[string]any{"runId": "run-1"}}
			headers.Set("Operation-Location", "/knowledgeBases/papers/operations/index-op?api-version=2026-06-01")
			code = 202
			remote.Status = "Succeeded"
			remote.LastIndexingRun = &IndexingRun{RunID: "run-1", Status: "Succeeded"}
		case "/knowledgeBases/papers/operations/index-op":
			body = KnowledgeBaseOperation{ID: "index-op", Status: "Succeeded",
				OperationType: "Indexing", IndexingResult: &IndexingRun{RunID: "run-1"}}
		default:
			t.Fatalf("unexpected KB request: %s %s", request.Method, request.URL)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return response(request, code, string(encoded), headers), nil
	})
	return client, remote, &patches, &posts
}

func TestGAKnowledgeBaseConfigurationAndExplicitIndexing(t *testing.T) {
	client, remote, patches, posts := kbFixture(t)
	if _, err := client.ResolveBookshelfByName(t.Context(), "00000000-0000-0000-0000-000000000001", "demo", "literature"); err != nil {
		t.Fatal(err)
	}
	desired := remote.KnowledgeBaseDefinition
	if _, err := client.ReconcileKnowledgeBase(t.Context(), "papers", "literature", desired); err != nil {
		t.Fatal(err)
	}
	if *patches != 0 || *posts != 0 {
		t.Fatal("unchanged configuration was mutated or indexed")
	}
	desired.Description = "Updated scientific papers"
	if _, err := client.ReconcileKnowledgeBase(t.Context(), "papers", "literature", desired); err != nil {
		t.Fatal(err)
	}
	readback, err := client.KnowledgeBase(t.Context(), "papers", "literature")
	if err != nil || VerifyKnowledgeBase(readback, desired) != nil || *patches != 1 || *posts != 0 {
		t.Fatalf("configuration-only update failed: %v", err)
	}
	location, run, err := client.StartKnowledgeBaseIndexing(t.Context(), "papers", IndexingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.PollKnowledgeBaseIndexing(t.Context(), location)
	if err != nil {
		t.Fatal(err)
	}
	readback, err = client.KnowledgeBase(t.Context(), "papers", "literature")
	if err != nil || VerifyKnowledgeBaseIndexing(readback, operation, run) != nil || *posts != 1 {
		t.Fatalf("indexing verification failed: %v", err)
	}
	readback.LastIndexingRun.RunID = "older-run"
	if err := VerifyKnowledgeBaseIndexing(readback, operation, run); err == nil {
		t.Fatal("older indexing result accepted")
	}
}

func TestKnowledgeBaseSourceAndEndpointGuards(t *testing.T) {
	client, remote, _, _ := kbFixture(t)
	if _, err := client.KnowledgeBase(t.Context(), "papers", "literature"); err == nil {
		t.Fatal("unverified Bookshelf endpoint accepted")
	}
	if _, err := client.ResolveBookshelf(t.Context(), bookshelfID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"https://storage.blob.core.windows.net/papers", "./local-folder", kbScope + "/storageContainers/data", assetID + "?extra"} {
		desired := remote.KnowledgeBaseDefinition
		desired.StorageAssetReferences = []StorageAssetReference{{ID: id}}
		if err := client.ValidateKnowledgeBaseSources(t.Context(), desired); err == nil {
			t.Fatalf("invalid source accepted: %s", id)
		}
	}
	desired := remote.KnowledgeBaseDefinition
	desired.StorageAssetReferences = []StorageAssetReference{{ID: assetID}, {ID: strings.ToUpper(assetID)}}
	if err := client.ValidateKnowledgeBaseSources(t.Context(), desired); err == nil {
		t.Fatal("duplicate ARM sources accepted")
	}
	for _, destination := range []string{"https://evil.invalid/operations/op", ARMEndpoint + "/operations/op"} {
		if _, err := client.PollKnowledgeBaseIndexing(t.Context(), destination); err == nil {
			t.Fatal("operation destination escaped the Bookshelf")
		}
	}
	remote.Description = "drift"
	if err := VerifyKnowledgeBase(remote, desired); err == nil {
		t.Fatal("managed configuration drift accepted")
	}
}

func TestKnowledgeBaseIndexingFailuresAndCancellation(t *testing.T) {
	client, _, _, _ := kbFixture(t)
	if _, err := client.ResolveBookshelf(t.Context(), bookshelfID); err != nil {
		t.Fatal(err)
	}
	address := "https://literature.discovery.azure.com/knowledgeBases/papers/operations/index-op?api-version=2026-06-01"
	for _, status := range []string{"Failed", "Canceled", "unknown"} {
		client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
			return response(request, 200, `{"id":"index-op","operationType":"Indexing","status":"`+status+`"}`, nil), nil
		})
		if _, err := client.PollKnowledgeBaseIndexing(t.Context(), address); err == nil {
			t.Fatalf("indexing status %s was accepted", status)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.PollKnowledgeBaseIndexing(ctx, address); err == nil {
		t.Fatal("canceled indexing poll succeeded")
	}
}

func TestKnowledgeBasePrerequisiteAndSubmissionFailures(t *testing.T) {
	for _, uri := range []string{"http://literature.discovery.azure.com", "https://evil.invalid", "https://literature.discovery.azure.com/path", "https://user@literature.discovery.azure.com"} {
		client := NewClient(credential{})
		client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
			data, err := json.Marshal(map[string]any{"id": bookshelfID, "name": "literature", "properties": map[string]any{
				"provisioningState": "Succeeded", "bookshelfUri": uri,
			}})
			if err != nil {
				return nil, err
			}
			return response(request, 200, string(data), nil), nil
		})
		if _, err := client.ResolveBookshelf(t.Context(), bookshelfID); err == nil {
			t.Fatalf("untrusted Bookshelf URI accepted: %s", uri)
		}
	}
	client, remote, patches, _ := kbFixture(t)
	if _, err := client.ResolveBookshelf(t.Context(), bookshelfID); err != nil {
		t.Fatal(err)
	}
	base := client.HTTP.Transport
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == assetID {
			return response(request, 404, `{}`, nil), nil
		}
		return base.RoundTrip(request)
	})
	if _, err := client.ReconcileKnowledgeBase(t.Context(), "papers", "literature", remote.KnowledgeBaseDefinition); err == nil || *patches != 0 {
		t.Fatal("missing Terraform-owned source did not block KB mutation")
	}
	for _, scenario := range []string{"denied", "no-operation", "invalid-body"} {
		calls := 0
		client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
			calls++
			code, body := 202, `{}`
			header := http.Header{}
			switch scenario {
			case "denied":
				code = 403
			case "invalid-body":
				body = "invalid"
				header.Set("Operation-Location", "/knowledgeBases/papers/operations/op")
			}
			return response(request, code, body, header), nil
		})
		if _, _, err := client.StartKnowledgeBaseIndexing(t.Context(), "papers", IndexingRequest{}); err == nil || calls != 1 {
			t.Fatalf("submission failure %s was hidden or retried", scenario)
		}
	}
}
