package data

import (
	"errors"
	"slices"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

type Handoff struct {
	Status                 string   `json:"status"`
	Dataset                string   `json:"dataset"`
	ProjectResourceID      string   `json:"projectResourceId"`
	StorageAssetResourceID string   `json:"storageAssetResourceId"`
	ResourceURI            string   `json:"resourceUri"`
	BlobPrefixURI          string   `json:"blobPrefixUri"`
	AssetVerified          bool     `json:"assetVerified"`
	BlobVerified           bool     `json:"blobVerified"`
	ConversationAttached   bool     `json:"conversationAttached"`
	AgentService           string   `json:"agentService,omitempty"`
	AgentName              string   `json:"agentName,omitempty"`
	ToolServices           []string `json:"toolServices,omitempty"`
	NextStep               string   `json:"nextStep"`
}

func AgentTools(project *config.Project, agent string) ([]string, error) {
	service := project.Services[agent]
	if service == nil || service.Host != config.AgentHost || service.Agent == nil {
		return nil, errors.New("--agent must select a configured discovery.agent service")
	}
	tools := []string{}
	for _, binding := range service.Agent.Bindings {
		target := project.Services[binding.Service]
		if target == nil || target.Host != config.ToolHost {
			return nil, errors.New("agent binding does not identify a configured Discovery tool")
		}
		if !slices.Contains(tools, binding.Service) {
			tools = append(tools, binding.Service)
		}
	}
	slices.Sort(tools)
	return tools, nil
}

func Describe(project *config.Project, dataset, agent string, destination Destination) (Handoff, error) {
	if destination.StorageAssetID == "" || destination.ResourceURI == "" {
		return Handoff{}, errors.New("data show requires an existing IaC-owned storageAssetResourceId; provision it first")
	}
	result := Handoff{Status: "assetVerified", Dataset: dataset, ProjectResourceID: project.Discovery.ProjectResourceID,
		StorageAssetResourceID: destination.StorageAssetID, ResourceURI: destination.ResourceURI, BlobPrefixURI: destination.URI,
		AssetVerified: true,
		NextStep:      "Attach this storage asset to the intended conversation/task, select it in your request, and have the agent use GetResourceContext before supplying the tool's inputMounts/outputMounts. This command does not verify Blob payloads, agent deployment or attachment."}
	if agent != "" {
		tools, err := AgentTools(project, agent)
		if err != nil {
			return Handoff{}, err
		}
		result.AgentService = agent
		result.AgentName, _ = project.Services[agent].Definition["name"].(string)
		result.ToolServices = tools
	}
	return result, nil
}
