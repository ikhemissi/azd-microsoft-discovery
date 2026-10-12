package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

type knowledgeBaseCloud struct {
	t                                                               *testing.T
	project                                                         *config.Project
	kb                                                              *config.KnowledgeBase
	remote                                                          *discovery.KnowledgeBase
	patches, posts, clients                                         int
	exists, asyncConfig, ambiguous, pollError, staleRun, omitRunIDs bool
	rejectPost, sourceFailure, poolFailure                          int
	configStatus, indexStatus                                       string
}

func knowledgeBaseFixture(t *testing.T, autoindex *bool) (*Provider, *config.Project, *knowledgeBaseCloud) {
	t.Helper()
	project, provider, _ := fixture(t, "reference")
	kb := &config.KnowledgeBase{Name: "papers", BookshelfName: "literature",
		SubscriptionID: authSubscription, ResourceGroup: "demo", Autoindex: autoindex,
		Description: "Scientific papers", CopilotInstruction: "Ground answers in these papers.",
		Sources: []config.KnowledgeBaseSource{{StorageAssetResourceID: testScope + "/storageContainers/data/storageAssets/papers"}},
	}
	project.Services = map[string]*config.Service{"literature": {
		Name: "literature", Host: config.KnowledgeBaseHost, Project: project.Root, KnowledgeBase: kb,
	}}
	cloud := &knowledgeBaseCloud{t: t, project: project, kb: kb, configStatus: "Succeeded", indexStatus: "Succeeded",
		remote: &discovery.KnowledgeBase{ID: "opaque-ga-kb-id", Name: kb.Name, BookshelfName: kb.BookshelfName,
			ProvisioningState: "Succeeded", Status: "NotStarted"},
	}
	provider.deps.CloudInSubscription = func(ctx context.Context, subscription string) (*discovery.Client, error) {
		cloud.clients++
		if subscription != kb.SubscriptionID {
			t.Fatal("KB cloud client used a different subscription")
		}
		client := discovery.NewClient(token{})
		client.Sleep = func(context.Context, time.Duration) error { return errors.New("offline polling interrupted") }
		client.HTTP.Transport = roundTrip(cloud.request)
		return client, nil
	}
	return provider, project, cloud
}

func (f *knowledgeBaseCloud) request(request *http.Request) (*http.Response, error) {
	f.t.Helper()
	if request.URL.Query().Get("api-version") != discovery.BookshelfVersion {
		f.t.Fatal("KB request did not use the June GA API")
	}
	status, headers := 200, http.Header{}
	var body any
	switch {
	case request.URL.Host == "management.azure.com":
		if request.Method != http.MethodGet {
			f.t.Fatal("Terraform-owned infrastructure was mutated")
		}
		id := request.URL.Path
		resource := &discovery.Resource{ID: id, Name: path.Base(id)}
		resource.Properties.ProvisioningState = "Succeeded"
		switch id {
		case config.BookshelfResourceID(f.kb):
			resource.Properties.BookshelfURI = "https://literature.discovery.azure.com"
		case f.kb.Sources[0].StorageAssetResourceID:
			if f.sourceFailure != 0 {
				status = f.sourceFailure
			}
		case f.project.Discovery.ProjectResourceID:
		case f.project.Discovery.NodePoolResourceID, f.kb.NodePoolResourceID:
			if f.poolFailure != 0 {
				status = f.poolFailure
			}
		default:
			f.t.Fatalf("unexpected prerequisite GET: %s", id)
		}
		body = resource
	case request.URL.Path == "/knowledgeBases/papers":
		if request.Method == http.MethodPatch {
			f.patches++
			var definition discovery.KnowledgeBaseDefinition
			data, err := io.ReadAll(request.Body)
			if err != nil || json.Unmarshal(data, &definition) != nil {
				f.t.Fatal("invalid KB PATCH")
			}
			if strings.Contains(string(data), "userAssignedIdentity") || strings.Contains(string(data), "version") {
				f.t.Fatal("identity or KB version was fabricated")
			}
			f.remote.KnowledgeBaseDefinition = definition
			f.exists = true
			if f.asyncConfig {
				headers.Set("Operation-Location", "/knowledgeBases/papers/operations/config-op?api-version=2026-06-01")
				f.remote.ProvisioningState = "Updating"
			} else {
				f.remote.ProvisioningState = "Succeeded"
			}
			status = 201
		} else if request.Method != http.MethodGet {
			f.t.Fatal("unexpected KB method")
		}
		if !f.exists {
			status = 404
		}
		body = f.remote
	case request.URL.Path == "/knowledgeBases/papers:startIndexing":
		if request.Method != http.MethodPost {
			f.t.Fatal("indexing was not a POST")
		}
		f.posts++
		var input discovery.IndexingRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			f.t.Fatal(err)
		}
		expectedPool := f.kb.NodePoolResourceID
		if expectedPool == "" {
			expectedPool = f.project.Discovery.NodePoolResourceID
		}
		if input.ProjectID != f.project.Discovery.ProjectResourceID || input.NodePoolID != expectedPool {
			f.t.Fatal("indexing project/node pool selection changed")
		}
		if f.rejectPost != 0 {
			status, body = f.rejectPost, map[string]any{"error": map[string]any{"code": "Denied"}}
			break
		}
		if f.ambiguous {
			return nil, errors.New("offline acknowledgement lost")
		}
		op, run := fmt.Sprintf("op-%d", f.posts), fmt.Sprintf("run-%d", f.posts)
		headers.Set("Operation-Location", "/knowledgeBases/papers/operations/"+op+"?api-version=2026-06-01")
		body = map[string]any{"id": op, "status": "Running"}
		if !f.omitRunIDs {
			body.(map[string]any)["result"] = map[string]any{"runId": run}
		}
		status = 202
	case request.URL.Path == "/knowledgeBases/papers/operations/config-op":
		if f.pollError {
			status = 403
		}
		body = map[string]any{"id": "config-op", "status": f.configStatus}
		if f.configStatus == "Succeeded" {
			f.remote.ProvisioningState = "Succeeded"
		}
	case strings.HasPrefix(request.URL.Path, "/knowledgeBases/papers/operations/op-"):
		if f.pollError {
			status = 403
		}
		op, run := path.Base(request.URL.Path), fmt.Sprintf("run-%d", f.posts)
		result := &discovery.KnowledgeBaseOperation{ID: op, Status: f.indexStatus, OperationType: "Indexing"}
		if !f.omitRunIDs {
			result.IndexingResult = &discovery.IndexingRun{RunID: run}
		}
		if !f.staleRun && f.indexStatus == "Succeeded" {
			f.remote.Status = "Succeeded"
			f.remote.LastIndexingRun = &discovery.IndexingRun{RunID: run, Status: "Succeeded"}
		}
		body = result
	default:
		f.t.Fatalf("unexpected KB request: %s %s", request.Method, request.URL)
	}
	data, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(data))), Request: request}, nil
}

func deployKnowledgeBase(t *testing.T, provider *Provider) (*azdext.ServiceDeployResult, error) {
	t.Helper()
	service := &azdext.ServiceConfig{Name: "literature", Host: config.KnowledgeBaseHost}
	target, err := provider.GetTargetResource(t.Context(), authSubscription, service, nil)
	if err != nil {
		return nil, err
	}
	return provider.Deploy(t.Context(), service, nil, target, nil)
}

func TestKnowledgeBaseNativeLifecycleAndFreshIndexing(t *testing.T) {
	disabled := false
	for _, autoindex := range []*bool{nil, &disabled} {
		t.Run(fmt.Sprint(autoindex == nil), func(t *testing.T) {
			provider, _, cloud := knowledgeBaseFixture(t, autoindex)
			service := &azdext.ServiceConfig{Name: "literature", Host: config.KnowledgeBaseHost}
			if err := provider.Initialize(t.Context(), service); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Package(t.Context(), service, nil, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Publish(t.Context(), service, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if cloud.clients != 0 {
				t.Fatal("KB Package/Publish accessed Azure")
			}
			result, err := deployKnowledgeBase(t, provider)
			if err != nil || result.Artifacts[0].Metadata["knowledgeBaseId"] != "opaque-ga-kb-id" {
				t.Fatalf("KB native deploy failed: %v", err)
			}
			expectedPosts := 0
			if autoindex == nil {
				expectedPosts = 1
				if result.Artifacts[0].Metadata["indexingStatus"] != "Succeeded" {
					t.Fatal("autoindex did not verify readiness")
				}
			} else if result.Artifacts[0].Metadata["indexingStatus"] != "NotStarted" {
				t.Fatal("configuration-only deployment claimed indexed readiness")
			}
			if cloud.posts != expectedPosts || cloud.patches != 1 {
				t.Fatal("unexpected KB mutations")
			}
			if _, err := deployKnowledgeBase(t, provider); err != nil {
				t.Fatal(err)
			}
			if cloud.patches != 1 || cloud.posts != 2*expectedPosts {
				t.Fatal("fresh deployment did not index new storage content or unnecessarily changed the definition")
			}
			if _, err := provider.Index(t.Context(), "literature", nil); err != nil {
				t.Fatal(err)
			}
			if cloud.patches != 1 || cloud.posts != 2*expectedPosts+1 {
				t.Fatal("explicit indexing reconciled the KB or failed to start a fresh run")
			}
		})
	}
}

func TestKnowledgeBaseRecoveryNoDuplicateIndexOrPatch(t *testing.T) {
	for _, phase := range []string{"configuration", "indexing", "indexing-poll-denied", "export"} {
		t.Run(phase, func(t *testing.T) {
			provider, project, cloud := knowledgeBaseFixture(t, nil)
			if phase == "configuration" {
				cloud.asyncConfig, cloud.pollError = true, true
			} else if phase == "indexing" {
				cloud.indexStatus = "Running"
			} else if phase == "indexing-poll-denied" {
				cloud.pollError = true
			} else {
				provider.deps.Export = func(context.Context, string, string) error { return errors.New("offline export failure") }
			}
			if _, err := deployKnowledgeBase(t, provider); err == nil {
				t.Fatal("interrupted deployment claimed success")
			}
			if phase != "export" {
				cloud.kb.Description = "changed while pending"
				if _, err := deployKnowledgeBase(t, provider); err == nil {
					t.Fatal("pending deployment accepted different desired configuration")
				}
				cloud.kb.Description = "Scientific papers"
			}
			cloud.pollError, cloud.indexStatus = false, "Succeeded"
			provider.deps.Export = func(context.Context, string, string) error { return nil }
			if _, err := deployKnowledgeBase(t, provider); err != nil {
				t.Fatalf("KB recovery failed: %v", err)
			}
			if cloud.patches != 1 || cloud.posts != 1 {
				t.Fatal("recovery submitted a duplicate PATCH/indexing POST")
			}
			store, err := state.Open(project.Root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Read("literature", config.BookshelfResourceID(cloud.kb)+"/knowledgeBases/papers")
			if err != nil || record.Phase != "complete" || record.Indexing.CompletedRunID != "run-1" {
				t.Fatalf("successful KB completion was not persisted: %+v (%v)", record, err)
			}
		})
	}
}

func TestKnowledgeBaseAmbiguityTerminalFailureAndStaleRun(t *testing.T) {
	for _, failure := range []string{"ambiguous", "rejected", "failed", "stale"} {
		t.Run(failure, func(t *testing.T) {
			provider, _, cloud := knowledgeBaseFixture(t, nil)
			switch failure {
			case "ambiguous":
				cloud.ambiguous = true
			case "rejected":
				cloud.rejectPost = 403
			case "failed":
				cloud.indexStatus = "Failed"
			case "stale":
				cloud.omitRunIDs, cloud.staleRun = true, true
				cloud.remote.LastIndexingRun = &discovery.IndexingRun{RunID: "prior-run", Status: "Succeeded"}
			}
			if _, err := deployKnowledgeBase(t, provider); err == nil {
				t.Fatal("unsafe indexing outcome was accepted")
			}
			cloud.ambiguous, cloud.rejectPost, cloud.indexStatus = false, 0, "Succeeded"
			_, err := deployKnowledgeBase(t, provider)
			if failure == "ambiguous" || failure == "stale" {
				if err == nil || cloud.posts != 1 {
					t.Fatal("ambiguous/stale indexing was resubmitted or accepted")
				}
				if failure == "stale" {
					cloud.staleRun = false
					if _, err := deployKnowledgeBase(t, provider); err != nil || cloud.posts != 1 {
						t.Fatalf("delayed run readback did not recover without resubmission: %v", err)
					}
				}
			} else if err != nil || cloud.posts != 2 || cloud.patches != 1 {
				t.Fatalf("definitive failure could not be corrected: %v", err)
			}
		})
	}
}

func TestKnowledgeBaseIndexRequiresDeployedMatchingDefinition(t *testing.T) {
	disabled := false
	provider, _, cloud := knowledgeBaseFixture(t, &disabled)
	if _, err := provider.Index(t.Context(), "literature", nil); err == nil || cloud.posts != 0 || cloud.patches != 0 {
		t.Fatal("manual indexing created a missing KB")
	}
	if _, err := deployKnowledgeBase(t, provider); err != nil {
		t.Fatal(err)
	}
	cloud.kb.Description = "not yet deployed"
	if _, err := provider.Index(t.Context(), "literature", nil); err == nil || cloud.posts != 0 || cloud.patches != 1 {
		t.Fatal("manual indexing silently changed a drifted definition")
	}
	cloud.kb.Description = "Scientific papers"
	cloud.sourceFailure = 404
	if _, err := provider.Index(t.Context(), "literature", nil); err == nil || cloud.posts != 0 {
		t.Fatal("missing source registration was ignored")
	}
	cloud.sourceFailure = 0
	cloud.remote.Status = "Running"
	if _, err := provider.Index(t.Context(), "literature", nil); err == nil || cloud.posts != 0 {
		t.Fatal("untracked active indexing was duplicated")
	}
}

func TestKnowledgeBaseScopeOverrideAndNoInferredCompute(t *testing.T) {
	provider, project, cloud := knowledgeBaseFixture(t, nil)
	cloud.kb.SubscriptionID, cloud.kb.ResourceGroup = otherSubscription, "other-group"
	project.Discovery.NodePoolResourceID = ""
	service := &azdext.ServiceConfig{Name: "literature", Host: config.KnowledgeBaseHost}
	target, err := provider.GetTargetResource(t.Context(), authSubscription, service, nil)
	if err != nil || target.SubscriptionId != otherSubscription || target.ResourceGroupName != "other-group" {
		t.Fatalf("KB explicit scope override was ignored: %v", err)
	}
	if _, err := provider.Deploy(t.Context(), service, nil, target, nil); err != nil {
		t.Fatal(err)
	}
	if cloud.posts != 1 {
		t.Fatal("optional node pool prevented indexing")
	}
}

func TestKnowledgeBaseReadbackFailureDoesNotDiscardAcknowledgedIndex(t *testing.T) {
	provider, project, cloud := knowledgeBaseFixture(t, nil)
	cloud.indexStatus = "Running"
	if _, err := deployKnowledgeBase(t, provider); err == nil {
		t.Fatal("running index unexpectedly completed")
	}
	cloud.remote.ProvisioningState = "Failed"
	if _, err := deployKnowledgeBase(t, provider); err == nil || cloud.posts != 1 {
		t.Fatal("failed KB readback discarded or duplicated an acknowledged operation")
	}
	store, err := state.Open(project.Root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Read("literature", config.BookshelfResourceID(cloud.kb)+"/knowledgeBases/papers")
	if err != nil || record.Phase != "kb-index-polling" || record.Operation == "" {
		t.Fatal("resource readback failure erased pending operation correlation")
	}
	cloud.remote.ProvisioningState, cloud.indexStatus = "Succeeded", "Succeeded"
	if _, err := deployKnowledgeBase(t, provider); err != nil || cloud.posts != 1 {
		t.Fatalf("acknowledged operation did not recover after transient readback failure: %v", err)
	}
}

func TestKnowledgeBaseManualIndexExportRecoveryAndConfigTerminalFailure(t *testing.T) {
	disabled := false
	provider, _, cloud := knowledgeBaseFixture(t, &disabled)
	cloud.asyncConfig, cloud.configStatus = true, "Failed"
	if _, err := deployKnowledgeBase(t, provider); err == nil || cloud.posts != 0 {
		t.Fatal("failed KB configuration operation started indexing or claimed success")
	}
	cloud.configStatus, cloud.kb.Description = "Succeeded", "corrected"
	if _, err := deployKnowledgeBase(t, provider); err != nil || cloud.patches != 2 {
		t.Fatalf("terminal configuration failure could not be corrected: %v", err)
	}
	provider.deps.Export = func(context.Context, string, string) error { return errors.New("manual export failed") }
	if _, err := provider.Index(t.Context(), "literature", nil); err == nil {
		t.Fatal("manual export failure was hidden")
	}
	provider.deps.Export = func(context.Context, string, string) error { return nil }
	if _, err := provider.Index(t.Context(), "literature", nil); err != nil || cloud.posts != 1 || cloud.patches != 2 {
		t.Fatalf("manual export restoration duplicated a job or changed the definition: %v", err)
	}
}

func TestKnowledgeBaseComputeSelectionCanBeCorrectedBeforeSubmission(t *testing.T) {
	provider, _, cloud := knowledgeBaseFixture(t, nil)
	cloud.poolFailure = 404
	if _, err := deployKnowledgeBase(t, provider); err == nil || cloud.posts != 0 {
		t.Fatal("missing compute did not block indexing")
	}
	cloud.poolFailure = 0
	cloud.kb.NodePoolResourceID = testScope + "/supercomputers/cpu/nodePools/alternative"
	if _, err := deployKnowledgeBase(t, provider); err != nil || cloud.posts != 1 || cloud.patches != 1 {
		t.Fatalf("safe compute correction was blocked before any indexing submission: %v", err)
	}
}
