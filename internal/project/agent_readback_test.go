package project

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

func TestMatchingAgentWithoutDescriptionDoesNotCreateVersion(t *testing.T) {
	for _, phase := range []string{"", "submitting", "polling", "verified"} {
		t.Run("prior-state-"+phase, func(t *testing.T) {
			project, provider, cloud, sc := agentFixture(t)
			client := cloud.client()
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
			if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			agent := project.Services[sc.Name]
			agent.Uses = []string{"simulation"}
			agent.Agent.Bindings = map[string]config.Binding{"toolId": {Service: "simulation", Output: "resourceId"}}
			agent.Definition["description"] = "Offline contract fixture"
			agent.Definition["instructions"] = "Keep {{runtime}} and ${LITERAL}."
			agent.Definition["discoveryExtensions"] = map[string]any{
				"humanInTheLoop": "Enabled",
				"tools":          []any{map[string]any{"toolId": "{{toolId}}", "confirmation": "Enabled"}},
			}
			store, err := state.Open(project.Root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if phase != "" {
				definition, err := discovery.AgentDefinition(agent, map[string]map[string]string{
					"simulation": {"resourceId": testScope + "/tools/demo-tool"},
				})
				if err != nil {
					t.Fatal(err)
				}
				payload, err := discovery.Payload(definition)
				if err != nil {
					t.Fatal(err)
				}
				desired, err := state.Hash(payload)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Write(&state.Record{
					SchemaVersion: 1, Scope: project.Discovery.ProjectResourceID, Service: sc.Name,
					Desired: desired, Phase: phase,
				}); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile("../discovery/testdata/agent-readback-description-omitted.json")
			if err != nil {
				t.Fatal(err)
			}
			base := client.HTTP.Transport
			reads, posts := 0, 0
			client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host == "demo.workspace.discovery.azure.com" {
					if request.Method == http.MethodPost {
						posts++
						return httpResponse(request, 409, `{}`, nil), nil
					}
					reads++
					return httpResponse(request, 200, string(data), nil), nil
				}
				return base.RoundTrip(request)
			})
			exported := map[string]string{}
			provider.deps.Export = func(_ context.Context, key, value string) error {
				exported[key] = value
				return nil
			}
			for range 2 {
				if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil {
					t.Fatalf("matching existing agent failed reconciliation: %v", err)
				}
			}
			if posts != 0 || reads != 2 || exported["SERVICE_RESEARCHER_NAME"] != "researcher" ||
				exported["SERVICE_RESEARCHER_VERSION"] != "1" {
				t.Fatalf("unexpected reconciliation: posts=%d reads=%d exports=%v", posts, reads, exported)
			}
			record, err := store.Read(sc.Name, project.Discovery.ProjectResourceID)
			if err != nil || record.Phase != "complete" || record.Outputs["version"] != "1" {
				t.Fatalf("matching agent was not recorded complete: %+v %v", record, err)
			}
		})
	}
}
