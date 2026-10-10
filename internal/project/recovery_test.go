package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

func agentFixture(t *testing.T) (*config.Project, *Provider, *fakeCloud, *azdext.ServiceConfig) {
	t.Helper()
	project, provider, _ := fixture(t, "reference")
	project.Services["researcher"] = &config.Service{
		Name: "researcher", Host: config.AgentHost,
		Agent:      &config.Agent{Model: &config.AgentModel{DeploymentName: "chat"}},
		Definition: map[string]any{"name": "researcher", "kind": "prompt", "instructions": "Keep unchanged."},
	}
	cloud := &fakeCloud{t: t, project: project}
	return project, provider, cloud, &azdext.ServiceConfig{Name: "researcher", Host: config.AgentHost}
}

func httpResponse(request *http.Request, status int, body string, header http.Header) *http.Response {
	return &http.Response{Request: request, StatusCode: status, Header: header,
		Body: io.NopCloser(strings.NewReader(body))}
}

func TestAgentRejectedVersusAmbiguousSubmission(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 422, 408, 409, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			project, provider, cloud, sc := agentFixture(t)
			client := cloud.client()
			base := client.HTTP.Transport
			fail := true
			client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodPost && fail {
					return httpResponse(request, status, `{"error":{"code":"Rejected"}}`, nil), nil
				}
				return base.RoundTrip(request)
			})
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err == nil {
				t.Fatal("initial failure reported success")
			}
			store, err := state.Open(project.Root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Read(sc.Name, project.Discovery.ProjectResourceID)
			if err != nil {
				t.Fatal(err)
			}
			rejected := (&discovery.HTTPError{Status: status}).Rejected()
			if (record.Phase == "failed") != rejected {
				t.Fatalf("wrong failure classification: %+v", record)
			}
			fail = false
			_, err = provider.Deploy(t.Context(), sc, nil, nil, nil)
			if rejected && (err != nil || cloud.posts != 1) {
				t.Fatalf("definitive rejection could not recover: %v", err)
			}
			if !rejected && (err == nil || cloud.posts != 0) {
				t.Fatal("ambiguous submission was retried")
			}
		})
	}
}

func TestTerminalOperationFailureRecovery(t *testing.T) {
	for _, host := range []string{config.ToolHost, config.AgentHost} {
		t.Run(host, func(t *testing.T) {
			project, provider, cloud, agentSC := agentFixture(t)
			sc := agentSC
			if host == config.ToolHost {
				sc = &azdext.ServiceConfig{Name: "simulation", Host: host}
			}
			client := cloud.client()
			base := client.HTTP.Transport
			fail := true
			client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/operations/failed" {
					return httpResponse(request, 200, `{"status":"Failed"}`, nil), nil
				}
				if fail && (request.Method == http.MethodPut || request.Method == http.MethodPost) {
					return httpResponse(request, 202, `{}`,
						http.Header{"Operation-Location": []string{discovery.ARMEndpoint + "/operations/failed"}}), nil
				}
				return base.RoundTrip(request)
			})
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); !terminalFailure(err) {
				t.Fatalf("expected terminal failure: %v", err)
			}
			// Resume an older pending record too, even if desired configuration changed.
			store, err := state.Open(project.Root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Read(sc.Name, project.Discovery.ProjectResourceID)
			if err != nil || record.Phase != "failed" {
				t.Fatalf("terminal failure not persisted: %+v %v", record, err)
			}
			record.Phase, record.Operation = "polling", discovery.ARMEndpoint+"/operations/failed"
			if err := store.Write(record); err != nil {
				t.Fatal(err)
			}
			project.Services[sc.Name].Definition["description"] = "corrected"
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); !terminalFailure(err) {
				t.Fatalf("pending failure was not reconciled first: %v", err)
			}
			fail = false
			if _, err := provider.Deploy(t.Context(), sc, nil, nil, nil); err != nil {
				t.Fatalf("corrected configuration could not recover: %v", err)
			}
		})
	}
}

func TestFailedExistingToolCanBeRepaired(t *testing.T) {
	project, provider, _ := fixture(t, "reference")
	definition, err := discovery.ToolDefinition(project.Services["simulation"], testDigest)
	if err != nil {
		t.Fatal(err)
	}
	cloud := &fakeCloud{t: t, project: project, definition: definition, failReady: true}
	client := cloud.client()
	base := client.HTTP.Transport
	client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPut {
			cloud.failReady = false
		}
		return base.RoundTrip(request)
	})
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
	if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if cloud.puts != 1 {
		t.Fatal("failed tool was not repaired")
	}
}

func TestLiteralToolReadinessBeforeAgentSubmission(t *testing.T) {
	for _, status := range []string{"Succeeded", "Failed", "missing"} {
		t.Run(status, func(t *testing.T) {
			project, provider, cloud, sc := agentFixture(t)
			id := testScope + "/tools/existing"
			project.Services[sc.Name].Definition["discoveryExtensions"] = map[string]any{
				"tools": []any{map[string]any{"toolId": id}},
			}
			client := cloud.client()
			base := client.HTTP.Transport
			reads := 0
			client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == id {
					reads++
					code := 200
					if status == "missing" {
						code = 404
					}
					data, err := json.Marshal(map[string]any{"id": id, "properties": map[string]any{"provisioningState": status}})
					if err != nil {
						return nil, err
					}
					return httpResponse(request, code, string(data), nil), nil
				}
				return base.RoundTrip(request)
			})
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return client, nil }
			_, err := provider.Deploy(t.Context(), sc, nil, nil, nil)
			if reads != 1 || (err == nil) != (status == "Succeeded") ||
				(status != "Succeeded" && cloud.posts != 0) {
				t.Fatalf("literal verification failed: reads=%d posts=%d err=%v", reads, cloud.posts, err)
			}
		})
	}
}
