package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/discovery"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
	"google.golang.org/protobuf/proto"
)

type Container interface {
	Build(context.Context, *azdext.ContainerBuildRequest) (*azdext.ContainerBuildResponse, error)
	Package(context.Context, *azdext.ContainerPackageRequest) (*azdext.ContainerPackageResponse, error)
	Publish(context.Context, *azdext.ContainerPublishRequest) (*azdext.ContainerPublishResponse, error)
}

type Dependencies struct {
	Load        func(context.Context, bool) (*config.Project, error)
	Container   Container
	Cloud       func(context.Context) (*discovery.Client, error)
	Digest      func(context.Context, string, string) (string, error)
	Environment func(context.Context) (string, error)
	Export      func(context.Context, string, string) error
}

type Provider struct {
	azdext.BaseServiceTargetProvider
	deps Dependencies
}

var _ azdext.ServiceTargetProvider = (*Provider)(nil)

func New(deps Dependencies) *Provider { return &Provider{deps: deps} }

type containerRPC struct{ client *azdext.AzdClient }

func (c containerRPC) Build(ctx context.Context, request *azdext.ContainerBuildRequest) (*azdext.ContainerBuildResponse, error) {
	return c.client.Container().Build(ctx, request)
}
func (c containerRPC) Package(ctx context.Context, request *azdext.ContainerPackageRequest) (*azdext.ContainerPackageResponse, error) {
	return c.client.Container().Package(ctx, request)
}
func (c containerRPC) Publish(ctx context.Context, request *azdext.ContainerPublishRequest) (*azdext.ContainerPublishResponse, error) {
	return c.client.Container().Publish(ctx, request)
}

func Register(host *azdext.ExtensionHost) {
	client := host.Client()
	deps := Dependencies{
		Load: func(ctx context.Context, packageOnly bool) (*config.Project, error) {
			return config.Load(ctx, client, os.Getenv("AZD_ENVIRONMENT"), packageOnly)
		},
		Container: containerRPC{client},
		Cloud: func(ctx context.Context) (*discovery.Client, error) {
			credential, err := newTokenProvider(ctx, client)
			if err != nil {
				return nil, err
			}
			return discovery.NewClient(credential), nil
		},
		Digest: func(ctx context.Context, image, imageHash string) (string, error) {
			credential, err := newTokenProvider(ctx, client)
			if err != nil {
				return "", err
			}
			registry := &discovery.Registry{Credential: credential}
			if imageHash != "" {
				return registry.ResolveBuilt(ctx, image, imageHash)
			}
			return registry.Resolve(ctx, image)
		},
		Environment: func(ctx context.Context) (string, error) {
			return currentEnvironment(ctx, client)
		},
		Export: func(ctx context.Context, key, value string) error {
			_, err := client.Environment().SetValue(ctx, &azdext.SetEnvRequest{
				EnvName: os.Getenv("AZD_ENVIRONMENT"), Key: key, Value: value,
			})
			return err
		},
	}
	for _, name := range []string{config.ToolHost, config.AgentHost} {
		host.WithServiceTarget(name, func() azdext.ServiceTargetProvider { return New(deps) })
	}
}

func (p *Provider) service(ctx context.Context, sc *azdext.ServiceConfig, packageOnly bool) (*config.Project, *config.Service, error) {
	if sc == nil {
		return nil, nil, errors.New("service configuration is required")
	}
	project, err := p.deps.Load(ctx, packageOnly)
	if err != nil {
		return nil, nil, err
	}
	service := project.Services[sc.Name]
	if service == nil || service.Host != sc.Host {
		return nil, nil, errors.New("fresh configuration no longer matches the requested service")
	}
	if service.Bookshelf != nil {
		return nil, nil, errors.New("bookshelf deployment contracts remain a separate unimplemented track")
	}
	return project, service, nil
}

func (p *Provider) Initialize(ctx context.Context, sc *azdext.ServiceConfig) error {
	_, _, err := p.service(ctx, sc, true)
	return err
}

func toolID(project *config.Project, service *config.Service) (string, error) {
	workspace, err := arm.ParseResourceID(project.Discovery.WorkspaceResourceID)
	if err != nil {
		return "", err
	}
	name, ok := service.Definition["name"].(string)
	if !ok || name == "" {
		return "", errors.New("tool definition has no name")
	}
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Discovery/tools/%s",
		workspace.SubscriptionID, workspace.ResourceGroupName, name), nil
}

func (p *Provider) GetTargetResource(ctx context.Context, subscription string, sc *azdext.ServiceConfig,
	_ func() (*azdext.TargetResource, error)) (*azdext.TargetResource, error) {
	project, service, err := p.service(ctx, sc, false)
	if err != nil {
		return nil, err
	}
	resourceID := project.Discovery.ProjectResourceID
	if service.Tool != nil {
		resourceID, err = toolID(project, service)
		if err != nil {
			return nil, err
		}
	}
	id, err := arm.ParseResourceID(resourceID)
	if err != nil {
		return nil, err
	}
	if subscription != "" && !strings.EqualFold(subscription, id.SubscriptionID) {
		return nil, errors.New("deployment subscription differs from configured Discovery scope")
	}
	return &azdext.TargetResource{
		SubscriptionId: id.SubscriptionID, ResourceGroupName: id.ResourceGroupName,
		ResourceName: id.Name, ResourceType: id.ResourceType.String(),
		Metadata: map[string]string{"resourceId": resourceID},
	}, nil
}

func containerArtifact(artifacts []*azdext.Artifact, kind azdext.LocationKind) (*azdext.Artifact, error) {
	var found *azdext.Artifact
	for _, artifact := range artifacts {
		if artifact == nil {
			return nil, errors.New("nil lifecycle artifact")
		}
		if artifact.Kind != azdext.ArtifactKind_ARTIFACT_KIND_CONTAINER {
			continue
		}
		if artifact.Location == "" || artifact.LocationKind != kind {
			return nil, errors.New("container lifecycle artifact has an invalid location")
		}
		if found != nil && found.Location != artifact.Location {
			return nil, errors.New("multiple distinct container lifecycle artifacts")
		}
		found = artifact
	}
	return found, nil
}

func imageArtifact(image string) *azdext.Artifact {
	return &azdext.Artifact{
		Kind: azdext.ArtifactKind_ARTIFACT_KIND_CONTAINER, Location: image,
		LocationKind: azdext.LocationKind_LOCATION_KIND_REMOTE,
		Metadata:     map[string]string{"remoteImage": image},
	}
}

func stampPackage(service *config.Service, artifact *azdext.Artifact) (*azdext.ServicePackageResult, error) {
	imageHash := artifact.Metadata["imageHash"]
	if !strings.HasPrefix(imageHash, "sha256:") {
		imageHash = "sha256:" + imageHash
	}
	if err := config.ValidateImage("example.azurecr.io/config@" + imageHash); err != nil {
		return nil, errors.New("native package artifact lacks a full imageHash; refusing an unverifiable build result")
	}
	fingerprint, err := sourceHash(service)
	if err != nil {
		return nil, err
	}
	copy := proto.Clone(artifact).(*azdext.Artifact)
	if copy.Metadata == nil {
		copy.Metadata = map[string]string{}
	}
	copy.Metadata["discoverySource"] = fingerprint
	return &azdext.ServicePackageResult{Artifacts: []*azdext.Artifact{copy}}, nil
}

func (p *Provider) Package(ctx context.Context, sc *azdext.ServiceConfig, input *azdext.ServiceContext,
	_ azdext.ProgressReporter) (*azdext.ServicePackageResult, error) {
	_, service, err := p.service(ctx, sc, true)
	if err != nil {
		return nil, err
	}
	if service.Tool == nil {
		return &azdext.ServicePackageResult{}, nil
	}
	if service.Tool.Image.Mode == "reference" {
		if err := config.ValidateImage(service.Tool.Image.Reference); err != nil {
			if strings.HasPrefix(service.Tool.Image.Reference, "${") && strings.HasSuffix(service.Tool.Image.Reference, "}") {
				return &azdext.ServicePackageResult{}, nil
			}
			return nil, err
		}
		return &azdext.ServicePackageResult{Artifacts: []*azdext.Artifact{imageArtifact(service.Tool.Image.Reference)}}, nil
	}
	serviceContext := &azdext.ServiceContext{}
	if input != nil {
		serviceContext = proto.Clone(input).(*azdext.ServiceContext)
	}
	// Core's docker framework normally already built AND packaged before this target is called.
	existing, err := containerArtifact(serviceContext.Package, azdext.LocationKind_LOCATION_KIND_LOCAL)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		stamped, err := stampPackage(service, existing)
		if err != nil {
			return nil, err
		}
		stamp := stamped.Artifacts[0]
		stamp.Kind = azdext.ArtifactKind_ARTIFACT_KIND_CONFIG
		stamp.Metadata["discoveryPackageImage"] = existing.Location
		stamp.Location = service.Tool.Definition
		if !filepath.IsAbs(stamp.Location) {
			stamp.Location = filepath.Join(service.Project, stamp.Location)
		}
		return stamped, nil
	}
	build, err := containerArtifact(serviceContext.Build, azdext.LocationKind_LOCATION_KIND_LOCAL)
	if err != nil {
		return nil, err
	}
	if build == nil {
		response, err := p.deps.Container.Build(ctx, &azdext.ContainerBuildRequest{ServiceName: sc.Name, ServiceContext: serviceContext})
		if err != nil {
			return nil, fmt.Errorf("native container build failed: %w", err)
		}
		if response == nil || response.Result == nil {
			return nil, errors.New("native container build returned no result")
		}
		serviceContext.Build = response.Result.Artifacts
		build, err = containerArtifact(serviceContext.Build, azdext.LocationKind_LOCATION_KIND_LOCAL)
		if err != nil || build == nil {
			return nil, errors.New("native container build returned no image artifact")
		}
	}
	response, err := p.deps.Container.Package(ctx, &azdext.ContainerPackageRequest{ServiceName: sc.Name, ServiceContext: serviceContext})
	if err != nil {
		return nil, fmt.Errorf("native container package failed: %w", err)
	}
	if response == nil || response.Result == nil {
		return nil, errors.New("native container package returned no result")
	}
	artifact, err := containerArtifact(response.Result.Artifacts, azdext.LocationKind_LOCATION_KIND_LOCAL)
	if err != nil || artifact == nil {
		return nil, errors.New("native container package returned no image artifact")
	}
	return stampPackage(service, artifact)
}

func (p *Provider) Publish(ctx context.Context, sc *azdext.ServiceConfig, input *azdext.ServiceContext,
	_ *azdext.TargetResource, options *azdext.PublishOptions, progress azdext.ProgressReporter) (*azdext.ServicePublishResult, error) {
	_, service, err := p.service(ctx, sc, false)
	if err != nil {
		return nil, err
	}
	if service.Tool == nil {
		return &azdext.ServicePublishResult{}, nil
	}
	if options != nil && options.Image != "" {
		return nil, errors.New("azd 1.35.0 Container.Publish RPC cannot forward --to; configure core docker.image/tag/registry instead")
	}
	if service.Tool.Image.Mode == "reference" {
		if input != nil {
			artifact, err := containerArtifact(input.Package, azdext.LocationKind_LOCATION_KIND_REMOTE)
			if err != nil || (artifact != nil && artifact.Location != service.Tool.Image.Reference) {
				return nil, errors.New("reference package differs from current immutable image configuration")
			}
		}
		return &azdext.ServicePublishResult{Artifacts: []*azdext.Artifact{imageArtifact(service.Tool.Image.Reference)}}, nil
	}
	if input == nil {
		return nil, errors.New("publish requires the native container package artifact")
	}
	artifact, err := containerArtifact(input.Package, azdext.LocationKind_LOCATION_KIND_LOCAL)
	if err != nil || artifact == nil {
		return nil, errors.New("publish requires exactly one native local container package")
	}
	fingerprint, err := sourceHash(service)
	if err != nil {
		return nil, err
	}
	packagedSource := artifact.Metadata["discoverySource"]
	for _, candidate := range input.Package {
		if candidate.Kind == azdext.ArtifactKind_ARTIFACT_KIND_CONFIG &&
			candidate.Metadata["discoveryPackageImage"] == artifact.Location &&
			candidate.Metadata["imageHash"] == artifact.Metadata["imageHash"] {
			packagedSource = candidate.Metadata["discoverySource"]
		}
	}
	if packagedSource != fingerprint {
		return nil, errors.New("container package belongs to stale configuration; repackage before publishing")
	}
	if progress != nil {
		progress("Publishing native container image")
	}
	response, err := p.deps.Container.Publish(ctx, &azdext.ContainerPublishRequest{ServiceName: sc.Name, ServiceContext: input})
	if err != nil {
		return nil, fmt.Errorf("native ACR publication failed; tool was not registered: %w", err)
	}
	if response == nil || response.Result == nil {
		return nil, errors.New("native container publication returned no result")
	}
	published, err := containerArtifact(response.Result.Artifacts, azdext.LocationKind_LOCATION_KIND_REMOTE)
	if err != nil || published == nil {
		return nil, errors.New("native publication returned no remote image artifact")
	}
	if progress != nil {
		progress("Verifying immutable ACR manifest and packaged image identity")
	}
	image, err := p.deps.Digest(ctx, published.Location, artifact.Metadata["imageHash"])
	if err != nil {
		return nil, err
	}
	if err := config.ValidateImage(image); err != nil {
		return nil, err
	}
	immutable := imageArtifact(image)
	immutable.Metadata["discoverySource"] = fingerprint
	return &azdext.ServicePublishResult{Artifacts: []*azdext.Artifact{immutable}}, nil
}

func sourceHash(service *config.Service) (string, error) {
	var settings any
	switch service.Host {
	case config.ToolHost:
		settings = service.Tool
	case config.AgentHost:
		settings = service.Agent
	}
	return state.Hash(struct {
		Host       string
		Config     any
		Definition map[string]any
		Docker     map[string]any
		Project    string
	}{service.Host, settings, service.Definition, service.Docker, service.Project})
}
