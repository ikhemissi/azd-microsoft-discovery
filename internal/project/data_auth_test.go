package project

import "testing"

func TestDataCredentialUsesExplicitSelectedEnvironmentWithoutBicep(t *testing.T) {
	t.Setenv("AZD_ENVIRONMENT", "process")
	backend := &authRPC{
		current:       "current",
		subscriptions: map[string]string{"selected": authSubscription, "process": otherSubscription},
		tenants:       map[string]string{authSubscription: authTenant, otherSubscription: otherTenant},
	}
	client, _ := authClient(t, backend)
	environment, err := ResolveEnvironment(t.Context(), client, "selected")
	if err != nil || environment != "selected" {
		t.Fatal("explicit data environment selection was lost")
	}
	credential, err := CredentialForEnvironment(t.Context(), client, environment)
	if err != nil || credential.TenantID() != authTenant {
		t.Fatalf("data credential did not use the selected tenant: %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.deploymentCalls != 0 || backend.currentCalls != 0 ||
		len(backend.valueRequests) != 1 || backend.valueRequests[0] != "selected" ||
		len(backend.lookupRequests) != 1 || backend.lookupRequests[0] != authSubscription {
		t.Fatal("data credential consulted Bicep, the process environment or the wrong subscription")
	}
}
