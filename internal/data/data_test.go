package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func fixture(t *testing.T) (*config.Project, *Bundle, Destination) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".azure", "demo"), 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "inputs")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("reviewed experiment data\n")
	sum := sha256.Sum256(content)
	manifest, err := json.Marshal(Manifest{SchemaVersion: "1.0", Files: []Entry{
		{Path: "experiment.json", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"experiment.json": content, "bundle.yaml": manifest} {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	project := &config.Project{Root: root, Discovery: config.Settings{Data: &config.Data{
		Sources:  map[string]config.DataSource{"inputs": {Prefix: "approved"}},
		Datasets: map[string]config.Dataset{"example": {Source: "inputs", Directory: "inputs", Manifest: "bundle.yaml", Namespace: "experiment", Version: "v1"}},
	}}}
	bundle, err := Validate(t.Context(), project, "example")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bundle.Root.Close(); err != nil {
			t.Error(err)
		}
	})
	return project, bundle, Destination{ContainerID: "verified-container-id", Prefix: "approved/experiment/v1",
		URI: "https://example.blob.core.windows.net/inputs/approved/experiment/v1/"}
}

type memoryBlobs struct {
	objects                          map[string][]byte
	creates                          int
	failEntry, corruptEntry, lostAck bool
}

func (m *memoryBlobs) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := []string{}
	for name := range m.objects {
		if strings.HasPrefix(name, prefix) {
			result = append(result, name)
		}
	}
	return result, nil
}
func (m *memoryBlobs) Read(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content, exists := m.objects[name]
	if !exists {
		return nil, ErrMissing
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}
func (m *memoryBlobs) Create(ctx context.Context, name string, reader io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := m.objects[name]; exists {
		return ErrExists
	}
	if m.failEntry && strings.HasSuffix(name, "experiment.json") {
		return errors.New("offline interruption")
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if m.corruptEntry && strings.HasSuffix(name, "experiment.json") {
		content[0] ^= 1
	}
	m.objects[name] = content
	m.creates++
	if m.lostAck {
		return errors.New("offline acknowledgement lost")
	}
	return nil
}

func TestBundleUploadDryRunCompletionAndResume(t *testing.T) {
	for _, failure := range []string{"none", "interrupted", "lost acknowledgement"} {
		t.Run(failure, func(t *testing.T) {
			project, bundle, destination := fixture(t)
			remote := &memoryBlobs{objects: map[string][]byte{}}
			result, err := Upload(t.Context(), project, "demo", bundle, destination, remote, true)
			if err != nil || result.Status != "dryRun" || result.BlobVerified || remote.creates != 0 {
				t.Fatalf("dry-run did not stay read-only: %+v (%v)", result, err)
			}
			if _, err := os.Stat(filepath.Join(project.Root, ".azure", "demo", "discovery")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("dry-run created recovery state")
			}
			remote.failEntry, remote.lostAck = failure == "interrupted", failure == "lost acknowledgement"
			result, err = Upload(t.Context(), project, "demo", bundle, destination, remote, false)
			if failure == "interrupted" {
				if err == nil || remote.objects[destination.Prefix+"/"+completeName] != nil {
					t.Fatal("partial upload was marked complete")
				}
				remote.failEntry = false
				result, err = Upload(t.Context(), project, "demo", bundle, destination, remote, false)
			}
			if err != nil || !result.BlobVerified || result.ConversationAttached || result.DiscoveryAttachment != "required" ||
				len(remote.objects) != len(bundle.Entries)+2 {
				t.Fatalf("verified upload failed or claimed attachment: %+v (%v)", result, err)
			}
			writes := remote.creates
			if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, false); err != nil || writes != remote.creates {
				t.Fatalf("completed upload was overwritten on resume: %v", err)
			}
		})
	}
}

func TestUploadCollisionsCorruptionAndChangedLocalInput(t *testing.T) {
	for _, failure := range []string{"foreign prefix", "corrupt remote", "changed local", "changed fingerprint", "long path"} {
		t.Run(failure, func(t *testing.T) {
			project, bundle, destination := fixture(t)
			remote := &memoryBlobs{objects: map[string][]byte{}}
			switch failure {
			case "foreign prefix":
				remote.objects[destination.Prefix+"/customer-file"] = []byte("private")
				if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, true); err == nil {
					t.Fatal("dry-run accepted a foreign immutable prefix")
				}
			case "corrupt remote":
				remote.corruptEntry = true
			case "changed local":
				if err := os.WriteFile(filepath.Join(project.Root, "inputs", "experiment.json"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed fingerprint":
				if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, false); err != nil {
					t.Fatal(err)
				}
				bundle.Fingerprint = strings.Repeat("0", 64)
			case "long path":
				destination.Prefix = strings.Repeat("x", 1024)
			}
			if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, false); err == nil {
				t.Fatal("unsafe upload succeeded")
			}
			if failure != "changed fingerprint" && remote.objects[destination.Prefix+"/"+completeName] != nil {
				t.Fatal("failed upload published completion")
			}
			if failure == "foreign prefix" && remote.creates != 0 {
				t.Fatal("foreign customer data was modified")
			}
			snapshots, err := filepath.Glob(filepath.Join(project.Root, ".azure", "demo", "discovery", ".upload-payload-*"))
			if err != nil || len(snapshots) != 0 {
				t.Fatal("private snapshots were not cleaned up")
			}
		})
	}
}

func TestBundleValidationRejectsUnsafeAndIncompleteInputs(t *testing.T) {
	for _, failure := range []string{"traversal", "absolute", "duplicate", "case collision", "missing", "checksum", "size", "version", "command", "symlink", "extra", "parent symlink", "excessive", "total limit"} {
		t.Run(failure, func(t *testing.T) {
			project, bundle, _ := fixture(t)
			entry := bundle.Entries[1]
			if entry.Path != "experiment.json" {
				t.Fatal("fixture order changed")
			}
			document := map[string]any{"schemaVersion": "1.0", "files": []any{map[string]any{
				"path": entry.Path, "size": entry.Size, "sha256": entry.SHA256,
			}}}
			file := document["files"].([]any)[0].(map[string]any)
			switch failure {
			case "traversal":
				file["path"] = "../outside"
			case "absolute":
				file["path"] = "/outside"
			case "duplicate":
				document["files"] = []any{file, file}
			case "case collision":
				document["files"] = []any{file, map[string]any{"path": "EXPERIMENT.json", "size": entry.Size, "sha256": entry.SHA256}}
			case "missing":
				file["path"] = "missing"
			case "checksum":
				file["sha256"] = strings.Repeat("0", 64)
			case "size":
				file["size"] = entry.Size + 1
			case "version":
				document["schemaVersion"] = "2.0"
			case "command":
				document["command"] = "never execute"
			case "symlink":
				if err := os.Remove(filepath.Join(project.Root, "inputs", entry.Path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/passwd", filepath.Join(project.Root, "inputs", entry.Path)); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				if err := os.Symlink(project.Root, filepath.Join(project.Root, "inputs", "alias")); err != nil {
					t.Fatal(err)
				}
				file["path"] = "alias/inputs/experiment.json"
			case "extra":
				if err := os.WriteFile(filepath.Join(project.Root, "inputs", "UNDECLARED"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "excessive":
				file["size"] = int64(1<<30) + 1
			case "total limit":
				file["size"] = int64(1 << 30)
				document["files"] = []any{file, map[string]any{"path": "second.bin", "size": int64(1 << 30), "sha256": entry.SHA256}}
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project.Root, "inputs", "bundle.yaml"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			candidate, validationErr := Validate(t.Context(), project, "example")
			if validationErr == nil {
				candidate.Root.Close()
				t.Fatal("unsafe/incomplete bundle was accepted")
			}
			if failure == "total limit" && !strings.Contains(validationErr.Error(), "2 GiB") {
				t.Fatal("total size was not rejected before filesystem reads")
			}
		})
	}
}
