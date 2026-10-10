package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/spf13/cobra"
)

type projectLoader func(context.Context, string, bool) (*config.Project, error)

type report struct {
	SchemaVersion       string        `json:"schemaVersion"`
	Command             string        `json:"command"`
	Status              string        `json:"status"`
	Source              string        `json:"source"`
	ValidationScope     string        `json:"validationScope"`
	DeploymentSupported bool          `json:"deploymentSupported"`
	Project             string        `json:"project,omitempty"`
	Checks              []check       `json:"checks"`
	Services            []servicePlan `json:"services"`
}

type check struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type servicePlan struct {
	Name               string   `json:"name"`
	Host               string   `json:"host"`
	Ownership          string   `json:"ownership"`
	Uses               []string `json:"uses"`
	Intent             string   `json:"intent"`
	Action             string   `json:"action"`
	ImagePublication   string   `json:"imagePublication,omitempty"`
	UnresolvedBindings []string `json:"unresolvedBindings"`
	ExpectedOutputs    []string `json:"expectedOutputs"`
}

func newInspectCommand(name string, extension *azdext.ExtensionContext, load projectLoader) *cobra.Command {
	var local bool
	short := "Inspect intended Discovery deployment operations without making changes"
	if name == "doctor" {
		short = "Validate local Discovery configuration; Azure checks are not yet implemented"
	}
	command := &cobra.Command{
		Use:   name,
		Short: short,
		Long:  short + ". Success means local configuration is valid, not that Azure resources are ready or deployed.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if local && command.Flags().Changed("environment") {
				return errors.New("--local does not load azd environments; omit --environment and supply process environment variables")
			}
			result := report{
				SchemaVersion: "1.0", Command: name, Status: "valid", Source: "azd",
				ValidationScope: "local", Services: []servicePlan{},
			}
			if local {
				result.Source = "local"
			}
			project, loadErr := load(command.Context(), extension.Environment, local)
			if loadErr != nil {
				result.Status = "blocked"
				result.Checks = []check{{Name: "configuration", State: "blocked", Message: loadErr.Error()}}
			} else {
				result.Project = project.Name
				result.DeploymentSupported = true
				for _, service := range project.Services {
					if service.Bookshelf != nil || (service.Agent != nil && len(service.Agent.KnowledgeBases) > 0) {
						result.DeploymentSupported = false
					}
				}
				result.Checks = []check{
					{Name: "configuration", State: "passed", Message: "Local schemas, paths, definitions, supported scope checks, and binding declarations validated."},
					{Name: "azureReadiness", State: "notChecked", Message: "Authentication, remote resources, RBAC, registry access, networking, and indexing readiness are not checked in this iteration."},
					{Name: "nativeDeployment", State: "implemented", Message: "Tool/agent lifecycle providers are registered and offline-tested. Live ACR/Discovery deployment remains unverified; KB support is deferred."},
				}
				result.Services = summarize(project)
			}
			if err := writeReport(command.OutOrStdout(), extension.OutputFormat, result); err != nil {
				return err
			}
			if loadErr != nil {
				return errors.New("Discovery configuration is blocked; see the report for details")
			}
			_, err := fmt.Fprintln(command.ErrOrStderr(), "Warning: local validation only; Azure readiness is not checked and live deployment is not certified.")
			return err
		},
	}
	command.Flags().BoolVar(&local, "local", false, "Read azure.yaml locally using only process environment values; do not connect to azd")
	return azdext.RegisterFlagOptions(command, azdext.FlagOptions{
		Name: "output", AllowedValues: []string{"json", "default"},
	})
}

func summarize(project *config.Project) []servicePlan {
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	result := []servicePlan{}
	for _, name := range names {
		service := project.Services[name]
		outputs := config.Outputs(service.Host)
		if len(outputs) == 0 {
			continue
		}
		item := servicePlan{
			Name: name, Host: service.Host, Ownership: "reference",
			Uses: append([]string{}, service.Uses...), Action: "unresolved",
			Intent: "verifyReference", UnresolvedBindings: []string{}, ExpectedOutputs: outputs,
		}
		switch service.Host {
		case config.ToolHost:
			item.Ownership, item.Intent = "managedApplication", "registerTool"
			item.ImagePublication = "none"
			if service.Tool.Image.Mode == "build" {
				item.ImagePublication = "buildAndPublish"
			}
		case config.AgentHost:
			item.Ownership, item.Intent = "managedApplication", "reconcileAgent"
			for key := range service.Agent.Bindings {
				item.UnresolvedBindings = append(item.UnresolvedBindings, key)
			}
			for _, binding := range service.Agent.KnowledgeBases {
				item.UnresolvedBindings = append(item.UnresolvedBindings, binding.Service+"."+binding.Output)
			}
			slices.Sort(item.UnresolvedBindings)
		}
		result = append(result, item)
	}
	return result
}

func writeReport(writer io.Writer, format string, result report) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if _, err := fmt.Fprintf(writer, "Discovery %s: %s (local validation only)\n", result.Command, result.Status); err != nil {
		return err
	}
	for _, check := range result.Checks {
		if _, err := fmt.Fprintf(writer, "  %s: %s - %s\n", check.Name, check.State, check.Message); err != nil {
			return err
		}
	}
	for _, service := range result.Services {
		if _, err := fmt.Fprintf(writer, "  %s (%s): %s, %s; action %s\n",
			service.Name, service.Host, service.Ownership, service.Intent, service.Action); err != nil {
			return err
		}
	}
	return nil
}
