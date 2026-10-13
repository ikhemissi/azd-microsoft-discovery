package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
)

func TestMetadataSurvivesSDKCacheRoundtrip(t *testing.T) {
	root := NewRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"metadata"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var original extensions.ExtensionCommandMetadata
	if err := json.Unmarshal(output.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	cached, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var metadata extensions.ExtensionCommandMetadata
	if err := json.Unmarshal(cached, &metadata); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []*extensions.ConfigurationMetadata{original.Configuration, metadata.Configuration} {
		if schema == nil {
			t.Fatal("configuration metadata missing")
		}
		for _, entry := range []struct {
			name    string
			compile func() error
		}{
			{"project", func() error { _, err := extensions.CompileSchema(schema.Project); return err }},
			{"service", func() error { _, err := extensions.CompileSchema(schema.Service); return err }},
		} {
			if err := entry.compile(); err != nil {
				t.Fatalf("%s failed SDK roundtrip: %v", entry.name, err)
			}
		}
		if err := extensions.ValidateAgainstSchema(schema.Service, map[string]any{"model": map[string]any{"deploymentName": "chat"}}); err != nil {
			t.Fatal(err)
		}
		if err := extensions.ValidateAgainstSchema(schema.Service, map[string]any{"knowledgeBases": []any{map[string]any{"service": "literature"}}}); err != nil {
			t.Fatalf("KB attachment references did not survive metadata caching: %v", err)
		}
		if err := extensions.ValidateAgainstSchema(schema.Service, map[string]any{"knowledgeBases": []any{"literal-id"}}); err == nil {
			t.Fatal("metadata accepted an unverified literal KB attachment")
		}
		if err := extensions.ValidateAgainstSchema(schema.Service, map[string]any{
			"bookshelfName": "${BOOKSHELF}", "sources": []any{map[string]any{"storageAssetResourceId": "${ASSET}"}}}); err != nil {
			t.Fatalf("KB metadata/default inputs did not survive caching: %v", err)
		}
		if err := extensions.ValidateAgainstSchema(schema.Project, map[string]any{
			"schemaVersion": "1.0", "workspaceResourceId": "${WORKSPACE}", "projectResourceId": "${PROJECT}",
			"data": map[string]any{
				"sources":  map[string]any{"inputs": map[string]any{"storageContainerResourceId": "${REGISTRATION}", "blobContainerResourceId": "${BLOB}", "prefix": "approved"}},
				"datasets": map[string]any{"example": map[string]any{"source": "inputs", "directory": "./inputs", "namespace": "experiment", "version": "v1", "overwrite": false, "storageAssetResourceId": "${ASSET}"}},
			},
		}); err != nil {
			t.Fatalf("data metadata did not survive caching: %v", err)
		}
	}
}
