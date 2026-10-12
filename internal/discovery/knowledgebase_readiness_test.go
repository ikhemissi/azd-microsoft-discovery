package discovery

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestKnowledgeBaseReadinessAndNotStartedOperation(t *testing.T) {
	client, remote, _, _ := kbFixture(t)
	if _, err := client.ResolveBookshelf(t.Context(), bookshelfID); err != nil {
		t.Fatal(err)
	}
	remote.ProvisioningState = "Updating"
	sleeps := 0
	client.Sleep = func(context.Context, time.Duration) error {
		sleeps++
		remote.ProvisioningState = "Succeeded"
		return nil
	}
	resource, err := client.ReadyKnowledgeBase(t.Context(), "papers", "literature")
	if err != nil || resource.ProvisioningState != "Succeeded" || sleeps != 1 {
		t.Fatalf("KB readiness did not wait for the existing update: %v", err)
	}
	remote.ProvisioningState = "Failed"
	if _, err := client.ReadyKnowledgeBase(t.Context(), "papers", "literature"); err == nil {
		t.Fatal("failed KB became ready")
	}
	calls := 0
	client.HTTP.Transport = transport(func(request *http.Request) (*http.Response, error) {
		statuses := []string{"NotStarted", "Running", "Succeeded"}
		status := statuses[min(calls, len(statuses)-1)]
		calls++
		return response(request, 200, `{"status":"`+status+`"}`, nil), nil
	})
	if err := client.Poll(t.Context(), "https://literature.discovery.azure.com/knowledgeBases/papers/operations/config-op"); err != nil || calls != 3 {
		t.Fatalf("GA NotStarted was not handled as a pending operation: %v", err)
	}
}
