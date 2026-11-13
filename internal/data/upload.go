package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
)

var ErrMissing = errors.New("blob does not exist")
var ErrExists = errors.New("blob already exists")

const claimName = ".azd-upload.json"
const completeName = ".azd-complete.json"

type BlobStore interface {
	List(context.Context, string) ([]string, error)
	Read(context.Context, string) (io.ReadCloser, error)
	Create(context.Context, string, io.Reader) error
}

type Destination struct {
	ContainerID    string
	Prefix         string
	URI            string
	StorageAssetID string
	ResourceURI    string
}

type Result struct {
	Status                 string `json:"status"`
	Dataset                string `json:"dataset"`
	Files                  int    `json:"files"`
	Bytes                  int64  `json:"bytes"`
	ContentSHA256          string `json:"contentSha256"`
	BlobPrefixURI          string `json:"blobPrefixUri,omitempty"`
	BlobVerified           bool   `json:"blobVerified"`
	ConversationAttached   bool   `json:"conversationAttached"`
	DiscoveryAttachment    string `json:"discoveryAttachment"`
	StorageAssetResourceID string `json:"storageAssetResourceId,omitempty"`
	ResourceURI            string `json:"resourceUri,omitempty"`
	AssetVerified          bool   `json:"assetVerified"`
}

type marker struct {
	SchemaVersion int    `json:"schemaVersion"`
	Environment   string `json:"environment"`
	Dataset       string `json:"dataset"`
	Destination   string `json:"destination"`
	Fingerprint   string `json:"fingerprint"`
	UploadID      string `json:"uploadId"`
}

func Report(bundle *Bundle, destination Destination, status string) Result {
	return Result{Status: status, Dataset: bundle.Name, Files: len(bundle.Entries), Bytes: bundle.Bytes,
		ContentSHA256: bundle.Fingerprint, BlobPrefixURI: destination.URI, BlobVerified: status == "uploaded",
		DiscoveryAttachment: "required", StorageAssetResourceID: destination.StorageAssetID,
		ResourceURI: destination.ResourceURI, AssetVerified: destination.StorageAssetID != ""}
}

func readMarker(ctx context.Context, remote BlobStore, name string) ([]byte, error) {
	reader, err := remote.Read(ctx, name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, 8193))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(data) > 8192 {
		return nil, errors.New("upload marker is unreadable or exceeds its limit")
	}
	return data, nil
}

func verify(ctx context.Context, remote BlobStore, name string, entry Entry) error {
	reader, err := remote.Read(ctx, name)
	if err != nil {
		return err
	}
	digest, readErr := Digest(ctx, reader, entry.Size)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || digest != entry.SHA256 {
		return errors.New("remote blob failed content-integrity verification; no completion marker was published")
	}
	return nil
}

func putMarker(ctx context.Context, remote BlobStore, key string, expected []byte) error {
	if err := remote.Create(ctx, key, bytes.NewReader(expected)); err != nil {
		// A lost acknowledgement or conditional conflict is reconciled by exact readback.
		actual, readErr := readMarker(ctx, remote, key)
		if readErr != nil || !bytes.Equal(actual, expected) {
			return errors.Join(err, errors.New("marker creation was not verified"))
		}
	}
	actual, err := readMarker(ctx, remote, key)
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("upload marker readback does not match this upload")
	}
	return nil
}

func Upload(ctx context.Context, project *config.Project, environment string, bundle *Bundle,
	destination Destination, remote BlobStore, dryRun bool) (result Result, err error) {
	for _, entry := range bundle.Entries {
		name := destination.Prefix + "/" + entry.Path
		if destination.Prefix == "" || len(name) > 1024 || len(strings.Split(name, "/")) > 254 {
			return Result{}, errors.New("combined Blob destination exceeds supported path limits")
		}
	}
	if dryRun {
		existing, err := remote.List(ctx, destination.Prefix+"/")
		if err != nil {
			return Result{}, err
		}
		if len(existing) != 0 {
			raw, err := readMarker(ctx, remote, path.Join(destination.Prefix, claimName))
			var claim marker
			if err != nil || json.Unmarshal(raw, &claim) != nil || claim.SchemaVersion != 1 ||
				claim.Fingerprint != bundle.Fingerprint || claim.Dataset != bundle.Name || claim.Environment != environment ||
				claim.Destination != destination.ContainerID+"#"+destination.Prefix {
				return Result{}, errors.New("dry-run found a conflicting immutable destination")
			}
		}
		return Report(bundle, destination, "dryRun"), nil
	}
	store, err := state.Open(project.Root, environment)
	if err != nil {
		return Result{}, err
	}
	scope := destination.ContainerID + "#" + destination.Prefix
	key, err := state.Hash(scope)
	if err != nil {
		return Result{}, err
	}
	service := "data:" + bundle.Name + ":" + key
	unlock, err := store.Lock(ctx, service)
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	record, err := store.Read(service, scope)
	if err != nil {
		return Result{}, err
	}
	if record != nil && (record.Source != bundle.Fingerprint || record.Outputs["uploadId"] == "") {
		return Result{}, errors.New("immutable upload destination belongs to different content; choose a new version")
	}
	existing, err := remote.List(ctx, destination.Prefix+"/")
	if err != nil {
		return Result{}, err
	}
	if record == nil {
		if len(existing) != 0 {
			return Result{}, errors.New("upload destination already contains data and has no matching local ownership record")
		}
		record = &state.Record{SchemaVersion: 1, Scope: scope, Service: service, Source: bundle.Fingerprint,
			Desired: bundle.Fingerprint, Phase: "uploading", Outputs: map[string]string{"uploadId": uuid.NewString()}}
		if err := store.Write(record); err != nil {
			return Result{}, err
		}
	}
	if record.Phase != "uploading" && record.Phase != "complete" {
		return Result{}, errors.New("invalid upload recovery phase")
	}
	metadata, err := json.Marshal(marker{1, environment, bundle.Name, scope, bundle.Fingerprint, record.Outputs["uploadId"]})
	if err != nil {
		return Result{}, err
	}
	claim := path.Join(destination.Prefix, claimName)
	if err := putMarker(ctx, remote, claim, metadata); err != nil {
		return Result{}, err
	}
	expected := []string{claim, path.Join(destination.Prefix, completeName)}
	for i, entry := range bundle.Entries {
		key := path.Join(destination.Prefix, entry.Path)
		expected = append(expected, key)
		err := verify(ctx, remote, key, entry)
		if errors.Is(err, ErrMissing) {
			file, err := snapshot(ctx, bundle, entry, store.Directory)
			if err != nil {
				return Result{}, err
			}
			createErr := remote.Create(ctx, key, contextReader{ctx, file})
			closeErr := file.Close()
			removeErr := os.Remove(file.Name())
			// Readback, not the transport acknowledgement, establishes completion.
			verifyErr := verify(ctx, remote, key, entry)
			if closeErr != nil || removeErr != nil || verifyErr != nil {
				return Result{}, fmt.Errorf("upload entry %d incomplete: %w", i+1, errors.Join(createErr, closeErr, removeErr, verifyErr))
			}
		} else if err != nil {
			return Result{}, fmt.Errorf("upload entry %d: %w", i+1, err)
		}
	}
	existing, err = remote.List(ctx, destination.Prefix+"/")
	if err != nil {
		return Result{}, err
	}
	for _, name := range existing {
		if !slices.Contains(expected, name) {
			return Result{}, errors.New("upload prefix contains undeclared objects; refusing to mark the bundle complete")
		}
	}
	if err := putMarker(ctx, remote, path.Join(destination.Prefix, completeName), metadata); err != nil {
		return Result{}, err
	}
	record.Phase = "complete"
	if err := store.Write(record); err != nil {
		return Result{}, err
	}
	return Report(bundle, destination, "uploaded"), nil
}

func snapshot(ctx context.Context, bundle *Bundle, entry Entry, directory string) (snapshot *os.File, err error) {
	source, err := bundle.Open(entry)
	if err != nil {
		return nil, err
	}
	var file *os.File
	defer func() {
		err = errors.Join(err, source.Close())
		if err != nil && file != nil {
			err = errors.Join(err, file.Close(), os.Remove(file.Name()))
		}
	}()
	file, err = os.CreateTemp(directory, ".upload-payload-")
	if err != nil {
		return nil, errors.New("cannot create private upload snapshot")
	}
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, source}, entry.Size+1))
	if err != nil || count != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return nil, errors.New("local content changed since validation; no changed payload was uploaded")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("cannot rewind verified upload snapshot")
	}
	return file, nil
}
