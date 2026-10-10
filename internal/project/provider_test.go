package project

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
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

const testScope = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/demo/providers/Microsoft.Discovery"
const testWorkspace = testScope + "/workspaces/demo"
const testDigest = "example.azurecr.io/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeContainers struct {
	builds, packages, publishes int
	failure                     error
}

func localArtifact(location string) *azdext.Artifact {
	return &azdext.Artifact{Kind: azdext.ArtifactKind_ARTIFACT_KIND_CONTAINER,
		LocationKind: azdext.LocationKind_LOCATION_KIND_LOCAL, Location: location,
		Metadata: map[string]string{"imageHash": "sha256:" + strings.Repeat("a", 64)}}
}

func (f *fakeContainers) Build(_ context.Context, request *azdext.ContainerBuildRequest) (*azdext.ContainerBuildResponse, error) {
	f.builds++
	return &azdext.ContainerBuildResponse{Result: &azdext.ServiceBuildResult{Artifacts: []*azdext.Artifact{localArtifact("sha256:local")}}}, f.failure
}
func (f *fakeContainers) Package(_ context.Context, request *azdext.ContainerPackageRequest) (*azdext.ContainerPackageResponse, error) {
	f.packages++
	if len(request.ServiceContext.Build) == 0 {
		return nil, errors.New("build result was not handed off")
	}
	return &azdext.ContainerPackageResponse{Result: &azdext.ServicePackageResult{Artifacts: []*azdext.Artifact{localArtifact("demo:local")}}}, f.failure
}
func (f *fakeContainers) Publish(_ context.Context, request *azdext.ContainerPublishRequest) (*azdext.ContainerPublishResponse, error) {
	f.publishes++
	if len(request.ServiceContext.Package) == 0 {
		return nil, errors.New("package result was not handed off")
	}
	return &azdext.ContainerPublishResponse{Result: &azdext.ServicePublishResult{Artifacts: []*azdext.Artifact{imageArtifact("example.azurecr.io/demo:published")}}}, f.failure
}

type token struct{}

func (token) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline-fixture", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func fixture(t *testing.T, mode string) (*config.Project, *Provider, *fakeContainers) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".azure", "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	tool := &config.Service{Name: "simulation", Host: config.ToolHost, Project: root,
		Tool: &config.Tool{Definition: "tool.yaml", Image: config.Image{Mode: mode, InfraNode: "worker", Reference: testDigest}},
		Definition: map[string]any{"name": "demo-tool", "version": "1.0.0",
			"infra": []any{
				map[string]any{"name": "worker", "image": map[string]any{"acr": "original:tag"}},
				map[string]any{"name": "other", "image": map[string]any{"acr": "untouched:tag"}},
			}, "command": "run {{scriptName}}"},
	}
	if mode == "build" {
		tool.Tool.Image.Reference = ""
	}
	project := &config.Project{Name: "demo", Root: root, Discovery: config.Settings{
		SchemaVersion: "1.0", WorkspaceResourceID: testWorkspace, ProjectResourceID: testWorkspace + "/projects/research",
		NodePoolResourceID: testScope + "/supercomputers/cpu/nodePools/worker", OperationTimeoutSeconds: 5,
	}, Services: map[string]*config.Service{"simulation": tool}}
	containers := &fakeContainers{}
	return project, New(Dependencies{
		Load:        func(context.Context, bool) (*config.Project, error) { return project, nil },
		Container:   containers,
		Digest:      func(context.Context, string, string) (string, error) { return testDigest, nil },
		Environment: func(context.Context) (string, error) { return "demo", nil },
		Export:      func(context.Context, string, string) error { return nil },
	}), containers
}

func TestImageLifecycle(t *testing.T) {
	for _, mode := range []string{"reference", "build"} {
		t.Run(mode, func(t *testing.T) {
			project, provider, containers := fixture(t, mode)
			service := &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}
			packaged, err := provider.Package(t.Context(), service, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			published, err := provider.Publish(t.Context(), service, &azdext.ServiceContext{Package: packaged.Artifacts}, nil, nil, nil)
			if err != nil || len(published.Artifacts) != 1 || published.Artifacts[0].Location != testDigest {
				t.Fatalf("immutable publication: %v, %v", published, err)
			}
			expected := 0
			if mode == "build" {
				expected = 1
			}
			if containers.builds != expected || containers.packages != expected || containers.publishes != expected {
				t.Fatalf("unexpected native lifecycle calls: %+v", containers)
			}
			if mode == "build" {
				project.Services["simulation"].Definition["version"] = "2.0.0"
				if _, err := provider.Publish(t.Context(), service, &azdext.ServiceContext{Package: packaged.Artifacts}, nil, nil, nil); err == nil {
					t.Fatal("stale package accepted")
				}
			}
		})
	}
}

func TestFrameworkArtifactReuseAndFailures(t *testing.T) {
	_, provider, containers := fixture(t, "build")
	service := &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}
	input := &azdext.ServiceContext{Package: []*azdext.Artifact{localArtifact("core:package")}}
	packaged, err := provider.Package(t.Context(), service, input, nil)
	if err != nil || containers.builds != 0 || containers.packages != 0 || len(packaged.Artifacts) != 1 {
		t.Fatalf("core artifact was not reused: %v %v %+v", packaged, err, containers)
	}
	containers.failure = errors.New("publication fixture failure")
	combined := &azdext.ServiceContext{Package: append(input.Package, packaged.Artifacts...)}
	if _, err := provider.Publish(t.Context(), service, combined, nil, nil, nil); err == nil || containers.publishes != 1 {
		t.Fatal("publication failure hidden")
	}
	if _, err := provider.Publish(t.Context(), service, input, nil, &azdext.PublishOptions{Image: "override:tag"}, nil); err == nil {
		t.Fatal("unsupported --to silently ignored")
	}
	if _, err := provider.Publish(t.Context(), service, nil, nil, nil, nil); err == nil {
		t.Fatal("missing package accepted")
	}
}

type fakeCloud struct {
	t                              *testing.T
	project                        *config.Project
	definition                     map[string]any
	agent                          *discovery.Agent
	puts, posts, toolReads         int
	failReady, mismatch, ambiguous bool
	modelFailure                   string
	modelReads                     []string
}

func (f *fakeCloud) client() *discovery.Client {
	client := discovery.NewClient(token{})
	client.Sleep = func(ctx context.Context, delay time.Duration) error { return ctx.Err() }
	client.HTTP.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer offline-fixture" {
			f.t.Fatal("SDK token was not used")
		}
		status := 200
		var body any
		if request.URL.Host == "management.azure.com" {
			id := request.URL.Path
			resource := &discovery.Resource{ID: id, Name: "demo", Location: "westus3"}
			resource.Properties.ProvisioningState = "Succeeded"
			switch id {
			case testWorkspace + "/chatModelDeployments/chat", testWorkspace + "/chatModelDeployments/fresh":
				resource.Name = id[strings.LastIndex(id, "/")+1:]
				f.modelReads = append(f.modelReads, resource.Name)
				switch f.modelFailure {
				case "missing":
					status = 404
				case "identity":
					resource.Name = "wrong"
				case "readiness":
					resource.Properties.ProvisioningState = "Failed"
				}
			case testScope + "/tools/demo-tool":
				resource.Name = "demo-tool"
				f.toolReads++
				if request.Method == http.MethodPut {
					f.puts++
					var payload struct {
						Properties struct {
							Definition map[string]any `json:"definitionContent"`
						} `json:"properties"`
					}
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						f.t.Fatal(err)
					}
					f.definition = payload.Properties.Definition
					if f.definition["infra"].([]any)[1].(map[string]any)["image"].(map[string]any)["acr"] != "untouched:tag" {
						f.t.Fatal("unrelated infra image changed")
					}
				} else if f.definition == nil {
					status = 404
				}
				resource.Properties.Definition = f.definition
				resource.Properties.Version = "1.0.0"
				if f.failReady {
					resource.Properties.ProvisioningState = "Failed"
				}
				if f.mismatch {
					resource.Properties.Version = "unexpected"
				}
				if f.toolReads == 3 {
					resource.Properties.ProvisioningState = "Creating"
				}
			}
			body = resource
		} else {
			if request.Method == http.MethodPost {
				f.posts++
				var payload discovery.AgentPayload
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					f.t.Fatal(err)
				}
				f.agent = &discovery.Agent{AgentPayload: payload, ProvisioningState: "Succeeded"}
				f.agent.FoundryDetails.Versions = &discovery.AgentVersions{}
				f.agent.FoundryDetails.Versions.Latest.Version = "1"
				f.agent.FoundryDetails.Versions.Latest.Description = &payload.FoundryDetails.Description
				f.agent.FoundryDetails.Versions.Latest.Definition = payload.FoundryDetails.Definition
				if f.ambiguous {
					return nil, errors.New("lost POST response")
				}
			}
			body = f.agent
			if f.agent == nil {
				status = 404
			}
		}
		data, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(data))), Request: request}, nil
	})
	return client
}

func TestToolDeployReadinessAndVerifiedExports(t *testing.T) {
	for _, mode := range []string{"reference", "build"} {
		t.Run(mode, func(t *testing.T) {
			project, provider, _ := fixture(t, mode)
			cloud := &fakeCloud{t: t, project: project}
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return cloud.client(), nil }
			exported := map[string]string{}
			provider.deps.Export = func(_ context.Context, key, value string) error { exported[key] = value; return nil }
			service := &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}
			packaged, err := provider.Package(t.Context(), service, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			published, err := provider.Publish(t.Context(), service, &azdext.ServiceContext{Package: packaged.Artifacts}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			input := &azdext.ServiceContext{Publish: published.Artifacts}
			for range 2 {
				if _, err := provider.Deploy(t.Context(), service, input, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if cloud.puts != 1 || cloud.toolReads < 4 || exported["SERVICE_SIMULATION_IMAGE"] != testDigest ||
				exported["SERVICE_SIMULATION_RESOURCE_ID"] != testScope+"/tools/demo-tool" {
				t.Fatalf("readiness/reconciliation/exports failed: %+v %+v", cloud, exported)
			}
		})
	}
}

func TestToolFailuresPreventCompletion(t *testing.T) {
	for _, failure := range []string{"registry", "readiness", "readback", "mutable", "missingPublish"} {
		t.Run(failure, func(t *testing.T) {
			project, provider, _ := fixture(t, "build")
			cloud := &fakeCloud{t: t, project: project, failReady: failure == "readiness", mismatch: failure == "readback"}
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return cloud.client(), nil }
			provider.deps.Export = func(context.Context, string, string) error { t.Fatal("failure exported success"); return nil }
			fingerprint, err := sourceHash(project.Services["simulation"])
			if err != nil {
				t.Fatal(err)
			}
			artifact := imageArtifact(testDigest)
			artifact.Metadata["discoverySource"] = fingerprint
			if failure == "mutable" {
				artifact.Location = "example.azurecr.io/demo:tag"
			}
			if failure == "registry" {
				provider.deps.Digest = func(context.Context, string, string) (string, error) { return "", errors.New("registry denied") }
			}
			input := &azdext.ServiceContext{Publish: []*azdext.Artifact{artifact}}
			if failure == "missingPublish" {
				input = nil
			}
			if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}, input, nil, nil); err == nil {
				t.Fatal("failure hidden")
			}
			if (failure == "registry" || failure == "mutable" || failure == "missingPublish") && cloud.puts != 0 {
				t.Fatal("invalid publication registered a tool")
			}
		})
	}
}

func TestAgentLateBindingsAndReconciliation(t *testing.T) {
	project, provider, _ := fixture(t, "reference")
	project.Services["researcher"] = &config.Service{Name: "researcher", Host: config.AgentHost, Uses: []string{"simulation"},
		Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "chat"}, Bindings: map[string]config.Binding{
			"toolId": {Service: "simulation", Output: "resourceId"},
		}}, Definition: map[string]any{"name": "researcher", "kind": "prompt", "model": map[string]any{"id": "{{CHAT-MODEL}}"},
			"instructions": "Keep {{scriptName}} and ${LITERAL}.", "discoveryExtensions": map[string]any{
				"humanInTheLoop": true, "tools": []any{map[string]any{"toolId": "{{toolId}}"}}},
		}}
	agentSC := &azdext.ServiceConfig{Name: "researcher", Host: config.AgentHost}
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) {
		t.Fatal("Package requested cloud outputs")
		return nil, nil
	}

	if _, err := provider.Package(t.Context(), agentSC, nil, nil); err != nil {
		t.Fatal(err)
	}
	cloud := &fakeCloud{t: t, project: project, ambiguous: true}
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return cloud.client(), nil }
	if _, err := provider.Deploy(t.Context(), agentSC, nil, nil, nil); err == nil || cloud.posts != 0 {
		t.Fatal("agent without deployed tool dependencies submitted")
	}
	if _, err := provider.Deploy(t.Context(), &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := provider.Deploy(t.Context(), agentSC, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if cloud.posts != 1 || cloud.agent.FoundryDetails.Definition["model"] != "chat" ||
		cloud.agent.FoundryDetails.Definition["instructions"] != "Keep {{scriptName}} and ${LITERAL}." {
		t.Fatal("late binding, ambiguous POST reconciliation, or unchanged rerun failed")
	}
	project.Services["simulation"].Definition["version"] = "2.0.0"
	if _, err := provider.Deploy(t.Context(), agentSC, nil, nil, nil); err == nil || cloud.posts != 1 {
		t.Fatal("stale dependency deployed an agent")
	}
}

func TestVerifiedExportFailureRestoresWithoutMutation(t *testing.T) {
	project, provider, _ := fixture(t, "reference")
	cloud := &fakeCloud{t: t, project: project}
	provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return cloud.client(), nil }
	provider.deps.Export = func(context.Context, string, string) error { return errors.New("environment unavailable") }
	service := &azdext.ServiceConfig{Name: "simulation", Host: config.ToolHost}
	if _, err := provider.Deploy(t.Context(), service, nil, nil, nil); err == nil {
		t.Fatal("failed export reported success")
	}
	store, err := state.Open(project.Root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Read("simulation", project.Discovery.ProjectResourceID)
	if err != nil || record == nil || record.Phase != "verified" {
		t.Fatalf("completion written before exports: %v %v", record, err)
	}
	provider.deps.Export = func(context.Context, string, string) error { return nil }
	if _, err := provider.Deploy(t.Context(), service, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if cloud.puts != 1 {
		t.Fatal("export restoration re-registered unchanged tool")
	}
	record, err = store.Read("simulation", project.Discovery.ProjectResourceID)
	if err != nil || record.Phase != "complete" {
		t.Fatal("restored deployment not marked complete")
	}
}

func TestAgentDirectModelVerification(t *testing.T) {
	for _, mode := range []string{"configured", "literal", "missing", "identity", "readiness", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			project, provider, containers := fixture(t, "reference")
			agent := &config.Service{Name: "researcher", Host: config.AgentHost,
				Agent: &config.Agent{Model: &config.AgentModel{DeploymentName: "${MODEL_NAME}"}},
				Definition: map[string]any{"name": "researcher", "kind": "prompt",
					"instructions": "Preserve {{runtime}}.", "model": map[string]any{"id": "chat"}},
			}
			project.Services[agent.Name] = agent
			sc := &azdext.ServiceConfig{Name: agent.Name, Host: agent.Host}
			loads := []bool{}
			provider.deps.Load = func(_ context.Context, packageOnly bool) (*config.Project, error) {
				loads = append(loads, packageOnly)
				return project, nil
			}
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) {
				t.Fatal("Package attempted model verification")
				return nil, nil
			}
			if _, err := provider.Package(t.Context(), sc, nil, nil); err != nil {
				t.Fatal(err)
			}
			agent.Agent.Model.DeploymentName = "fresh"
			if mode == "literal" {
				agent.Agent.Model = nil
			}
			if mode == "unresolved" {
				agent.Agent.Model.DeploymentName = "${MODEL_NAME}"
			}
			cloud := &fakeCloud{t: t, project: project, modelFailure: mode}
			provider.deps.Cloud = func(context.Context) (*discovery.Client, error) { return cloud.client(), nil }
			exported := 0
			provider.deps.Export = func(context.Context, string, string) error { exported++; return nil }
			_, err := provider.Deploy(t.Context(), sc, nil, nil, nil)
			success := mode == "configured" || mode == "literal"
			if (err == nil) != success {
				t.Fatalf("unexpected result: %v", err)
			}
			if !success && (cloud.posts != 0 || exported != 0) {
				t.Fatal("invalid/unready model submitted an agent or exported success")
			}
			if success {
				expected := "fresh"
				if mode == "literal" {
					expected = "chat"
				}
				if len(cloud.modelReads) != 1 || cloud.modelReads[0] != expected ||
					cloud.agent.FoundryDetails.Definition["model"] != expected ||
					agent.Definition["model"].(map[string]any)["id"] != "chat" || exported != 2 {
					t.Fatal("model verification, override, source preservation, or exports failed")
				}
			}
			if len(loads) < 2 || !loads[0] || loads[1] ||
				containers.builds != 0 || containers.publishes != 0 {
				t.Fatal("lifecycle did not refresh configuration or rebuilt containers")
			}
		})
	}
}
