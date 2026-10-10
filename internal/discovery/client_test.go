package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

type credential struct{}

func (credential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) Do(request *http.Request) (*http.Response, error)        { return f(request) }
func (f transport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(request *http.Request, status int, body string, headers http.Header) *http.Response {
	return &http.Response{Request: request, StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRegistryDigestResolution(t *testing.T) {
	const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`
	sum := sha256.Sum256([]byte(manifest))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, scenario := range []string{"tag", "digest", "built", "wrongBuild", "headerMismatch", "readbackMismatch", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			registry := &Registry{Credential: credential{}, Transport: transport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Host != "example.azurecr.io" {
					t.Fatal("registry destination changed")
				}
				header := http.Header{"Content-Type": []string{"application/vnd.oci.image.manifest.v1+json"}, "Docker-Content-Digest": []string{digest}}
				body, status := manifest, 200
				if scenario == "headerMismatch" || (scenario == "readbackMismatch" && calls == 2) {
					header.Set("Docker-Content-Digest", "sha256:"+strings.Repeat("b", 64))
				}
				if scenario == "denied" {
					status = 403
					body = `{"errors":[{"code":"DENIED","message":"fixture"}]}`
				}
				if calls == 2 && !strings.HasSuffix(request.URL.Path, "/"+digest) {
					t.Fatal("immutable readback used mutable selector")
				}
				return response(request, status, body, header), nil
			})}
			image := "example.azurecr.io/demo:published"
			if scenario == "digest" {
				image = "example.azurecr.io/demo@" + digest
			}
			actual, err := registry.Resolve(t.Context(), image)
			if scenario == "built" || scenario == "wrongBuild" {
				calls = 0
				hash := "sha256:" + strings.Repeat("a", 64)
				if scenario == "wrongBuild" {
					hash = "sha256:" + strings.Repeat("b", 64)
				}
				actual, err = registry.ResolveBuilt(t.Context(), image, hash)
			}
			success := scenario == "tag" || scenario == "digest" || scenario == "built"
			if (err == nil) != success {
				t.Fatalf("unexpected digest result: %s %v", actual, err)
			}
			if success && (actual != "example.azurecr.io/demo@"+digest || calls != 2) {
				t.Fatal("unverified immutable result")
			}
		})
	}
}

func TestReadinessAndOperationContracts(t *testing.T) {
	client := NewClient(credential{})
	client.WorkspaceEndpoint = "https://demo.workspace.discovery.azure.com"
	for _, address := range []string{"http://management.azure.com", "https://evil.invalid", "https://management.azure.com:443", "https://user@management.azure.com"} {
		if _, err := client.ValidateURL(address); err == nil {
			t.Fatal("untrusted operation destination accepted")
		}
	}
	requests, slept := 0, time.Duration(0)
	client.Sleep = func(ctx context.Context, delay time.Duration) error { slept += delay; return ctx.Err() }
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		requests++
		body := `{"status":"Running"}`
		if requests > 1 {
			body = `{"status":"Succeeded"}`
		}
		return response(request, 200, body, http.Header{"Retry-After": []string{"7"}}), nil
	})
	if err := client.Poll(t.Context(), ARMEndpoint+"/operations/fixture"); err != nil || requests != 2 || slept != 7*time.Second {
		t.Fatalf("authoritative polling failed: %v %d %s", err, requests, slept)
	}
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		return response(request, 200, `{"status":"Failed"}`, nil), nil
	})
	if err := client.Poll(t.Context(), ARMEndpoint+"/operations/fixture"); err == nil {
		t.Fatal("failed operation accepted")
	}
	if _, err := client.Operation(&Response{Status: 202, Header: http.Header{}}, ARMEndpoint); err == nil {
		t.Fatal("unverifiable accepted operation accepted")
	}
}

func TestDefinitionAdapters(t *testing.T) {
	image := "example.azurecr.io/demo@sha256:" + strings.Repeat("a", 64)
	service := &config.Service{Tool: &config.Tool{Image: config.Image{InfraNode: "worker"}}, Definition: map[string]any{
		"name": "demo", "infra": []any{
			map[string]any{"name": "worker", "image": map[string]any{"acr": "old"}},
			map[string]any{"name": "other", "image": map[string]any{"acr": "other"}},
		}, "command": "{{scriptName}}",
	}}
	definition, err := ToolDefinition(service, image)
	if err != nil {
		t.Fatal(err)
	}
	if definition["infra"].([]any)[0].(map[string]any)["image"].(map[string]any)["acr"] != image ||
		definition["infra"].([]any)[1].(map[string]any)["image"].(map[string]any)["acr"] != "other" ||
		service.Definition["infra"].([]any)[0].(map[string]any)["image"].(map[string]any)["acr"] != "old" {
		t.Fatal("targeted replacement mutated source or unrelated entries")
	}
	service.Definition["infra"].([]any)[1].(map[string]any)["name"] = "worker"
	if _, err := ToolDefinition(service, image); err == nil {
		t.Fatal("ambiguous infra accepted")
	}
	payload, err := Payload(map[string]any{"name": "agent", "kind": "prompt", "model": map[string]any{"id": "chat"}, "instructions": "unchanged"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"versions"`) || !strings.Contains(string(data), `"model":"chat"`) {
		t.Fatalf("request/readback DTO leak: %s", data)
	}
}
