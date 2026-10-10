package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func Clone(definition map[string]any) (map[string]any, error) {
	data, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	var copy map[string]any
	err = json.Unmarshal(data, &copy)
	return copy, err
}

func ToolDefinition(service *config.Service, image string) (map[string]any, error) {
	if err := config.ValidateImage(image); err != nil {
		return nil, err
	}
	definition, err := Clone(service.Definition)
	if err != nil {
		return nil, err
	}
	nodes, ok := definition["infra"].([]any)
	if !ok {
		return nil, errors.New("tool definition has no infra entries")
	}
	count := 0
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("tool infra entry must be an object")
		}
		if node["name"] == service.Tool.Image.InfraNode {
			target, ok := node["image"].(map[string]any)
			if !ok || len(target) != 1 {
				return nil, errors.New("unsupported tool image shape")
			}
			if _, ok := target["acr"].(string); !ok {
				return nil, errors.New("tool image.acr must be a string")
			}
			target["acr"] = image
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("expected exactly one matching infra node")
	}
	return definition, nil
}

func AgentDefinition(service *config.Service, outputs map[string]map[string]string) (map[string]any, error) {
	definition, err := Clone(service.Definition)
	if err != nil {
		return nil, err
	}
	resolve := func(value any) (string, error) {
		scalar, ok := value.(string)
		if !ok || scalar == "" {
			return "", errors.New("agent deployment field must be a nonempty scalar")
		}
		key, isBinding := config.BindingKey(scalar)
		if !isBinding {
			return scalar, nil
		}
		binding, exists := service.Agent.Bindings[key]
		if !exists {
			return "", errors.New("missing declared binding")
		}
		resolved := outputs[binding.Service][binding.Output]
		if resolved == "" {
			return "", fmt.Errorf("verified dependency output %s.%s is unavailable; deploy that service first", binding.Service, binding.Output)
		}
		return resolved, nil
	}
	model, ok := definition["model"].(map[string]any)
	if !ok && service.Agent.Model == nil {
		return nil, errors.New("agent model must be an object")
	}
	if service.Agent.Model != nil {
		if !ok {
			model = map[string]any{}
			definition["model"] = model
		}
		model["id"] = service.Agent.Model.DeploymentName
	}
	name, ok := model["id"].(string)
	if !ok {
		return nil, errors.New("agent model.id must be a deployment name")
	}
	if err := config.ValidateDeploymentName(name); err != nil {
		return nil, err
	}
	if extensions, ok := definition["discoveryExtensions"].(map[string]any); ok {
		if tools, ok := extensions["tools"].([]any); ok {
			for _, raw := range tools {
				tool, ok := raw.(map[string]any)
				if !ok {
					return nil, errors.New("agent tool must be an object")
				}
				tool["toolId"], err = resolve(tool["toolId"])
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return definition, nil
}

type AgentPayload struct {
	Name                string         `json:"name"`
	HumanInTheLoop      any            `json:"humanInTheLoop,omitempty"`
	Tools               any            `json:"tools,omitempty"`
	DiscoveryExtensions map[string]any `json:"discoveryExtensions"`
	FoundryDetails      FoundryDetails `json:"foundryDetails"`
}

type FoundryDetails struct {
	Description string         `json:"description"`
	Definition  map[string]any `json:"definition,omitempty"`
	Versions    *AgentVersions `json:"versions,omitempty"`
}

type AgentVersions struct {
	Latest struct {
		Version string `json:"version"`
		// Foundry returns description on the version, not the agent object.
		Description *string        `json:"description,omitempty"`
		Definition  map[string]any `json:"definition"`
	} `json:"latest"`
}

type Agent struct {
	AgentPayload
	ProvisioningState string `json:"provisioningState"`
}

func Payload(definition map[string]any) (*AgentPayload, error) {
	if err := config.ValidateAgentShape(definition); err != nil {
		return nil, err
	}
	model, ok := definition["model"].(map[string]any)
	if !ok || len(model) != 1 {
		return nil, errors.New("only the verified model.id request adapter is implemented")
	}
	payload := &AgentPayload{DiscoveryExtensions: map[string]any{}, FoundryDetails: FoundryDetails{
		Definition: map[string]any{"kind": definition["kind"], "model": model["id"], "instructions": definition["instructions"]},
	}}
	payload.Name, _ = definition["name"].(string)
	payload.FoundryDetails.Description, _ = definition["description"].(string)
	if extensions, ok := definition["discoveryExtensions"].(map[string]any); ok {
		for key, value := range extensions {
			switch key {
			case "humanInTheLoop":
				payload.HumanInTheLoop = value
			case "tools":
				payload.Tools = value
			case "knowledgeBases":
				return nil, errors.New("knowledge-base request/readiness contracts are not verified; attachment is not implemented")
			default:
				payload.DiscoveryExtensions[key] = value
			}
		}
	}
	return payload, nil
}

func VerifyAgent(agent *Agent, expected *AgentPayload) error {
	if agent == nil {
		return errors.New("agent readback is missing")
	}
	if agent.ProvisioningState != "Succeeded" {
		return errors.New("agent readback mismatch: provisioningState (expected Succeeded)")
	}
	if agent.FoundryDetails.Versions == nil {
		return errors.New("agent readback is missing foundryDetails.versions.latest")
	}
	latest := agent.FoundryDetails.Versions.Latest
	if strings.TrimSpace(latest.Version) == "" {
		return errors.New("agent readback is missing foundryDetails.versions.latest.version")
	}
	if latest.Definition == nil {
		return errors.New("agent readback is missing foundryDetails.versions.latest.definition")
	}
	for _, field := range []struct {
		path            string
		actual, desired any
	}{
		{"name", agent.Name, expected.Name},
		{"humanInTheLoop", agent.HumanInTheLoop, expected.HumanInTheLoop},
		{"tools", agent.Tools, expected.Tools},
		{"discoveryExtensions", agent.DiscoveryExtensions, expected.DiscoveryExtensions},
	} {
		if err := verifyField(field.path, field.actual, field.desired); err != nil {
			return err
		}
	}
	if latest.Description != nil {
		if err := verifyField("foundryDetails.versions.latest.description", *latest.Description, expected.FoundryDetails.Description); err != nil {
			return err
		}
	}
	for _, key := range slices.Sorted(maps.Keys(expected.FoundryDetails.Definition)) {
		if err := verifyField("foundryDetails.versions.latest.definition."+key,
			latest.Definition[key], expected.FoundryDetails.Definition[key]); err != nil {
			return err
		}
	}
	return nil
}

// Report the differing field without exposing prompts, IDs, or other values.
func verifyField(path string, actual, desired any) error {
	if reflect.DeepEqual(actual, desired) {
		return nil
	}
	switch want := desired.(type) {
	case map[string]any:
		if got, ok := actual.(map[string]any); ok && (got == nil) == (want == nil) {
			for _, key := range slices.Sorted(maps.Keys(want)) {
				value, exists := got[key]
				if !exists {
					return fmt.Errorf("agent readback mismatch: %s.%s (missing)", path, key)
				}
				if err := verifyField(path+"."+key, value, want[key]); err != nil {
					return err
				}
			}
			for _, key := range slices.Sorted(maps.Keys(got)) {
				if _, exists := want[key]; !exists {
					return fmt.Errorf("agent readback mismatch: %s.%s (unexpected)", path, key)
				}
			}
		}
	case []any:
		if got, ok := actual.([]any); ok && (got == nil) == (want == nil) {
			if len(got) != len(want) {
				return fmt.Errorf("agent readback mismatch: %s.length", path)
			}
			for i := range want {
				if err := verifyField(fmt.Sprintf("%s[%d]", path, i), got[i], want[i]); err != nil {
					return err
				}
			}
		}
	}
	return fmt.Errorf("agent readback mismatch: %s", path)
}
