package project

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

// Index checks the deployed definition before indexing; it never reconciles it.
func (p *Provider) Index(ctx context.Context, name string, progress azdext.ProgressReporter) (*azdext.ServiceDeployResult, error) {
	project, service, err := p.service(ctx, &azdext.ServiceConfig{Name: name, Host: config.KnowledgeBaseHost}, false)
	if err != nil {
		return nil, err
	}
	return p.runKnowledgeBase(ctx, project, service, true, progress)
}

func knowledgeBaseDefinition(kb *config.KnowledgeBase) discovery.KnowledgeBaseDefinition {
	definition := discovery.KnowledgeBaseDefinition{
		Description: kb.Description, CopilotInstruction: kb.CopilotInstruction,
	}
	for _, source := range kb.Sources {
		definition.StorageAssetReferences = append(definition.StorageAssetReferences,
			discovery.StorageAssetReference{ID: source.StorageAssetResourceID})
	}
	return definition
}

func (p *Provider) verifiedKnowledgeBaseID(ctx context.Context, store *state.Store, service *config.Service) (id string, err error) {
	if service == nil || service.KnowledgeBase == nil || p.deps.CloudInSubscription == nil {
		return "", errors.New("knowledge-base attachment requires a declared KB and subscription-scoped credentials")
	}
	unlock, err := store.Lock(ctx, service.Name)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	kb := service.KnowledgeBase
	scope := config.BookshelfResourceID(kb) + "/knowledgeBases/" + kb.Name
	record, err := store.Read(service.Name, scope)
	if err != nil {
		return "", err
	}
	source, err := sourceHash(service)
	if err != nil {
		return "", err
	}
	if record == nil || record.Source != source || (record.Phase != "complete" && record.Phase != "verified") ||
		record.Indexing == nil || record.Indexing.CompletedRunID == "" {
		return "", errors.New("KB attachment dependency is missing, stale, pending or not indexed; deploy and index the KB first")
	}
	client, err := p.deps.CloudInSubscription(ctx, kb.SubscriptionID)
	if err != nil {
		return "", err
	}
	if _, err := client.ResolveBookshelf(ctx, config.BookshelfResourceID(kb)); err != nil {
		return "", err
	}
	definition := knowledgeBaseDefinition(kb)
	if err := client.ValidateKnowledgeBaseSources(ctx, definition); err != nil {
		return "", err
	}
	remote, err := client.ReadyKnowledgeBase(ctx, kb.Name, kb.BookshelfName)
	if err != nil {
		return "", err
	}
	if err := discovery.VerifyKnowledgeBase(remote, definition); err != nil {
		return "", err
	}
	if remote.LastIndexingRun == nil || remote.LastIndexingRun.Status != "Succeeded" ||
		remote.LastIndexingRun.RunID != record.Indexing.CompletedRunID ||
		(remote.Status != "" && remote.Status != "Succeeded") {
		return "", errors.New("KB attachment requires the successful indexing run verified by this service; reconcile/index it before deploying the agent")
	}
	if err := config.ValidateKnowledgeBaseID(remote.ID); err != nil {
		return "", fmt.Errorf("GA KB readback has no usable attachment identifier; no version or fallback ID will be constructed: %w", err)
	}
	if !strings.EqualFold(record.Outputs["bookshelfResourceId"], config.BookshelfResourceID(kb)) ||
		record.Outputs["knowledgeBaseId"] != remote.ID {
		return "", errors.New("KB attachment identity differs from verified deployment outputs; refresh the KB deployment")
	}
	return remote.ID, nil
}

func knowledgeBaseFailure(store *state.Store, record *state.Record, err error, submitting bool) error {
	var response *discovery.HTTPError
	if terminalFailure(err) || (submitting && errors.As(err, &response) && response.Rejected()) {
		record.Phase, record.Operation = "failed", ""
		return errors.Join(err, store.Write(record))
	}
	return err
}

func (p *Provider) runKnowledgeBase(ctx context.Context, project *config.Project, service *config.Service,
	indexOnly bool, progress azdext.ProgressReporter) (result *azdext.ServiceDeployResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(project.Discovery.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	kb := service.KnowledgeBase
	if kb == nil || p.deps.CloudInSubscription == nil {
		return nil, errors.New("knowledge-base configuration and subscription-scoped cloud client are required")
	}
	shouldIndex := indexOnly || kb.Autoindex == nil || *kb.Autoindex
	client, err := p.deps.CloudInSubscription(ctx, kb.SubscriptionID)
	if err != nil {
		return nil, err
	}
	client.Progress = progress
	environment, err := p.deps.Environment(ctx)
	if err != nil {
		return nil, err
	}
	store, err := state.Open(project.Root, environment)
	if err != nil {
		return nil, err
	}
	unlock, err := store.Lock(ctx, service.Name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unlock()) }()

	bookID := config.BookshelfResourceID(kb)
	if _, err := client.ResolveBookshelf(ctx, bookID); err != nil {
		return nil, err
	}
	definition := knowledgeBaseDefinition(kb)
	if err := client.ValidateKnowledgeBaseSources(ctx, definition); err != nil {
		return nil, err
	}
	request := discovery.IndexingRequest{ProjectID: project.Discovery.ProjectResourceID, NodePoolID: kb.NodePoolResourceID}
	if request.NodePoolID == "" {
		request.NodePoolID = project.Discovery.NodePoolResourceID
	}
	source, err := sourceHash(service)
	if err != nil {
		return nil, err
	}
	desired, err := state.Hash(struct {
		Source    string
		Request   discovery.IndexingRequest
		IndexOnly bool
	}{source, request, indexOnly})
	if err != nil {
		return nil, err
	}
	scope := bookID + "/knowledgeBases/" + kb.Name // Local identity, not a GA attachment ID.
	record, err := store.Read(service.Name, scope)
	if err != nil {
		return nil, err
	}
	if record != nil && record.Phase == "kb-ready" && (record.Desired != desired || record.Source != source) {
		record = nil // The definition is settled and no indexing submission exists yet.
	}
	resuming := record != nil && record.Phase != "complete" && record.Phase != "failed"
	if resuming && (record.Desired != desired || record.Source != source) {
		return nil, errors.New("pending KB work belongs to different configuration/action; restore it and rerun the original deploy/index command before changing it")
	}
	var previousIndex *state.Indexing
	previousID := ""
	if !resuming && !shouldIndex && record != nil && record.Phase == "complete" && record.Source == source &&
		record.Indexing != nil && record.Indexing.CompletedRunID != "" {
		copy := *record.Indexing
		previousIndex, previousID = &copy, record.Outputs["knowledgeBaseId"]
	}
	if !resuming {
		record = &state.Record{SchemaVersion: 1, Scope: scope, Service: service.Name, Source: source,
			Desired: desired, Phase: "kb-ready"}
		if !indexOnly {
			record.Phase = "kb-config-submitting"
			if err := store.Write(record); err != nil {
				return nil, err
			}
			operation, err := client.ReconcileKnowledgeBase(ctx, kb.Name, kb.BookshelfName, definition)
			if err != nil {
				return nil, knowledgeBaseFailure(store, record, err, true)
			}
			record.Phase, record.Operation = "kb-config-polling", operation
			if err := store.Write(record); err != nil {
				return nil, err
			}
		}
	}
	switch record.Phase {
	case "kb-config-submitting", "kb-config-polling":
		if record.Operation != "" {
			if err := client.ValidateBookshelfOperationURL(record.Operation); err != nil {
				return nil, err
			}
			if err := client.Poll(ctx, record.Operation); err != nil {
				return nil, knowledgeBaseFailure(store, record, err, false)
			}
		}
	case "kb-ready", "kb-index-submitting", "kb-index-polling", "verified":
	default:
		return nil, errors.New("unrecognized KB recovery phase; inspect the recovery record before proceeding")
	}

	remote, err := client.ReadyKnowledgeBase(ctx, kb.Name, kb.BookshelfName)
	if err != nil {
		if record.Phase == "kb-config-submitting" || record.Phase == "kb-config-polling" || record.Phase == "kb-ready" {
			return nil, knowledgeBaseFailure(store, record, err, false)
		}
		return nil, err
	}
	if err := discovery.VerifyKnowledgeBase(remote, definition); err != nil {
		return nil, fmt.Errorf("KB configuration is not verified; deploy it before indexing, or inspect the pending update: %w", err)
	}
	if previousIndex != nil && remote.ID == previousID && remote.LastIndexingRun != nil &&
		remote.LastIndexingRun.RunID == previousIndex.CompletedRunID && remote.LastIndexingRun.Status == "Succeeded" {
		record.Indexing = previousIndex
	}
	if record.Phase == "kb-config-submitting" || record.Phase == "kb-config-polling" {
		record.Phase, record.Operation = "kb-ready", ""
		if err := store.Write(record); err != nil {
			return nil, err
		}
	}
	if record.Phase == "kb-ready" && shouldIndex {
		if remote.Status == "Running" || (remote.LastIndexingRun != nil && remote.LastIndexingRun.Status == "Running") {
			return nil, errors.New("KB already has an active indexing run outside this recovery record; wait for it before starting another")
		}
		for _, id := range []string{request.ProjectID, request.NodePoolID} {
			if id != "" {
				if _, err := client.Ready(ctx, id); err != nil {
					return nil, fmt.Errorf("indexing compute/project prerequisite: %w", err)
				}
			}
		}
		record.Indexing = &state.Indexing{}
		if remote.LastIndexingRun != nil {
			record.Indexing.PreviousRunID = remote.LastIndexingRun.RunID
		}
		record.Phase = "kb-index-submitting"
		if err := store.Write(record); err != nil {
			return nil, err
		}
		if progress != nil {
			progress("Starting knowledge-base indexing")
		}
		operation, runID, err := client.StartKnowledgeBaseIndexing(ctx, kb.Name, request)
		if err != nil {
			return nil, knowledgeBaseFailure(store, record, err, true)
		}
		record.Phase, record.Operation = "kb-index-polling", operation
		record.Indexing.AcknowledgedRunID = runID
		if err := store.Write(record); err != nil {
			return nil, err
		}
	}
	if record.Phase == "kb-index-submitting" {
		return nil, errors.New("indexing submission outcome is ambiguous; inspect the Bookshelf operation history and recover its operation URL before retrying; no duplicate job was submitted")
	}
	if record.Phase == "kb-index-polling" {
		if record.Indexing == nil || record.Operation == "" {
			return nil, errors.New("pending indexing has no correlation record/operation URL")
		}
		if err := client.ValidateKnowledgeBaseOperationURL(kb.Name, record.Operation); err != nil {
			return nil, err
		}
		if progress != nil {
			progress("Waiting for the existing knowledge-base indexing operation")
		}
		operation, err := client.PollKnowledgeBaseIndexing(ctx, record.Operation)
		if err != nil {
			return nil, knowledgeBaseFailure(store, record, err, false)
		}
		remote, err = client.KnowledgeBase(ctx, kb.Name, kb.BookshelfName)
		if err != nil {
			return nil, err
		}
		if err := discovery.VerifyKnowledgeBase(remote, definition); err != nil {
			return nil, err
		}
		if err := discovery.VerifyKnowledgeBaseIndexing(remote, operation, record.Indexing.AcknowledgedRunID); err != nil {
			return nil, err
		}
		if remote.LastIndexingRun.RunID == record.Indexing.PreviousRunID {
			return nil, errors.New("indexing readback still describes the previous run; rerun to resume verification, not a new submission")
		}
		record.Indexing.CompletedRunID = remote.LastIndexingRun.RunID
	}
	if record.Phase == "verified" && shouldIndex {
		if record.Indexing == nil || record.Indexing.CompletedRunID == "" {
			return nil, errors.New("verified indexing recovery record has no completed run identity")
		}
		if remote.LastIndexingRun == nil || remote.LastIndexingRun.Status != "Succeeded" ||
			remote.LastIndexingRun.RunID != record.Indexing.CompletedRunID {
			return nil, errors.New("verified indexing run changed before exports could be restored")
		}
	}
	status, runID := remote.Status, ""
	if status == "" {
		status = "NotStarted"
	}
	if remote.LastIndexingRun != nil {
		status, runID = remote.LastIndexingRun.Status, remote.LastIndexingRun.RunID
	}
	record.Outputs = map[string]string{
		"knowledgeBaseId": remote.ID, "name": remote.Name, "bookshelfResourceId": bookID,
		"indexingStatus": status, "runId": runID,
	}
	record.Phase = "verified"
	if err := store.Write(record); err != nil {
		return nil, err
	}
	keys := slices.Sorted(maps.Keys(record.Outputs))
	for _, key := range keys {
		if err := p.deps.Export(ctx, config.Alias(service.Name, key), record.Outputs[key]); err != nil {
			return nil, fmt.Errorf("verified KB export failed; rerun the same command to restore exports without indexing again: %w", err)
		}
	}
	record.Phase, record.Operation = "complete", ""
	if err := store.Write(record); err != nil {
		return nil, err
	}
	return &azdext.ServiceDeployResult{Artifacts: []*azdext.Artifact{{
		Kind: azdext.ArtifactKind_ARTIFACT_KIND_DEPLOYMENT, Location: bookID,
		LocationKind: azdext.LocationKind_LOCATION_KIND_REMOTE, Metadata: record.Outputs,
	}}}, nil
}
