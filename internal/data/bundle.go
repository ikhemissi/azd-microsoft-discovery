package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/state"
	"github.com/ikhemissi/azd-microsoft-discovery/schemas"
)

const MaxBundleBytes int64 = 2 << 30

type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion string  `json:"schemaVersion"`
	Files         []Entry `json:"files"`
}

type Bundle struct {
	Name        string
	Root        *os.Root
	Entries     []Entry
	Bytes       int64
	Fingerprint string
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (b *Bundle) Open(entry Entry) (*os.File, error) {
	parts := strings.Split(entry.Path, "/")
	for i := range parts {
		info, err := b.Root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("bundle entry is missing or contains a symlink")
		}
	}
	file, err := b.Root.Open(entry.Path)
	if err != nil {
		return nil, errors.New("cannot open bundle entry")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return nil, errors.Join(errors.New("bundle entry is not a regular file of the declared size"), file.Close())
	}
	return file, nil
}

func Digest(ctx context.Context, reader io.Reader, size int64) (string, error) {
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(contextReader{ctx, reader}, size+1))
	if err != nil || count != size {
		return "", errors.Join(errors.New("bundle content could not be verified at its declared size"), ctx.Err())
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func Validate(ctx context.Context, project *config.Project, name string) (bundle *Bundle, err error) {
	if project.Discovery.Data == nil {
		return nil, errors.New("declare discovery.data before validating datasets")
	}
	dataset, exists := project.Discovery.Data.Datasets[name]
	if !exists {
		return nil, errors.New("dataset is not declared")
	}
	directory, err := config.ResolvePath(project.Root, project.Root, dataset.Directory)
	if err != nil {
		return nil, errors.New("bundle directory must exist within the project")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("cannot open bundle directory")
	}
	bundle = &Bundle{Name: name, Root: root}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	if err := config.ValidateDataPath(dataset.Manifest); err != nil {
		return nil, err
	}
	info, err := root.Lstat(dataset.Manifest)
	if err != nil || !info.Mode().IsRegular() || info.Size() > config.MaxDocumentBytes {
		return nil, errors.New("bundle manifest must be a bounded regular file")
	}
	manifestEntry := Entry{Path: dataset.Manifest, Size: info.Size()}
	file, err := bundle.Open(manifestEntry)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, config.MaxDocumentBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) != manifestEntry.Size {
		return nil, errors.New("cannot read stable bundle manifest")
	}
	document, err := config.ParseDocument(raw)
	if err != nil {
		return nil, err
	}
	schema, err := schemas.Compile("bundle")
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("invalid bundle manifest")
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	if err := schema.Validate(normalized); err != nil {
		return nil, errors.New("bundle manifest schema/version, file count, checksum or size is invalid")
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return nil, errors.New("invalid typed bundle manifest")
	}
	manifestSum := sha256.Sum256(raw)
	manifestEntry.SHA256 = hex.EncodeToString(manifestSum[:])
	bundle.Entries = append(manifest.Files, manifestEntry)
	seen := map[string]bool{}
	declared := map[string]bool{}
	for i, entry := range bundle.Entries {
		if err := config.ValidateDataPath(entry.Path); err != nil {
			return nil, fmt.Errorf("bundle entry %d has an unsafe destination", i+1)
		}
		key := strings.ToLower(entry.Path)
		if seen[key] {
			return nil, errors.New("bundle has duplicate/case-colliding destinations or lists its own manifest")
		}
		for previous := range seen {
			if strings.HasPrefix(previous, key+"/") || strings.HasPrefix(key, previous+"/") {
				return nil, errors.New("bundle file/directory destinations collide")
			}
		}
		seen[key] = true
		declared[entry.Path] = true
		bundle.Bytes += entry.Size
		if bundle.Bytes > MaxBundleBytes {
			return nil, errors.New("bundle exceeds the 2 GiB total limit")
		}
	}
	for i, entry := range bundle.Entries {
		file, err := bundle.Open(entry)
		if err != nil {
			return nil, fmt.Errorf("bundle entry %d: %w", i+1, err)
		}
		digest, readErr := Digest(ctx, file, entry.Size)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || digest != entry.SHA256 {
			return nil, fmt.Errorf("bundle entry %d failed checksum verification", i+1)
		}
	}
	// An undeclared local file must never be silently excluded from an upload.
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("bundle traversal encountered an inaccessible entry or symlink")
		}
		if !entry.IsDir() && !declared[path.Clean(relative)] {
			return errors.New("bundle contains undeclared files")
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(bundle.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	bundle.Fingerprint, err = state.Hash(bundle.Entries)
	return bundle, err
}
