package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/cmd"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func validateReleaseTag(tag, version string) error {
	if _, err := semver.StrictNewVersion(version); err != nil {
		return fmt.Errorf("extension version %q is not a semantic version", version)
	}
	if tag != "v"+version {
		return fmt.Errorf("release tag %q must match extension version v%s", tag, version)
	}
	return nil
}

func TestManifestVersionMatchesBinaryDefault(t *testing.T) {
	manifest, err := config.ReadDocument("extension.yaml")
	if err != nil {
		t.Fatal(err)
	}
	version, ok := manifest["version"].(string)
	if !ok || version != cmd.Version {
		t.Fatalf("extension.yaml version %v must match internal/cmd/version.go default %s", manifest["version"], cmd.Version)
	}
	if err := validateReleaseTag("v"+version, version); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVersionMatchesTag(t *testing.T) {
	if os.Getenv("GITHUB_REF_TYPE") != "tag" {
		t.Skip("release version/tag check runs on tag workflows")
	}
	if err := validateReleaseTag(os.Getenv("GITHUB_REF_NAME"), cmd.Version); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseTagValidation(t *testing.T) {
	for _, test := range []struct {
		tag, version string
		valid        bool
	}{
		{"v0.1.0-preview.1", "0.1.0-preview.1", true},
		{"v1.2.3", "1.2.3", true},
		{"v1.2.3+build.1", "1.2.3+build.1", true},
		{"v0.1.0-preview.2", "0.1.0-preview.1", false},
		{"0.1.0-preview.1", "0.1.0-preview.1", false},
		{"v1.2", "1.2", false},
		{"v01.2.3", "01.2.3", false},
		{"v1.2.3-01", "1.2.3-01", false},
		{"v1.2.3-unsafe/name", "1.2.3-unsafe/name", false},
	} {
		if err := validateReleaseTag(test.tag, test.version); (err == nil) != test.valid {
			t.Errorf("tag=%q version=%q: %v", test.tag, test.version, err)
		}
	}
}

func TestLinuxReleaseBundle(t *testing.T) {
	path := os.Getenv("DISCOVERY_RELEASE_BUNDLE")
	if path == "" {
		t.Skip("set DISCOVERY_RELEASE_BUNDLE to validate a built Linux bundle")
	}
	if filepath.Base(path) != "ikhemissi-azd-discovery_"+cmd.Version+".zip" {
		t.Fatal("bundle filename does not match the extension version")
	}
	bundle, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bundle.Close(); err != nil {
			t.Errorf("close bundle: %v", err)
		}
	})
	var registry struct {
		Extensions []struct {
			ID       string `json:"id"`
			Versions []struct {
				Version   string `json:"version"`
				Artifacts map[string]struct {
					URL        string `json:"url"`
					EntryPoint string `json:"entryPoint"`
					Checksum   struct {
						Algorithm string `json:"algorithm"`
						Value     string `json:"value"`
					} `json:"checksum"`
				} `json:"artifacts"`
			} `json:"versions"`
		} `json:"extensions"`
	}
	files := map[string]*zip.File{}
	for _, file := range bundle.File {
		if _, exists := files[file.Name]; exists {
			t.Fatalf("duplicate bundle entry: %s", file.Name)
		}
		files[file.Name] = file
	}
	manifest, exists := files["registry.json"]
	if !exists {
		t.Fatal("bundle is missing registry.json")
	}
	reader, err := manifest.Open()
	if err != nil {
		t.Fatal(err)
	}
	decodeErr := json.NewDecoder(reader).Decode(&registry)
	closeErr := reader.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatalf("read bundle registry: %v %v", decodeErr, closeErr)
	}
	if len(registry.Extensions) != 1 || registry.Extensions[0].ID != "ikhemissi.azd.discovery" ||
		len(registry.Extensions[0].Versions) != 1 {
		t.Fatal("bundle must contain exactly this extension and version")
	}
	version := registry.Extensions[0].Versions[0]
	if version.Version != cmd.Version || len(version.Artifacts) != 1 {
		t.Fatal("bundle version or platform count is incorrect")
	}
	artifact, exists := version.Artifacts["linux/amd64"]
	if !exists || artifact.Checksum.Algorithm != "sha256" ||
		artifact.EntryPoint != "ikhemissi-azd-discovery-linux-amd64" || strings.Contains(artifact.URL, "://") {
		t.Fatal("bundle must contain a local Linux amd64 artifact with SHA-256 identity")
	}
	archive, exists := files[artifact.URL]
	if !exists || len(files) != 2 {
		t.Fatal("bundle must contain registry.json and exactly its referenced platform archive")
	}
	reader, err = archive.Open()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, reader)
	closeErr = reader.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("hash bundle artifact: %v %v", copyErr, closeErr)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.Checksum.Value {
		t.Fatal("bundled artifact checksum differs from registry metadata")
	}
}
