package cmd

import (
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/project"
)

func TestConfigureExtensionHostMatchesManifest(t *testing.T) {
	if err := azdext.VerifyProvidersMatchManifest(project.Register, "../../extension.yaml"); err != nil {
		t.Fatal(err)
	}
}
