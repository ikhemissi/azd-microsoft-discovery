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
	Definition     string             `json:"definition"`
	Model          *AgentModel        `json:"model"`
	Bindings       map[string]Binding `json:"bindings"`
	KnowledgeBases []Binding          `json:"knowledgeBases"`
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
