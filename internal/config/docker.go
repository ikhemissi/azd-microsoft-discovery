package config

import (
	"errors"
	"os"
	"strings"
)

func validateDocker(project *Project, service *Service, value any, lookup Lookup, deferred bool) error {
	if value == nil {
		value = map[string]any{}
	}
	options, ok := value.(map[string]any)
	if !ok {
		return errors.New("build image mode requires core docker settings")
	}
	if _, exists := options["registry"]; !exists {
		return errors.New("build mode requires explicit docker.registry; Discovery does not provide a verified default publication registry")
	}
	buildOptions := make(map[string]any, len(options))
	for key, value := range options {
		if key != "registry" {
			buildOptions[key] = value
		}
	}
	expandedOptions, err := expand(buildOptions, lookup)
	if err != nil {
		return err
	}
	service.Docker = expandedOptions.(map[string]any)
	if registry, exists := options["registry"]; exists {
		// Provisioning may supply the registry after local packaging; it is not image identity.
		service.Docker["registry"] = registry
	}
	for _, key := range []string{"target", "platform", "image", "tag"} {
		if value, exists := options[key]; exists {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return errors.New("docker." + key + " must be a nonempty string")
			}
		}
	}
	for _, flag := range []string{"remoteBuild", "imagePassthrough"} {
		if enabled, exists := options[flag]; exists && enabled != false {
			return errors.New("this iteration supports local core container builds, not docker." + flag)
		}
	}
	for _, key := range []string{"path", "context"} {
		value, exists := options[key]
		if !exists {
			if key == "path" {
				value = "./Dockerfile"
			} else {
				value = "."
			}
		}
		expanded, err := expand(value, lookup)
		if err != nil {
			return err
		}
		path, ok := expanded.(string)
		if !ok {
			return errors.New("docker." + key + " must be a string")
		}
		resolved, err := ResolvePath(project.Root, service.Project, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(resolved)
		if err != nil || (key == "context" && !info.IsDir()) || (key == "path" && !info.Mode().IsRegular()) {
			return errors.New("docker." + key + " must resolve to an existing " + key)
		}
	}
	if registry, exists := options["registry"]; exists {
		expanded, err := expandMode(registry, lookup, deferred)
		if err != nil {
			return err
		}
		host, ok := expanded.(string)
		if !ok || strings.TrimSpace(host) == "" {
			return errors.New("docker.registry must be a nonempty registry hostname")
		}
		if !strings.Contains(host, "${") && (strings.ContainsAny(host, "/@?# \r\n") || !strings.HasSuffix(host, ".azurecr.io")) {
			return errors.New("this iteration publishes only to a public-cloud ACR hostname (not a URL)")
		}
		if actual, exists := lookup("AZURE_CONTAINER_REGISTRY_ENDPOINT"); exists && actual != "" && !strings.Contains(host, "${") && !strings.EqualFold(actual, host) {
			return errors.New("AZURE_CONTAINER_REGISTRY_ENDPOINT overrides docker.registry in core azd; make both values agree or remove the environment override")
		}
	}
	return nil
}
