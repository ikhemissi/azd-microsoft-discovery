package project

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/google/uuid"
)

func currentEnvironment(ctx context.Context, client *azdext.AzdClient) (string, error) {
	if client == nil {
		return "", errors.New("azd client is required to resolve the current environment")
	}
	if name := os.Getenv("AZD_ENVIRONMENT"); name != "" {
		return name, nil
	}
	response, err := client.Environment().GetCurrent(ctx, &azdext.EmptyRequest{})
	if err != nil {
		return "", fmt.Errorf("resolve current azd environment: %w", err)
	}
	name := response.GetEnvironment().GetName()
	if name == "" {
		return "", errors.New("azd returned no current environment; select an environment before publishing or deploying")
	}
	return name, nil
}

func validatedID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || len(value) != 36 || id == uuid.Nil {
		return "", errors.New("expected a nonzero UUID in canonical GUID format")
	}
	return id.String(), nil
}

func ResolveEnvironment(ctx context.Context, client *azdext.AzdClient, selection string) (string, error) {
	if selection != "" {
		return selection, nil
	}
	return currentEnvironment(ctx, client)
}

func CredentialForEnvironment(ctx context.Context, client *azdext.AzdClient, selection string) (*azdext.TokenProvider, error) {
	environment, err := ResolveEnvironment(ctx, client, selection)
	if err != nil {
		return nil, err
	}
	return tokenProviderForEnvironment(ctx, client, environment)
}

func newTokenProvider(ctx context.Context, client *azdext.AzdClient) (*azdext.TokenProvider, error) {
	environment, err := currentEnvironment(ctx, client)
	if err != nil {
		return nil, err
	}
	return tokenProviderForEnvironment(ctx, client, environment)
}

func tokenProviderForEnvironment(ctx context.Context, client *azdext.AzdClient, environment string) (*azdext.TokenProvider, error) {
	values, err := client.Environment().GetValues(ctx, &azdext.GetEnvironmentRequest{Name: environment})
	if err != nil {
		return nil, fmt.Errorf("read subscription selection for azd environment %q: %w", environment, err)
	}
	var subscription string
	for _, pair := range values.GetKeyValues() {
		if pair.GetKey() == "AZURE_SUBSCRIPTION_ID" {
			subscription = pair.GetValue()
			break
		}
	}
	subscription, err = validatedID(subscription)
	if err != nil {
		return nil, fmt.Errorf("azd environment %q must select a valid AZURE_SUBSCRIPTION_ID; use azd env set AZURE_SUBSCRIPTION_ID <subscription-id> --environment %q: %w",
			environment, environment, err)
	}
	return tokenProviderForSubscription(ctx, client, subscription)
}

func tokenProviderForSubscription(ctx context.Context, client *azdext.AzdClient, subscription string) (*azdext.TokenProvider, error) {
	subscription, err := validatedID(subscription)
	if err != nil {
		return nil, fmt.Errorf("credential subscription: %w", err)
	}
	response, err := client.Account().LookupTenant(ctx, &azdext.LookupTenantRequest{SubscriptionId: subscription})
	if err != nil {
		return nil, fmt.Errorf("resolve credential tenant for the selected azd subscription: %w", err)
	}
	tenant, err := validatedID(response.GetTenantId())
	if err != nil {
		return nil, fmt.Errorf("azd account lookup returned an invalid tenant ID for the selected subscription: %w", err)
	}
	// An explicit tenant bypasses the SDK's Bicep-specific deployment-context lookup.
	credential, err := azdext.NewTokenProvider(ctx, client, &azdext.TokenProviderOptions{TenantID: tenant})
	if err != nil {
		return nil, fmt.Errorf("construct azd token provider for the selected subscription: %w", err)
	}
	return credential, nil
}
