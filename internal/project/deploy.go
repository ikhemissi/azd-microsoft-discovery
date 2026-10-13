package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

var endpointName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func pendingSubmission(record *state.Record) bool {
	return record != nil && (record.Phase == "submitting" || record.Phase == "polling")
}

func terminalFailure(err error) bool {
	var failure *discovery.OperationError
	return errors.As(err, &failure)
}

func recordFailure(store *state.Store, record *state.Record, err error) error {
	var response *discovery.HTTPError
	if terminalFailure(err) || (errors.As(err, &response) && response.Rejected()) {
		record.Phase, record.Operation = "failed", ""
		return errors.Join(err, store.Write(record))
	}
	return err
}

func resumeOperation(ctx context.Context, client *discovery.Client, store *state.Store, record *state.Record) error {
	if pendingSubmission(record) && record.Operation != "" {
		if err := client.Poll(ctx, record.Operation); err != nil {
			// Poll HTTP errors do not prove that the original mutation failed.
			if terminalFailure(err) {
				return recordFailure(store, record, err)
			}
			return err
		}
	}
	return nil
}

func (p *Provider) Deploy(ctx context.Context, sc *azdext.ServiceConfig, input *azdext.ServiceContext,
	target *azdext.TargetResource, progress azdext.ProgressReporter) (result *azdext.ServiceDeployResult, err error) {
	project, service, err := p.service(ctx, sc, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(project.Discovery.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	expected, err := p.GetTargetResource(ctx, target.GetSubscriptionId(), sc, nil)
	if err != nil {
		return nil, err
	}
	if target != nil && !reflect.DeepEqual(target.Metadata, expected.Metadata) {
		return nil, errors.New("Discovery target changed since resolution; rerun deployment")
	}
	if service.KnowledgeBase != nil {
		return p.runKnowledgeBase(ctx, project, service, false, progress)
	}
	client, err := p.deps.Cloud(ctx)
	if err != nil {
		return nil, err
	}
	client.Progress = progress
	workspace, err := client.Ready(ctx, project.Discovery.WorkspaceResourceID)
	if err != nil {
		return nil, fmt.Errorf("workspace prerequisite: %w", err)
	}
	if workspace.Location == "" || !endpointName.MatchString(workspace.Name) {
		return nil, errors.New("workspace readback lacks a verified public-cloud name/location")
	}
	client.WorkspaceEndpoint = "https://" + workspace.Name + ".workspace.discovery.azure.com"
	if _, err := client.Ready(ctx, project.Discovery.ProjectResourceID); err != nil {
		return nil, fmt.Errorf("project prerequisite: %w", err)
	}
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
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release Discovery state lock: %w", unlockErr))
		}
	}()
	source, err := sourceHash(service)
	if err != nil {
		return nil, err
	}
	record := &state.Record{
		SchemaVersion: 1, Scope: project.Discovery.ProjectResourceID, Service: service.Name,
		Source: source, Phase: "verified", Outputs: map[string]string{},
	}
	switch service.Host {
	case config.ToolHost:
		if _, err := client.Ready(ctx, project.Discovery.NodePoolResourceID); err != nil {
			return nil, fmt.Errorf("node pool prerequisite: %w", err)
		}
		if err := p.deployTool(ctx, client, store, project, service, input, workspace.Location, record); err != nil {
			return nil, err
		}
	case config.AgentHost:
		if err := p.deployAgent(ctx, client, store, project, service, record); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported Discovery deployment host")
	}
	// Verified completion is last: exports are individual, non-atomic Environment RPCs.
	if err := store.Write(record); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(record.Outputs))
	for key := range record.Outputs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := p.deps.Export(ctx, config.Alias(service.Name, key), record.Outputs[key]); err != nil {
			return nil, fmt.Errorf("verified resource export failed; rerun to restore exports without an unchanged upsert: %w", err)
		}
	}
	record.Phase, record.Operation = "complete", ""
	if err := store.Write(record); err != nil {
		return nil, err
	}
	artifact := &azdext.Artifact{
		Kind:     azdext.ArtifactKind_ARTIFACT_KIND_DEPLOYMENT,
		Location: expected.Metadata["resourceId"], LocationKind: azdext.LocationKind_LOCATION_KIND_REMOTE,
		Metadata: record.Outputs,
	}
	return &azdext.ServiceDeployResult{Artifacts: []*azdext.Artifact{artifact}}, nil
}

func verifyModelDeployment(ctx context.Context, client *discovery.Client, resourceID string) error {
	resource, err := client.Ready(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("model prerequisite: %w", err)
	}
	id, err := arm.ParseResourceID(resourceID)
	if err != nil || resource.Name != id.Name || resource.Name == "" {
		return errors.New("model readback deployment name does not match its resource ID")
	}
	return nil
}

func verifyTool(remote *discovery.Resource, definition map[string]any) error {
	if remote.Properties.ProvisioningState != "Succeeded" ||
		remote.Properties.Version != definition["version"] ||
		!reflect.DeepEqual(remote.Properties.Definition, definition) {
		return errors.New("registered tool definition/version/image does not match verified readback")
	}
	return nil
}

func (p *Provider) deployTool(ctx context.Context, client *discovery.Client, store *state.Store, project *config.Project,
	service *config.Service, input *azdext.ServiceContext, location string, record *state.Record) error {
	image := service.Tool.Image.Reference
	if service.Tool.Image.Mode == "build" {
		if input == nil {
			return errors.New("tool deployment requires a published immutable container artifact")
		}
		artifact, err := containerArtifact(input.Publish, azdext.LocationKind_LOCATION_KIND_REMOTE)
		if err != nil || artifact == nil {
			return errors.New("tool deployment requires the successful native publication result")
		}
		image = artifact.Location
		if artifact.Metadata["discoverySource"] != record.Source {
			return errors.New("published image belongs to stale tool configuration; repackage and publish")
		}
	}
	if err := config.ValidateImage(image); err != nil {
		return err
	}
	if service.Tool.Image.Mode == "build" {
		verified, err := p.deps.Digest(ctx, image, "")
		if err != nil {
			return err
		}
		if verified != image {
			return errors.New("immutable image changed during registry verification")
		}
	}
	definition, err := discovery.ToolDefinition(service, image)
	if err != nil {
		return err
	}
	if version, ok := definition["version"].(string); !ok || version == "" {
		return errors.New("tool definition requires a nonempty string version")
	}
	id, err := toolID(project, service)
	if err != nil {
		return err
	}
	record.Desired, err = state.Hash(definition)
	if err != nil {
		return err
	}
	pending, err := store.Read(service.Name, record.Scope)
	if err != nil {
		return err
	}
	if err := resumeOperation(ctx, client, store, pending); err != nil {
		return err
	}
	if pendingSubmission(pending) {
		if pending.Desired != record.Desired {
			return errors.New("an earlier tool update is unresolved; reconcile its configuration first")
		}
	}
	remote, readErr := client.Resource(ctx, id)
	if readErr == nil && remote.Properties.ProvisioningState != "Succeeded" {
		remote, readErr = client.Ready(ctx, id)
	}
	if terminalFailure(readErr) {
		// A terminally failed resource can be repaired by a new PUT.
		readErr = nil
	}
	unchanged := readErr == nil && verifyTool(remote, definition) == nil
	if readErr != nil {
		var response *discovery.HTTPError
		if !errors.As(readErr, &response) || response.Status != http.StatusNotFound {
			return readErr
		}
	}
	if !unchanged {
		body := struct {
			Location   string            `json:"location"`
			Tags       map[string]string `json:"tags,omitempty"`
			Properties struct {
				Version    string         `json:"version"`
				Definition map[string]any `json:"definitionContent"`
			} `json:"properties"`
		}{Location: location}
		if category, ok := definition["category"].(string); ok {
			body.Tags = map[string]string{"category": category}
		}
		body.Properties.Version = definition["version"].(string)
		body.Properties.Definition = definition
		record.Phase = "submitting"
		if err := store.Write(record); err != nil {
			return err
		}
		response, err := client.Request(ctx, http.MethodPut, discovery.ARMURL(id), body)
		if err != nil {
			return recordFailure(store, record, err)
		}
		operation, err := client.Operation(response, discovery.ARMEndpoint)
		if err != nil {
			return err
		}
		record.Operation, record.Phase = operation, "polling"
		if err := store.Write(record); err != nil {
			return err
		}
		if operation != "" {
			if err := client.Poll(ctx, operation); err != nil {
				if terminalFailure(err) {
					return recordFailure(store, record, err)
				}
				return err
			}
		}
	}
	remote, err = client.Ready(ctx, id)
	if err != nil {
		if terminalFailure(err) {
			return recordFailure(store, record, err)
		}
		return err
	}
	if err := verifyTool(remote, definition); err != nil {
		return err
	}
	record.Outputs = map[string]string{"resourceId": remote.ID, "image": image}
	record.Phase = "verified"
	return nil
}

func agentURL(client *discovery.Client, project *config.Project, name string) (string, error) {
	id, err := arm.ParseResourceID(project.Discovery.ProjectResourceID)
	if err != nil || name == "" {
		return "", errors.New("invalid project/agent identity")
	}
	return client.WorkspaceEndpoint + "/projects/" + url.PathEscape(id.Name) +
		"/agents/" + url.PathEscape(name) + "?api-version=" + discovery.AgentVersion, nil
}

func readAgent(ctx context.Context, client *discovery.Client, address string) (*discovery.Agent, error) {
	response, err := client.Request(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	var agent discovery.Agent
	if err := json.Unmarshal(response.Body, &agent); err != nil {
		return nil, errors.New("invalid agent readback response")
	}
	return &agent, nil
}

func readyAgent(ctx context.Context, client *discovery.Client, address string) (*discovery.Agent, error) {
	if client.Progress != nil {
		client.Progress("Verifying Discovery agent readiness")
	}
	for {
		agent, err := readAgent(ctx, client, address)
		if err != nil {
			return nil, err
		}
		done, err := discovery.Terminal(agent.ProvisioningState)
		if err != nil || done {
			return agent, err
		}
		if err := client.Sleep(ctx, 2*time.Second); err != nil {
			return nil, fmt.Errorf("agent readiness interrupted; remote submission was not canceled: %w", err)
		}
	}
}

func (p *Provider) bindingOutputs(ctx context.Context, client *discovery.Client, store *state.Store,
	project *config.Project, service *config.Service) (map[string]map[string]string, error) {
	outputs := map[string]map[string]string{}
	for _, binding := range service.Agent.Bindings {
		if outputs[binding.Service] != nil {
			continue
		}
		source := project.Services[binding.Service]
		switch source.Host {
		case config.ToolHost:
			record, err := store.Read(source.Name, project.Discovery.ProjectResourceID)
			if err != nil {
				return nil, err
			}
			fingerprint, err := sourceHash(source)
			if err != nil {
				return nil, err
			}
			if record == nil || record.Source != fingerprint || record.Outputs["image"] == "" {
				return nil, fmt.Errorf("tool dependency %q is missing/stale; deploy it first (no automatic rebuild)", source.Name)
			}
			definition, err := discovery.ToolDefinition(source, record.Outputs["image"])
			if err != nil {
				return nil, err
			}
			id, err := toolID(project, source)
			if err != nil || !strings.EqualFold(record.Outputs["resourceId"], id) {
				return nil, errors.New("tool dependency record has an incompatible resource identity")
			}
			remote, err := client.Ready(ctx, id)
			if err != nil {
				return nil, err
			}
			if err := verifyTool(remote, definition); err != nil {
				return nil, err
			}
			outputs[binding.Service] = record.Outputs
		default:
			return nil, errors.New("unsupported scalar dependency host")
		}
	}
	for _, binding := range service.Agent.KnowledgeBases {
		id, err := p.verifiedKnowledgeBaseID(ctx, store, project.Services[binding.Service])
		if err != nil {
			return nil, fmt.Errorf("knowledge-base dependency %q: %w", binding.Service, err)
		}
		outputs[binding.Service] = map[string]string{"knowledgeBaseId": id}
	}
	return outputs, nil
}

func (p *Provider) deployAgent(ctx context.Context, client *discovery.Client, store *state.Store,
	project *config.Project, service *config.Service, record *state.Record) error {
	outputs, err := p.bindingOutputs(ctx, client, store, project, service)
	if err != nil {
		return err
	}
	definition, err := discovery.AgentDefinition(service, outputs)
	if err != nil {
		return err
	}
	payload, err := discovery.Payload(definition)
	if err != nil {
		return err
	}
	name, ok := payload.FoundryDetails.Definition["model"].(string)
	if !ok {
		return errors.New("agent payload has no model deployment name")
	}
	if err := verifyModelDeployment(ctx, client,
		project.Discovery.WorkspaceResourceID+"/chatModelDeployments/"+name); err != nil {
		return fmt.Errorf("agent model deployment verification: %w", err)
	}
	if extensions, ok := definition["discoveryExtensions"].(map[string]any); ok {
		if tools, ok := extensions["tools"].([]any); ok {
			verified := map[string]bool{}
			for _, raw := range tools {
				tool, ok := raw.(map[string]any)
				if !ok {
					return errors.New("agent tool must be an object")
				}
				id, ok := tool["toolId"].(string)
				if !ok {
					return errors.New("agent toolId must be a resource ID")
				}
				if !verified[id] {
					if _, err := client.Ready(ctx, id); err != nil {
						return fmt.Errorf("agent tool prerequisite %s: %w", id, err)
					}
					verified[id] = true
				}
			}
		}
	}
	record.Desired, err = state.Hash(payload)
	if err != nil {
		return err
	}
	address, err := agentURL(client, project, payload.Name)
	if err != nil {
		return err
	}
	pending, err := store.Read(service.Name, record.Scope)
	if err != nil {
		return err
	}
	if err := resumeOperation(ctx, client, store, pending); err != nil {
		return err
	}
	if pendingSubmission(pending) {
		if pending.Desired != record.Desired {
			return errors.New("an earlier agent update is unresolved; restore its configuration and reconcile before another update")
		}
	}
	agent, readErr := readyAgent(ctx, client, address)
	if terminalFailure(readErr) {
		if pendingSubmission(pending) {
			return recordFailure(store, pending, readErr)
		}
		readErr = nil
	}
	unchanged := readErr == nil && discovery.VerifyAgent(agent, payload) == nil
	if readErr != nil {
		var response *discovery.HTTPError
		if !errors.As(readErr, &response) || response.Status != http.StatusNotFound {
			return readErr
		}
	}
	if !unchanged {
		if pendingSubmission(pending) {
			return errors.New("previous agent submission outcome is ambiguous; inspect Azure before permitting another POST")
		}
		record.Phase = "submitting"
		if err := store.Write(record); err != nil {
			return err
		}
		id, err := arm.ParseResourceID(project.Discovery.ProjectResourceID)
		if err != nil {
			return err
		}
		upsert := client.WorkspaceEndpoint + "/projects/" + url.PathEscape(id.Name) +
			":upsertAgent?api-version=" + discovery.AgentVersion
		body := struct {
			*discovery.AgentPayload
			KnowledgeBases *[]discovery.KnowledgeBaseReference `json:"knowledgeBases,omitempty"`
		}{AgentPayload: payload}
		if len(payload.KnowledgeBases) > 0 {
			body.KnowledgeBases = &payload.KnowledgeBases
		} else if agent != nil && len(agent.KnowledgeBases) > 0 {
			empty := []discovery.KnowledgeBaseReference{}
			body.KnowledgeBases = &empty
		}
		response, err := client.Request(ctx, http.MethodPost, upsert, body)
		if err != nil {
			var rejection *discovery.HTTPError
			if errors.As(err, &rejection) && rejection.Rejected() {
				return recordFailure(store, record, err)
			}
			// No retry: a POST may have created an immutable version despite the error.
			agent, readErr = readyAgent(ctx, client, address)
			if terminalFailure(readErr) {
				return recordFailure(store, record, readErr)
			}
			if readErr != nil || discovery.VerifyAgent(agent, payload) != nil {
				return fmt.Errorf("agent submission failed/ambiguous; recovery record retained, no retry: %w", err)
			}
		} else {
			operation, err := client.Operation(response, client.WorkspaceEndpoint)
			if err != nil {
				return err
			}
			record.Operation, record.Phase = operation, "polling"
			if err := store.Write(record); err != nil {
				return err
			}
			if operation != "" {
				if err := client.Poll(ctx, operation); err != nil {
					if terminalFailure(err) {
						return recordFailure(store, record, err)
					}
					return err
				}
			}
			agent, err = readyAgent(ctx, client, address)
			if err != nil {
				if terminalFailure(err) {
					return recordFailure(store, record, err)
				}
				return err
			}
		}
	}
	if err := discovery.VerifyAgent(agent, payload); err != nil {
		return err
	}
	record.Outputs = map[string]string{"name": agent.Name, "version": agent.FoundryDetails.Versions.Latest.Version}
	record.Phase = "verified"
	return nil
}
