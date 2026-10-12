package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/ikhemissi/azd-microsoft-discovery/schemas"
)

var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var separator = regexp.MustCompile(`[^A-Za-z0-9]`)

type Lookup func(string) (string, bool)

func Parse(root string, document map[string]any, lookup Lookup) (*Project, error) {
	return parse(root, document, lookup, false)
}

// ParseForPackage defers infrastructure outputs until provisioning/deployment.
func ParseForPackage(root string, document map[string]any, lookup Lookup) (*Project, error) {
	return parse(root, document, lookup, true)
}

func parse(root string, document map[string]any, lookup Lookup, deferred bool) (*Project, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	project := &Project{Root: root, Services: map[string]*Service{}}
	project.Name, _ = document["name"].(string)
	if strings.TrimSpace(project.Name) == "" {
		return nil, errors.New("azure.yaml requires a project name")
	}
	settingsLookup := lookup
	if deferred {
		settingsLookup = func(name string) (string, bool) {
			value, ok := lookup(name)
			if !ok || value == "" {
				return "${" + name + "}", true
			}
			return value, true
		}
	}
	if err := decodeMode("project", document["discovery"], settingsLookup, &project.Discovery, deferred); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if project.Discovery.OperationTimeoutSeconds == 0 {
		project.Discovery.OperationTimeoutSeconds = 1800
	}
	if err := validateScopeMode(project.Discovery, deferred); err != nil {
		return nil, err
	}
	rawServices, ok := document["services"].(map[string]any)
	if !ok || len(rawServices) == 0 {
		return nil, errors.New("azure.yaml requires declared services")
	}
	for _, name := range sortedKeys(rawServices) {
		raw, ok := rawServices[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("service %q must be an object", name)
		}
		service, err := parseService(project, name, raw, settingsLookup, deferred)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		project.Services[name] = service
	}
	if err := validateDependencies(project); err != nil {
		return nil, err
	}
	count := 0
	aliases := map[string]string{}
	targets := map[string]string{}
	for _, name := range sortedKeys(project.Services) {
		service := project.Services[name]
		if len(Outputs(service.Host)) == 0 {
			continue
		}
		count++
		for _, output := range Outputs(service.Host) {
			alias := Alias(name, output)
			if previous, ok := aliases[alias]; ok {
				return nil, fmt.Errorf("output alias %s collides between services %q and %q", alias, previous, name)
			}
			aliases[alias] = name
		}
		if err := validateDefinition(project, service); err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		if service.Host == ToolHost || service.Host == AgentHost {
			target := service.Host + ":" + strings.ToLower(service.Definition["name"].(string))
			if previous, exists := targets[target]; exists {
				return nil, fmt.Errorf("services %q and %q manage the same Discovery target", previous, name)
			}
			targets[target] = name
		}
		if kb := service.KnowledgeBase; kb != nil {
			target := strings.ToLower(BookshelfResourceID(kb) + "/knowledgeBases/" + kb.Name)
			if previous, exists := targets[target]; exists {
				return nil, fmt.Errorf("services %q and %q manage the same KB", previous, name)
			}
			targets[target] = name
		}
	}
	if count == 0 {
		return nil, errors.New("no Discovery services declared; use discovery.tool, discovery.agent, or discovery.knowledgebase")
	}
	return project, nil
}

func parseService(project *Project, name string, raw map[string]any, lookup Lookup, deferred bool) (*Service, error) {
	service := &Service{Name: name}
	service.Host, _ = raw["host"].(string)
	if service.Host == "" {
		return nil, errors.New("host is required")
	}
	if uses, exists := raw["uses"]; exists {
		items, ok := uses.([]any)
		if !ok {
			return nil, errors.New("uses must be a list of service names")
		}
		for _, item := range items {
			dependency, ok := item.(string)
			if !ok || strings.TrimSpace(dependency) == "" {
				return nil, errors.New("uses must contain nonempty service names")
			}
			if slices.Contains(service.Uses, dependency) {
				return nil, errors.New("uses contains a duplicate dependency")
			}
			service.Uses = append(service.Uses, dependency)
		}
	}
	if len(Outputs(service.Host)) == 0 {
		if strings.HasPrefix(service.Host, "discovery.") {
			return nil, errors.New("unsupported Discovery host")
		}
		return service, nil
	}
	service.Project = project.Root
	if path, exists := raw["project"]; exists {
		expanded, err := expand(path, lookup)
		if err != nil {
			return nil, fmt.Errorf("project: %w", err)
		}
		value, ok := expanded.(string)
		if !ok {
			return nil, errors.New("project path must be a string")
		}
		service.Project, err = ResolvePath(project.Root, project.Root, value)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(service.Project)
		if err != nil || !info.IsDir() {
			return nil, errors.New("service project must be an existing directory")
		}
	}
	switch service.Host {
	case KnowledgeBaseHost:
		if raw["language"] != nil || raw["docker"] != nil {
			return nil, errors.New("knowledge bases do not use language/docker build settings")
		}
		if err := parseKnowledgeBase(service, raw["config"], lookup, deferred); err != nil {
			return nil, err
		}
	case ToolHost:
		service.Tool = &Tool{}
		if err := decodeMode("tool", raw["config"], lookup, service.Tool, deferred); err != nil {
			return nil, err
		}
		if project.Discovery.NodePoolResourceID == "" && !deferred {
			return nil, errors.New("tool requires discovery.nodePoolResourceId")
		}
		if service.Tool.Image.Mode == "reference" {
			if raw["language"] != nil || raw["docker"] != nil {
				return nil, errors.New("reference image mode must omit build-only language/docker settings")
			}
			if err := validateImage(service.Tool.Image.Reference); err != nil && !(deferred && variable.FindString(service.Tool.Image.Reference) == service.Tool.Image.Reference) {
				return nil, err
			}
		} else if raw["language"] != "docker" {
			return nil, errors.New("build image mode requires language: docker")
		} else {
			if _, exists := raw["project"]; !exists {
				return nil, errors.New("build image mode requires project (use project: . for the root); azd otherwise skips Docker builds")
			}
			if err := validateDocker(project, service, raw["docker"], lookup, deferred); err != nil {
				return nil, err
			}
		}
	case AgentHost:
		service.Agent = &Agent{}
		if err := decodeMode("agent", raw["config"], lookup, service.Agent, deferred); err != nil {
			return nil, err
		}
		if service.Agent.Model != nil {
			name := service.Agent.Model.DeploymentName
			if !(deferred && variable.FindString(name) == name) {
				if err := ValidateDeploymentName(name); err != nil {
					return nil, fmt.Errorf("model.deploymentName: %w", err)
				}
			}
		}
	case BookshelfHost:
		return nil, errors.New("discovery.bookshelf is outside this iteration; Terraform owns Bookshelf infrastructure")
	}
	return service, nil
}

func decodeMode(kind string, value any, lookup Lookup, target any, deferred bool) error {
	if kind == "tool" || kind == "agent" {
		if value == nil {
			value = map[string]any{}
		}
		if input, ok := value.(map[string]any); ok {
			options := make(map[string]any, len(input)+2)
			maps.Copy(options, input)
			if _, exists := options["definition"]; !exists {
				options["definition"] = kind + ".yaml"
			}
			if kind == "agent" {
				if _, exists := options["bindings"]; !exists {
					options["bindings"] = map[string]any{}
				}
			} else {
				if _, exists := options["type"]; !exists {
					options["type"] = "container"
				}
				image, exists := options["image"]
				if !exists {
					image = map[string]any{}
				}
				if inputImage, ok := image.(map[string]any); ok {
					imageOptions := make(map[string]any, len(inputImage)+1)
					maps.Copy(imageOptions, inputImage)
					if _, exists := imageOptions["mode"]; !exists {
						imageOptions["mode"] = "build"
					}
					image = imageOptions
				}
				options["image"] = image
			}
			value = options
		}
	}
	expanded, err := expandMode(value, lookup, deferred)
	if err != nil {
		return err
	}
	data, err := json.Marshal(expanded)
	if err != nil {
		return errors.New("configuration contains unsupported values")
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return fmt.Errorf("normalize configuration: %w", err)
	}
	schema, err := schemas.Compile(kind)
	if err != nil {
		return fmt.Errorf("compile configuration schema: %w", err)
	}
	if err := schema.Validate(normalized); err != nil {
		// Schema errors can include input values, which may be confidential.
		return fmt.Errorf("configuration does not match the %s schema (required fields, types, ranges, or unknown keys)", kind)
	}
	return json.Unmarshal(data, target)
}

func expand(value any, lookup Lookup) (any, error) {
	return expandMode(value, lookup, false)
}

func expandMode(value any, lookup Lookup, deferred bool) (any, error) {
	switch value := value.(type) {
	case string:
		var missing string
		expanded := variable.ReplaceAllStringFunc(value, func(token string) string {
			name := variable.FindStringSubmatch(token)[1]
			resolved, ok := lookup(name)
			if !ok || resolved == "" {
				missing = name
			}
			return resolved
		})
		if missing != "" {
			return nil, fmt.Errorf("required environment variable %s is missing or empty", missing)
		}
		if strings.Contains(expanded, "${") && !deferred {
			return nil, errors.New("configuration contains an unresolved environment expression")
		}
		return expanded, nil
	case map[string]any:
		result := make(map[string]any, len(value))
		for _, key := range sortedKeys(value) {
			expanded, err := expandMode(value[key], lookup, deferred)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = expanded
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			expanded, err := expandMode(item, lookup, deferred)
			if err != nil {
				return nil, err
			}
			result[i] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func resource(value, expected string) (*arm.ResourceID, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "?#\r\n\t ") {
		return nil, errors.New("invalid ARM resource ID")
	}
	id, err := arm.ParseResourceID(value)
	if err != nil || id.SubscriptionID == "" || id.ResourceGroupName == "" || id.Parent == nil {
		return nil, errors.New("expected a subscription/resource-group-scoped ARM resource ID")
	}
	if expected != "" && !strings.EqualFold(id.ResourceType.String(), expected) {
		return nil, fmt.Errorf("expected ARM resource type %s", expected)
	}
	return id, nil
}

func validateScopeMode(settings Settings, deferred bool) error {
	workspace, err := resource(settings.WorkspaceResourceID, "Microsoft.Discovery/workspaces")
	if err != nil && !(deferred && variable.FindString(settings.WorkspaceResourceID) == settings.WorkspaceResourceID) {
		return fmt.Errorf("workspaceResourceId: %w", err)
	}
	project, err := resource(settings.ProjectResourceID, "Microsoft.Discovery/workspaces/projects")
	if err != nil && !(deferred && variable.FindString(settings.ProjectResourceID) == settings.ProjectResourceID) {
		return fmt.Errorf("projectResourceId: %w", err)
	}
	if project != nil && workspace != nil && !strings.EqualFold(project.Parent.String(), workspace.String()) {
		return errors.New("projectResourceId must belong to workspaceResourceId")
	}
	if settings.NodePoolResourceID != "" {
		if deferred && variable.FindString(settings.NodePoolResourceID) == settings.NodePoolResourceID {
			return nil
		}
		id, err := resource(settings.NodePoolResourceID, "Microsoft.Discovery/supercomputers/nodePools")
		if err != nil {
			return err
		}
		if workspace != nil && !strings.EqualFold(id.SubscriptionID, workspace.SubscriptionID) {
			return errors.New("node pool subscription differs from workspace subscription")
		}
	}
	return nil
}

func Outputs(host string) []string {
	switch host {
	case ToolHost:
		return []string{"resourceId", "image"}
	case AgentHost:
		return []string{"name", "version"}
	case KnowledgeBaseHost:
		return []string{"knowledgeBaseId", "name", "bookshelfResourceId", "indexingStatus", "runId"}
	case BookshelfHost:
		return []string{"resourceId", "knowledgeBaseId", "indexingStatus"}
	default:
		return nil
	}
}

func Alias(service, output string) string {
	words := map[string]string{
		"resourceId":      "RESOURCE_ID",
		"knowledgeBaseId": "KNOWLEDGE_BASE_ID", "indexingStatus": "INDEXING_STATUS",
		"bookshelfResourceId": "BOOKSHELF_RESOURCE_ID", "runId": "RUN_ID",
	}
	suffix, ok := words[output]
	if !ok {
		suffix = strings.ToUpper(output)
	}
	return "SERVICE_" + strings.ToUpper(separator.ReplaceAllString(service, "_")) + "_" + suffix
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
