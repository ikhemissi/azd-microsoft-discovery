package config

const (
	ToolHost          = "discovery.tool"
	AgentHost         = "discovery.agent"
	KnowledgeBaseHost = "discovery.knowledgebase"
	BookshelfHost     = "discovery.bookshelf"
)

type Project struct {
	Name      string
	Root      string
	Discovery Settings
	Services  map[string]*Service
}

type Settings struct {
	SchemaVersion           string `json:"schemaVersion"`
	WorkspaceResourceID     string `json:"workspaceResourceId"`
	ProjectResourceID       string `json:"projectResourceId"`
	NodePoolResourceID      string `json:"nodePoolResourceId"`
	OperationTimeoutSeconds int    `json:"operationTimeoutSeconds"`
	Data                    *Data  `json:"data,omitempty"`
}

type Data struct {
	Sources  map[string]DataSource `json:"sources"`
	Datasets map[string]Dataset    `json:"datasets"`
}

type DataSource struct {
	StorageContainerResourceID string `json:"storageContainerResourceId"`
	BlobContainerResourceID    string `json:"blobContainerResourceId"`
	Prefix                     string `json:"prefix"`
}

type Dataset struct {
	Source                 string `json:"source"`
	Directory              string `json:"directory"`
	Manifest               string `json:"manifest"`
	Namespace              string `json:"namespace"`
	Version                string `json:"version"`
	Overwrite              bool   `json:"overwrite"`
	StorageAssetResourceID string `json:"storageAssetResourceId,omitempty"`
}

type Service struct {
	Name          string
	Host          string
	Project       string
	Uses          []string
	Tool          *Tool
	Agent         *Agent
	KnowledgeBase *KnowledgeBase
	Bookshelf     *Bookshelf
	Definition    map[string]any
	Docker        map[string]any
}

type KnowledgeBase struct {
	Name               string                `json:"name"`
	BookshelfName      string                `json:"bookshelfName"`
	SubscriptionID     string                `json:"subscriptionId"`
	ResourceGroup      string                `json:"resourceGroup"`
	Description        string                `json:"description"`
	CopilotInstruction string                `json:"copilotInstruction"`
	Sources            []KnowledgeBaseSource `json:"sources"`
	Autoindex          *bool                 `json:"autoindex"`
	NodePoolResourceID string                `json:"nodePoolResourceId"`
}

type KnowledgeBaseSource struct {
	StorageAssetResourceID string `json:"storageAssetResourceId"`
}

type Image struct {
	Mode      string `json:"mode"`
	InfraNode string `json:"infraNode"`
	Reference string `json:"reference"`
}

type Tool struct {
	Type       string `json:"type"`
	Definition string `json:"definition"`
	Image      Image  `json:"image"`
}

type Binding struct {
	Service string `json:"service"`
	Output  string `json:"output"`
}

type Agent struct {
	Definition     string                 `json:"definition"`
	Model          *AgentModel            `json:"model"`
	Bindings       map[string]Binding     `json:"bindings"`
	KnowledgeBases []KnowledgeBaseBinding `json:"knowledgeBases"`
}

type KnowledgeBaseBinding struct {
	Service string `json:"service"`
}

type AgentModel struct {
	DeploymentName string `json:"deploymentName"`
}

type Bookshelf struct {
	Mode            string    `json:"mode"`
	ResourceID      string    `json:"resourceId"`
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	Source          *Source   `json:"source"`
	Indexing        *Indexing `json:"indexing"`
}

type Source struct {
	StorageAssetResourceID string `json:"storageAssetResourceId"`
}

type Indexing struct {
	NodePoolResourceID string `json:"nodePoolResourceId"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
}
