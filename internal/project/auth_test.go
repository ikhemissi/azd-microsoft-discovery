package project

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	authSubscription  = "00000000-0000-0000-0000-000000000001"
	authTenant        = "00000000-0000-0000-0000-000000000002"
	otherSubscription = "00000000-0000-0000-0000-000000000003"
	otherTenant       = "00000000-0000-0000-0000-000000000004"
)

type authRPC struct {
	azdext.UnimplementedEnvironmentServiceServer
	azdext.UnimplementedAccountServiceServer
	azdext.UnimplementedDeploymentServiceServer

	mu                               sync.Mutex
	current                          string
	subscriptions                    map[string]string
	tenants                          map[string]string
	currentErr, valuesErr, lookupErr error
	currentCalls, deploymentCalls    int
	valueRequests, lookupRequests    []string
}

func (s *authRPC) GetCurrent(context.Context, *azdext.EmptyRequest) (*azdext.EnvironmentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentCalls++
	if s.currentErr != nil {
		return nil, s.currentErr
	}
	return &azdext.EnvironmentResponse{Environment: &azdext.Environment{Name: s.current}}, nil
}

func (s *authRPC) GetValues(_ context.Context, request *azdext.GetEnvironmentRequest) (*azdext.KeyValueListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valueRequests = append(s.valueRequests, request.Name)
	if s.valuesErr != nil {
		return nil, s.valuesErr
	}
	response := &azdext.KeyValueListResponse{}
	if subscription, exists := s.subscriptions[request.Name]; exists {
		response.KeyValues = []*azdext.KeyValue{{Key: "AZURE_SUBSCRIPTION_ID", Value: subscription}}
	}
	return response, nil
}

func (s *authRPC) LookupTenant(_ context.Context, request *azdext.LookupTenantRequest) (*azdext.LookupTenantResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookupRequests = append(s.lookupRequests, request.SubscriptionId)
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return &azdext.LookupTenantResponse{TenantId: s.tenants[request.SubscriptionId]}, nil
}

func (s *authRPC) GetDeploymentContext(context.Context, *azdext.EmptyRequest) (*azdext.GetDeploymentContextResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deploymentCalls++
	return nil, status.Error(codes.FailedPrecondition, "deployment-context lookup must not run in this Terraform fixture")
}

func authClient(t *testing.T, backend *authRPC) (*azdext.AzdClient, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AZD_EXEC_PROJECT_DIR", root)
	if err := os.Mkdir(filepath.Join(root, "infra"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"azure.yaml":    "name: terraform-auth\ninfra:\n  provider: terraform\n  path: ./infra\n",
		"infra/main.tf": "terraform { required_version = \">= 1.0\" }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	azdext.RegisterEnvironmentServiceServer(server, backend)
	azdext.RegisterAccountServiceServer(server, backend)
	azdext.RegisterDeploymentServiceServer(server, backend)
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-finished; err != nil {
			t.Errorf("auth fixture server: %v", err)
		}
	})
	client, err := azdext.NewAzdClient(azdext.WithAddress(listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, root
}

func TestTerraformCloudAndDigestCredentialsAvoidDeploymentContext(t *testing.T) {
	t.Setenv("AZD_ENVIRONMENT", "terraform")
	t.Setenv("AZURE_SUBSCRIPTION_ID", otherSubscription)
	t.Setenv("AZURE_TENANT_ID", otherTenant)
	backend := &authRPC{
		current:       "default",
		subscriptions: map[string]string{"terraform": authSubscription, "default": otherSubscription},
		tenants:       map[string]string{authSubscription: authTenant, otherSubscription: otherTenant},
	}
	client, root := authClient(t, backend)
	host := azdext.NewExtensionHost(client)
	Register(host)
	for _, registration := range host.ServiceTargets() {
		t.Run(registration.Host, func(t *testing.T) {
			provider, ok := registration.Factory().(*Provider)
			if !ok {
				t.Fatal("unexpected provider factory")
			}
			cloud, err := provider.deps.Cloud(t.Context())
			if err != nil {
				t.Fatalf("Terraform Cloud credential construction: %v", err)
			}
			credential, ok := cloud.Credential.(*azdext.TokenProvider)
			if !ok || credential.TenantID() != authTenant {
				t.Fatal("Cloud did not preserve the SDK credential or selected subscription tenant")
			}
			// Invalid artifacts stop before registry I/O but after credential
			// construction, exercising both normal Digest entry paths offline.
			for _, input := range []struct{ image, hash, message string }{
				{"invalid image", "", "published image artifact"},
				{"example.azurecr.io/tool:local", "invalid", "native package artifact"},
			} {
				if _, err := provider.deps.Digest(t.Context(), input.image, input.hash); err == nil ||
					!strings.Contains(err.Error(), input.message) {
					t.Fatalf("Digest did not reach offline artifact validation: %v", err)
				}
			}
		})
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.currentCalls != 0 || backend.deploymentCalls != 0 || len(backend.lookupRequests) != 6 {
		t.Fatalf("unexpected credential RPC calls: current=%d deployment=%d lookups=%v",
			backend.currentCalls, backend.deploymentCalls, backend.lookupRequests)
	}
	for _, name := range backend.valueRequests {
		if name != "terraform" {
			t.Fatalf("wrong effective environment: %s", name)
		}
	}
	for _, subscription := range backend.lookupRequests {
		if subscription != authSubscription {
			t.Fatal("credential lookup used a default/process subscription instead of the selected environment")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "infra", "main.bicep")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Terraform credential fixture unexpectedly needs a Bicep file: %v", err)
	}
}

func TestTokenProviderEnvironmentValidationAndErrors(t *testing.T) {
	for _, test := range []struct {
		name, current, subscription, tenant, expected string
		currentErr, valuesErr, lookupErr              error
		lookups                                       int
	}{
		{name: "current environment", current: "selected", subscription: authSubscription, tenant: authTenant, lookups: 1},
		{name: "no current environment", expected: "no current environment"},
		{name: "current lookup failure", currentErr: status.Error(codes.NotFound, "missing environment"), expected: "resolve current azd environment"},
		{name: "environment read failure", current: "selected", valuesErr: status.Error(codes.Unavailable, "offline failure"), expected: "read subscription selection"},
		{name: "missing subscription", current: "selected", tenant: authTenant, expected: "valid AZURE_SUBSCRIPTION_ID"},
		{name: "invalid subscription", current: "selected", subscription: "not-an-id", tenant: authTenant, expected: "valid AZURE_SUBSCRIPTION_ID"},
		{name: "zero subscription", current: "selected", subscription: "00000000-0000-0000-0000-000000000000", tenant: authTenant, expected: "valid AZURE_SUBSCRIPTION_ID"},
		{name: "subscription alias", current: "selected", subscription: "display-name", tenant: authTenant, expected: "valid AZURE_SUBSCRIPTION_ID"},
		{name: "tenant lookup failure", current: "selected", subscription: authSubscription, lookupErr: status.Error(codes.PermissionDenied, "lookup denied"), expected: "resolve credential tenant", lookups: 1},
		{name: "empty tenant", current: "selected", subscription: authSubscription, expected: "invalid tenant ID", lookups: 1},
		{name: "tenant alias", current: "selected", subscription: authSubscription, tenant: "common", expected: "invalid tenant ID", lookups: 1},
		{name: "invalid tenant", current: "selected", subscription: authSubscription, tenant: "bad-id", expected: "invalid tenant ID", lookups: 1},
		{name: "zero tenant", current: "selected", subscription: authSubscription, tenant: "00000000-0000-0000-0000-000000000000", expected: "invalid tenant ID", lookups: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AZD_ENVIRONMENT", "")
			t.Setenv("AZURE_SUBSCRIPTION_ID", otherSubscription)
			backend := &authRPC{
				current:       test.current,
				subscriptions: map[string]string{test.current: test.subscription},
				tenants:       map[string]string{test.subscription: test.tenant},
				currentErr:    test.currentErr, valuesErr: test.valuesErr, lookupErr: test.lookupErr,
			}
			client, _ := authClient(t, backend)
			credential, err := newTokenProvider(t.Context(), client)
			if test.expected == "" {
				if err != nil || credential.TenantID() != test.tenant {
					t.Fatalf("current environment tenant resolution failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.expected) {
				t.Fatalf("expected %q, got %v", test.expected, err)
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.currentCalls != 1 || backend.deploymentCalls != 0 ||
				len(backend.lookupRequests) != test.lookups {
				t.Fatalf("unexpected fallback/RPC calls: current=%d deployment=%d lookups=%v",
					backend.currentCalls, backend.deploymentCalls, backend.lookupRequests)
			}
			if len(backend.valueRequests) > 0 && backend.valueRequests[0] != test.current {
				t.Fatal("environment value lookup did not use current environment name")
			}
		})
	}
}

func TestTokenProviderRefreshesSubscriptionSelection(t *testing.T) {
	t.Setenv("AZD_ENVIRONMENT", "terraform")
	backend := &authRPC{
		subscriptions: map[string]string{"terraform": authSubscription},
		tenants:       map[string]string{authSubscription: authTenant, otherSubscription: otherTenant},
	}
	client, _ := authClient(t, backend)
	first, err := newTokenProvider(t.Context(), client)
	if err != nil || first.TenantID() != authTenant {
		t.Fatalf("initial tenant lookup: %v", err)
	}
	backend.mu.Lock()
	backend.subscriptions["terraform"] = otherSubscription
	backend.mu.Unlock()
	second, err := newTokenProvider(t.Context(), client)
	if err != nil || second.TenantID() != otherTenant {
		t.Fatalf("updated subscription selection was not refreshed: %v", err)
	}
}
