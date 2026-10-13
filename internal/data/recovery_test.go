package data

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadRejectsForeignOwnershipUnexpectedObjectsAndCancellation(t *testing.T) {
	for _, failure := range []string{"foreign marker", "unexpected object", "cancel", "corrupt completed payload"} {
		t.Run(failure, func(t *testing.T) {
			project, bundle, destination := fixture(t)
			remote := &memoryBlobs{objects: map[string][]byte{}, failEntry: true}
			if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, false); err == nil {
				t.Fatal("expected interrupted upload")
			}
			remote.failEntry = false
			switch failure {
			case "foreign marker":
				key := destination.Prefix + "/" + claimName
				var owner marker
				if err := json.Unmarshal(remote.objects[key], &owner); err != nil {
					t.Fatal(err)
				}
				owner.Environment = "foreign-environment"
				encoded, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				remote.objects[key] = encoded
			case "unexpected object":
				remote.objects[destination.Prefix+"/foreign-object"] = []byte("unapproved data")
			case "corrupt completed payload":
				if _, err := Upload(t.Context(), project, "demo", bundle, destination, remote, false); err != nil {
					t.Fatal(err)
				}
				remote.objects[destination.Prefix+"/experiment.json"][0] ^= 1
			}
			ctx := t.Context()
			if failure == "cancel" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			result, err := Upload(ctx, project, "demo", bundle, destination, remote, false)
			if err == nil || result.BlobVerified {
				t.Fatal("unsafe recovery claimed ready content")
			}
			if failure != "corrupt completed payload" && remote.objects[destination.Prefix+"/"+completeName] != nil {
				t.Fatal("unsafe recovery published completion")
			}
		})
	}
}

func TestValidateChecksOnlyDataAndNeverExecutesUploadedCode(t *testing.T) {
	project, bundle, _ := fixture(t)
	if bundle.Fingerprint == "" {
		t.Fatal("content fingerprint is missing")
	}
	files, err := os.ReadDir(filepath.Join(project.Root, "inputs"))
	if err != nil || len(files) != 2 {
		t.Fatal("validation wrote files")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if candidate, err := Validate(ctx, project, "example"); err == nil {
		candidate.Root.Close()
		t.Fatal("cancelled validation succeeded")
	}
}
