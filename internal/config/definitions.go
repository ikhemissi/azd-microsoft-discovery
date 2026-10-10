package config

import (
	_ "crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/distribution/reference"
)

var placeholder = regexp.MustCompile(`^\{\{([A-Za-z][A-Za-z0-9_-]*)\}\}$`)
var toolName = regexp.MustCompile(`^[A-Za-z0-9-]{3,24}$`)
var deploymentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func BindingKey(value string) (string, bool) {
	match := placeholder.FindStringSubmatch(value)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// ValidateAgentShape is shared by preflight and payload adaptation. It does not
// resolve model/tool placeholders or require provisioning outputs.
func ValidateAgentShape(definition map[string]any) error {
	for _, key := range sortedKeys(definition) {
		switch key {
		case "name", "kind", "model", "instructions", "description", "discoveryExtensions":
		default:
			return fmt.Errorf("agent field %q has no verified request adapter", key)
		}
	}
	for _, key := range []string{"name", "instructions"} {
		if text, ok := definition[key].(string); !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("agent %s must be a nonempty string", key)
		}
	}
	if definition["kind"] != "prompt" {
		return errors.New("only kind: prompt agent definitions are supported")
	}
	if raw, exists := definition["description"]; exists {
		if _, ok := raw.(string); !ok {
			return errors.New("agent description must be a string")
		}
	}
	if raw, exists := definition["model"]; exists {
		model, ok := raw.(map[string]any)
		if !ok || len(model) != 1 {
			return errors.New("agent model must contain only id")
		}
		if id, ok := model["id"].(string); !ok || strings.TrimSpace(id) == "" {
			return errors.New("agent model.id must be a nonempty string")
		}
	}
	if raw, exists := definition["discoveryExtensions"]; exists {
		extensions, ok := raw.(map[string]any)
		if !ok {
			return errors.New("discoveryExtensions must be an object")
		}
		if _, exists := extensions["knowledgeBases"]; exists {
			return errors.New("knowledge-base attachment is outside this iteration")
		}
		if raw, exists := extensions["humanInTheLoop"]; exists {
			switch raw.(type) {
			case bool, string:
			default:
				return errors.New("humanInTheLoop must be a boolean or Discovery setting string")
			}
		}
		if raw, exists := extensions["tools"]; exists {
			tools, ok := raw.([]any)
			if !ok {
				return errors.New("discoveryExtensions.tools must be a list")
			}
			for _, raw := range tools {
				tool, ok := raw.(map[string]any)
				if !ok {
					return errors.New("agent tool must be an object")
				}
				if id, ok := tool["toolId"].(string); !ok || strings.TrimSpace(id) == "" {
					return errors.New("agent toolId must be a nonempty string")
				}
			}
		}
	}
	return nil
}

func ValidateDeploymentName(value string) error {
	if !deploymentName.MatchString(value) {
		return errors.New("requires a deployment name, not an ARM ID, catalog path, or unresolved placeholder")
	}
	return nil
}

func validateImage(value string) error {
	image, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return errors.New("image reference must be a valid OCI repository and SHA-256 digest")
	}

	if _, ok := image.(reference.Digested); !ok || !strings.Contains(value, "@sha256:") {
		return errors.New("image reference must include an immutable SHA-256 digest")
	}
	domain := strings.SplitN(value, "/", 2)
	if len(domain) != 2 || (!strings.ContainsAny(domain[0], ".:") && domain[0] != "localhost") {
		return errors.New("image reference requires an explicit registry host")
	}
	if _, tagged := image.(reference.Tagged); tagged {
		return errors.New("image reference must use a digest without a mutable tag")
	}
	return nil
}

func ValidateImage(value string) error { return validateImage(value) }

func validateDefinition(project *Project, service *Service) error {
	var path string
	if service.Tool != nil {
		path = service.Tool.Definition
	} else if service.Agent != nil {
		path = service.Agent.Definition
	} else {
		return nil
	}
	resolved, err := ResolvePath(project.Root, service.Project, path)
	if err != nil {
		return err
	}
	definition, err := ReadDocument(resolved)
	if err != nil {
		return err
	}
	service.Definition = definition
	name, ok := definition["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("definition requires a nonempty name")
	}
	if service.Tool != nil {
		if !toolName.MatchString(name) {
			return errors.New("tool name must be 3-24 alphanumeric/hyphen characters")
		}
		if version, ok := definition["version"].(string); !ok || strings.TrimSpace(version) == "" {
			return errors.New("tool definition requires a nonempty string version")
		}
		nodes, ok := definition["infra"].([]any)
		if !ok {
			return errors.New("tool definition requires an infra list")
		}
		if service.Tool.Image.InfraNode == "" {
			candidates := []string{}
			for _, item := range nodes {
				node, ok := item.(map[string]any)
				if !ok {
					return errors.New("tool infra entries must be objects")
				}
				image, ok := node["image"].(map[string]any)
				name, named := node["name"].(string)
				_, acr := image["acr"].(string)
				if ok && len(image) == 1 && acr && named && name != "" {
					candidates = append(candidates, name)
				}
			}
			if len(candidates) != 1 {
				return errors.New("set image.infraNode explicitly: tool must have exactly one eligible named image.acr infra entry to infer it")
			}
			service.Tool.Image.InfraNode = candidates[0]
		}
		matches := 0
		for _, item := range nodes {
			node, ok := item.(map[string]any)
			if !ok {
				return errors.New("tool infra entries must be objects")
			}
			if node["name"] == service.Tool.Image.InfraNode {
				matches++
				image, ok := node["image"].(map[string]any)
				if !ok {
					return errors.New("selected infra node requires an image object")
				}
				if _, ok := image["acr"].(string); !ok || len(image) != 1 {
					return errors.New("selected infra node requires the supported image.acr shape")
				}
			}
		}
		if matches != 1 {
			return errors.New("image.infraNode must identify exactly one tool infra entry")
		}
		return nil
	}
	return validateAgent(project, service)
}

func validateAgent(project *Project, service *Service) error {
	definition := service.Definition
	if err := ValidateAgentShape(definition); err != nil {
		return err
	}
	if len(service.Agent.KnowledgeBases) > 0 {
		return errors.New("knowledge-base attachment is outside this iteration")
	}
	if definition["kind"] != "prompt" {
		return errors.New("only kind: prompt agent definitions are supported")
	}
	if instructions, ok := definition["instructions"].(string); !ok || instructions == "" {
		return errors.New("prompt agent requires nonempty instructions")
	}
	used := map[string]bool{}
	model, ok := definition["model"].(map[string]any)
	if raw, exists := definition["model"]; exists && (!ok || len(model) > 1 || (len(model) == 1 && model["id"] == nil)) {
		return fmt.Errorf("agent model must be an object containing only id, got %T", raw)
	}
	if service.Agent.Model == nil {
		if !ok {
			return errors.New("agent requires config.model.deploymentName or definition model.id")
		}
		name, _ := model["id"].(string)
		if err := ValidateDeploymentName(name); err != nil {
			return fmt.Errorf("model.id: %w", err)
		}
	}
	if raw, exists := definition["discoveryExtensions"]; exists {
		extensions, ok := raw.(map[string]any)
		if !ok {
			return errors.New("discoveryExtensions must be an object")
		}
		if rawTools, exists := extensions["tools"]; exists {
			tools, ok := rawTools.([]any)
			if !ok {
				return errors.New("discoveryExtensions.tools must be a list")
			}
			for _, rawTool := range tools {
				tool, ok := rawTool.(map[string]any)
				if !ok {
					return errors.New("agent tool entry must be an object")
				}
				if err := validateToolID(project, service, tool["toolId"], used); err != nil {
					return fmt.Errorf("toolId: %w", err)
				}
			}
		}
		if _, exists := extensions["knowledgeBases"]; exists && len(service.Agent.KnowledgeBases) > 0 {
			return errors.New("native and extension knowledge-base attachments cannot be combined")
		}
	}
	for _, key := range sortedKeys(service.Agent.Bindings) {
		if !used[key] {
			return fmt.Errorf("binding %q is unused in supported deployment fields", key)
		}
	}
	return nil
}

func validateToolID(project *Project, service *Service, raw any, used map[string]bool) error {
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return errors.New("requires a nonempty scalar string")
	}
	key, isBinding := BindingKey(value)
	if !isBinding {
		if strings.Contains(value, "{{") || strings.Contains(value, "${") {
			return errors.New("binding must replace an entire scalar with a declared {{binding}}")
		}
		id, err := resource(value, "Microsoft.Discovery/tools")
		if err != nil {
			return err
		}
		workspace, err := resource(project.Discovery.WorkspaceResourceID, "Microsoft.Discovery/workspaces")
		if err != nil {
			return err
		}
		if !strings.EqualFold(id.SubscriptionID, workspace.SubscriptionID) {
			return errors.New("literal tool ID differs from the workspace subscription")
		}
		return nil
	}
	binding, ok := service.Agent.Bindings[key]
	if !ok {
		return fmt.Errorf("placeholder %q has no declared binding", key)
	}
	source, ok := project.Services[binding.Service]
	if !ok || !slices.Contains(service.Uses, binding.Service) {
		return fmt.Errorf("binding source %q must exist and be declared in uses", binding.Service)
	}
	if source.Host != ToolHost || binding.Output != "resourceId" {
		return fmt.Errorf("binding source %q must provide discovery.tool output resourceId", binding.Service)
	}
	used[key] = true
	return nil
}

func validateDependencies(project *Project) error {
	active, complete := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if active[name] {
			return fmt.Errorf("uses contains a dependency cycle at service %q", name)
		}
		if complete[name] {
			return nil
		}
		active[name] = true
		for _, dependency := range project.Services[name].Uses {
			if _, ok := project.Services[dependency]; !ok {
				return fmt.Errorf("service %q uses missing service %q", name, dependency)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		active[name], complete[name] = false, true
		return nil
	}
	for _, name := range sortedKeys(project.Services) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}
