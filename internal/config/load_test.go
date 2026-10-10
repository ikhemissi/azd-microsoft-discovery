package config

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type projectRPC struct {
	azdext.UnimplementedProjectServiceServer
	document map[string]any
}

func (s *projectRPC) GetConfigSection(_ context.Context, request *azdext.GetProjectConfigSectionRequest) (*azdext.GetProjectConfigSectionResponse, error) {
	value, ok := s.document[request.Path].(map[string]any)
	if !ok {
		return &azdext.GetProjectConfigSectionResponse{}, nil
	}
	section, err := structpb.NewStruct(value)
	return &azdext.GetProjectConfigSectionResponse{Found: true, Section: section}, err
}

type environmentRPC struct {
	azdext.UnimplementedEnvironmentServiceServer
	mu   sync.Mutex
	name string
}

func (s *environmentRPC) GetValues(_ context.Context, _ *azdext.GetEnvironmentRequest) (*azdext.KeyValueListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &azdext.KeyValueListResponse{KeyValues: []*azdext.KeyValue{{Key: "MODEL", Value: s.name}}}, nil
}

func TestLoadRefreshesSDKEnvironmentAtBoundaries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AZD_EXEC_PROJECT_DIR", root)
	t.Setenv("MODEL", "wrong-process-value")
	document := agentDocument(t, root)
	document["services"].(map[string]any)["agent"] = map[string]any{
		"host": AgentHost, "config": map[string]any{"model": map[string]any{"deploymentName": "${MODEL}"}}}
	writeFile(t, root, "azure.yaml", "name: demo\n")
	writeFile(t, root, "agent.yaml", "name: researcher\nkind: prompt\ninstructions: Preserve ${LITERAL}.\n")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	env := &environmentRPC{}
	azdext.RegisterProjectServiceServer(server, &projectRPC{document: document})
	azdext.RegisterEnvironmentServiceServer(server, env)
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-finished; err != nil {
			t.Errorf("fixture RPC server: %v", err)
		}
	})
	client, err := azdext.NewAzdClient(azdext.WithAddress(listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if _, err := Load(t.Context(), client, "demo", true); err != nil {
		t.Fatalf("Package required outputs: %v", err)
	}
	if _, err := Load(t.Context(), client, "demo", false); err == nil {
		t.Fatal("Deploy fell back to process value despite empty azd value")
	}
	env.mu.Lock()
	env.name = "provisioned-chat"
	env.mu.Unlock()
	project, err := Load(t.Context(), client, "demo", false)
	if err != nil || project.Services["agent"].Agent.Model.DeploymentName != "provisioned-chat" {
		t.Fatalf("fresh provisioning output was not read: %v", err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 2 {
		t.Fatal("configuration loading changed local files")
	}
}
