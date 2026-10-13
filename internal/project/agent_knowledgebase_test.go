package project

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

func kbAgentFixture(t *testing.T, autoindex *bool) (*Provider, *config.Project, *knowledgeBaseCloud, *fakeCloud, *azdext.ServiceConfig) {
	t.Helper()
	provider, project, kbCloud := knowledgeBaseFixture(t, autoindex)
	project.Services["researcher"] = &config.Service{Name: "researcher", Host: config.AgentHost, Project: project.Root,
		Uses: []string{"literature"},
		Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "chat"},
			KnowledgeBases: []config.KnowledgeBaseBinding{{Service: "literature"}}},
		Definition: map[string]any{"name": "researcher", "kind": "prompt", "instructions": "Cite available literature; preserve {{runtime}}."},
	}
	agentCloud := &fakeCloud{t: t, project: project}
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return agentCloud.client(), nil }
	return provider, project, kbCloud, agentCloud, &azdext.ServiceConfig{Name: "researcher", Host: config.AgentHost}
}

func TestAgentKBAttachmentLifecycleAndExplicitRemoval(t *testing.T) {
	provider, project, kbCloud, agentCloud, sc := kbAgentFixture(t, nil)
	for _, operation := range []func() error{
		func() error { return provider.Initialize(t.Context(), sc) },
		func() error { _, err := provider.Package(t.Context(), sc, nil, nil); return err },
	} {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	if kbCloud.clients != 0 {
		t.Fatal("agent Package resolved KBs through Azure")
	}
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil || agentCloud.posts != 0 {
		t.Fatal("an agent attached an undeployed KB")
	}
	if _, err := deployKnowledgeBase(t, provider); err != nil {
		t.Fatal(err)
	}
	agentCloud.ambiguous = true
	for range 2 {
		if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if agentCloud.posts != 1 || len(agentCloud.agent.KnowledgeBases) != 1 ||
		agentCloud.agent.KnowledgeBases[0].KnowledgeBaseID != kbCloud.remote.ID ||
		kbCloud.posts != 1 || kbCloud.patches != 1 {
		t.Fatal("attachment changed server IDs, created duplicate agent versions or implicitly reindexed")
	}
	if _, exists := project.Services["researcher"].Definition["discoveryExtensions"]; exists {
		t.Fatal("attachment adaptation mutated the source agent definition")
	}
	agentCloud.ambiguous = false
	project.Services["researcher"].Agent.KnowledgeBases = nil
	clearSeen := false
	client := agentCloud.client()
	base := client.HTTP.Transport
	client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			content, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			request.Body = io.NopCloser(strings.NewReader(string(content)))
			var body map[string]any
			if err := json.Unmarshal(content, &body); err != nil {
				t.Fatal(err)
			}
			bases, exists := body["knowledgeBases"].([]any)
			clearSeen = exists && len(bases) == 0
		}
		return base.RoundTrip(request)
	})
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil || !clearSeen ||
		agentCloud.posts != 2 || len(agentCloud.agent.KnowledgeBases) != 0 {
		t.Fatalf("managed attachment removal was not explicit and verified: %v", err)
	}
}

func TestAgentCanBindToolsAndKnowledgeBasesTogether(t *testing.T) {
	provider, project, kbCloud, agentCloud, sc := kbAgentFixture(t, nil)
	toolProject, _, _ := fixture(t, "reference")
	tool := toolProject.Services["simulation"]
	tool.Project = project.Root
	project.Services[tool.Name] = tool
	agent := project.Services["researcher"]
	agent.Uses = append(agent.Uses, tool.Name)
	agent.Agent.Bindings = map[string]config.Binding{"gromacsToolId": {Service: tool.Name, Output: "resourceId"}}
	agent.Definition["discoveryExtensions"] = map[string]any{"tools": []any{map[string]any{
		"toolId": "{{gromacsToolId}}", "confirmation": "Enabled",
	}}}
	if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: tool.Name, Host: config.ToolHost}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := deployKnowledgeBase(t, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	tools := agentCloud.agent.Tools.([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["toolId"] != testScope+"/tools/demo-tool" ||
		len(agentCloud.agent.KnowledgeBases) != 1 || agentCloud.agent.KnowledgeBases[0].KnowledgeBaseID != kbCloud.remote.ID {
		t.Fatal("tool binding and KB attachment did not coexist in the native payload")
	}
}

func TestAgentKBAttachmentRequiresSuccessfulManualIndexWhenDisabled(t *testing.T) {
	disabled := false
	provider, _, kbCloud, agentCloud, sc := kbAgentFixture(t, &disabled)
	if _, err := deployKnowledgeBase(t, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil || agentCloud.posts != 0 || kbCloud.posts != 0 {
		t.Fatal("autoindex:false was overridden or unindexed KB was attached")
	}
	if _, err := provider.Index(t.Context(), "literature", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := deployKnowledgeBase(t, provider); err != nil || kbCloud.posts != 1 {
		t.Fatalf("unchanged configuration-only deployment lost verified indexing or started another job: %v", err)
	}
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil || agentCloud.posts != 1 || kbCloud.posts != 1 {
		t.Fatalf("manually indexed KB could not be attached: %v", err)
	}
}

func TestAgentKBAttachmentFailuresPreventSubmission(t *testing.T) {
	for _, scenario := range []string{"missing id", "changed id", "drift", "failed resource", "failed index", "changed run", "stale configuration", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			provider, project, kbCloud, agentCloud, sc := kbAgentFixture(t, nil)
			if _, err := deployKnowledgeBase(t, provider); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing id":
				kbCloud.remote.ID = ""
			case "changed id":
				kbCloud.remote.ID = "unexpected-new-id"
			case "drift":
				kbCloud.remote.Description = "unexpected"
			case "failed resource":
				kbCloud.remote.ProvisioningState = "Failed"
			case "failed index":
				kbCloud.remote.LastIndexingRun.Status = "Failed"
			case "changed run":
				kbCloud.remote.LastIndexingRun.RunID = "unverified-run"
			case "stale configuration":
				kbCloud.kb.Description = "not deployed"
			case "pending":
				store, err := state.Open(project.Root, "demo")
				if err != nil {
					t.Fatal(err)
				}
				record, err := store.Read("literature", config.BookshelfResourceID(kbCloud.kb)+"/knowledgeBases/papers")
				if err != nil {
					t.Fatal(err)
				}
				record.Phase = "kb-index-polling"
				if err := store.Write(record); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil || agentCloud.posts != 0 ||
				kbCloud.posts != 1 || kbCloud.patches != 1 {
				t.Fatal("unverified attachment submitted an agent or mutated/indexed the KB")
			}
		})
	}
}

func TestAgentKBAttachmentReadbackAndExportRecovery(t *testing.T) {
	for _, scenario := range []string{"export", "readback"} {
		t.Run(scenario, func(t *testing.T) {
			provider, _, kbCloud, agentCloud, sc := kbAgentFixture(t, nil)
			if _, err := deployKnowledgeBase(t, provider); err != nil {
				t.Fatal(err)
			}
			client := agentCloud.client()
			base := client.HTTP.Transport
			omitAttachment := scenario == "readback"
			client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(request)
				if err == nil && omitAttachment && request.URL.Host != "management.azure.com" && agentCloud.agent != nil {
					agentCloud.agent.KnowledgeBases = nil
					if err := response.Body.Close(); err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(agentCloud.agent)
					if err != nil {
						t.Fatal(err)
					}
					response.Body = io.NopCloser(strings.NewReader(string(encoded)))
				}
				return response, err
			})
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
			if scenario == "export" {
				provider.deps.Export = func(context.Context, string, string) error { return errors.New("offline export failure") }
			}
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil || agentCloud.posts != 1 {
				t.Fatal("unverified attachment/export failure was reported successful")
			}
			if scenario == "readback" {
				if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil || agentCloud.posts != 1 {
					t.Fatal("mismatching pending agent submission created a duplicate version")
				}
				omitAttachment = false
				agentCloud.agent.KnowledgeBases = []discovery.KnowledgeBaseReference{{KnowledgeBaseID: kbCloud.remote.ID}}
			}
			provider.deps.Export = func(context.Context, string, string) error { return nil }
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil || agentCloud.posts != 1 || kbCloud.posts != 1 {
				t.Fatalf("attachment/export restoration did not recover without duplicate mutations: %v", err)
			}
		})
	}
}

func TestMultipleAgentKBAttachmentsUseTheirConfiguredSubscriptionClients(t *testing.T) {
	provider, project, first, agentCloud, sc := kbAgentFixture(t, nil)
	_, secondProject, second := knowledgeBaseFixture(t, nil)
	second.kb.SubscriptionID = otherSubscription
	second.remote.ID = "second-service-returned-id"
	secondService := secondProject.Services["literature"]
	secondService.Name, secondService.Project = "other-literature", project.Root
	project.Services[secondService.Name] = secondService
	project.Services["researcher"].Agent.KnowledgeBases = append(project.Services["researcher"].Agent.KnowledgeBases,
		config.KnowledgeBaseBinding{Service: secondService.Name})
	project.Services["researcher"].Uses = append(project.Services["researcher"].Uses, secondService.Name)
	subscriptions := []string{}
	provider.deps.CloudInSubscription = func(ctx context.Context, subscription string) (*discovery.Client, error) {
		subscriptions = append(subscriptions, subscription)
		client := discovery.NewClient(token{})
		switch subscription {
		case authSubscription:
			client.HTTP.Transport = roundTrip(first.request)
		case otherSubscription:
			client.HTTP.Transport = roundTrip(second.request)
		default:
			t.Fatal("KB credentials fell back to an unrelated subscription")
		}
		return client, nil
	}
	for _, name := range []string{"literature", secondService.Name} {
		if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: name, Host: config.KnowledgeBaseHost}, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil || len(agentCloud.agent.KnowledgeBases) != 2 ||
		len(subscriptions) != 4 || subscriptions[2] != authSubscription || subscriptions[3] != otherSubscription {
		t.Fatalf("multiple cross-scope attachments were not resolved with their own credentials: %v", err)
	}
}
