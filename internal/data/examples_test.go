package data

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func TestPublishedBundleExamplesHaveRealChecksums(t *testing.T) {
	root, err := filepath.Abs("../../examples/reusable-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"experiment-a", "experiment-b"} {
		project := &config.Project{Root: root, Discovery: config.Settings{Data: &config.Data{
			Datasets: map[string]config.Dataset{name: {Directory: "bundles/" + name, Manifest: "bundle.yaml"}},
		}}}
		bundle, err := Validate(t.Context(), project, name)
		if err != nil {
			t.Fatalf("example %s is not valid: %v", name, err)
		}
		if err := bundle.Root.Close(); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile("../../docs/data-bundles.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 {
		t.Fatal("missing runtime mount and attachment documentation")
	}
}
