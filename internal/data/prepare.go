package data

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

func Prepare(ctx context.Context, project *config.Project, name string) (bundle *Bundle, err error) {
	if project.Discovery.Data == nil {
		return nil, errors.New("declare discovery.data before preparing a dataset")
	}
	dataset, exists := project.Discovery.Data.Datasets[name]
	if !exists {
		return nil, errors.New("dataset is not declared")
	}
	if err := config.ValidateDataPath(dataset.Manifest); err != nil {
		return nil, err
	}
	directory, err := config.ResolvePath(project.Root, project.Root, dataset.Directory)
	if err != nil {
		return nil, errors.New("bundle directory must exist within the project")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("cannot open bundle directory")
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if _, err := root.Lstat(dataset.Manifest); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("manifest already exists or cannot be inspected; prepare a new bundle or validate the existing one")
	}
	manifest := Manifest{SchemaVersion: "1.0"}
	paths := map[string]bool{}
	total := int64(0)
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("bundle contains an inaccessible entry or symlink")
		}
		if relative == "." {
			return nil
		}
		if err := config.ValidateDataPath(relative); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<30 {
			return errors.New("bundle payload must be a regular file no larger than 1 GiB")
		}
		key := strings.ToLower(relative)
		if paths[key] || len(manifest.Files) >= 1000 {
			return errors.New("bundle has case-colliding paths or more than 1000 files")
		}
		paths[key] = true
		total += info.Size()
		if total > MaxBundleBytes {
			return errors.New("bundle exceeds the 2 GiB total limit")
		}
		manifest.Files = append(manifest.Files, Entry{Path: relative, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(manifest.Files) == 0 {
		return nil, errors.New("bundle requires at least one payload file")
	}
	files := &Bundle{Root: root}
	for i := range manifest.Files {
		file, err := files.Open(manifest.Files[i])
		if err != nil {
			return nil, err
		}
		digest, readErr := Digest(ctx, file, manifest.Files[i].Size)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		manifest.Files[i].SHA256 = digest
	}
	slices.SortFunc(manifest.Files, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > config.MaxDocumentBytes || total+int64(len(encoded)) > MaxBundleBytes {
		return nil, errors.New("generated manifest or total bundle exceeds its size limit")
	}
	file, err := root.OpenFile(dataset.Manifest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("cannot create manifest without overwriting an existing file")
	}
	_, writeErr := file.Write(encoded)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return nil, errors.Join(errors.New("cannot persist generated manifest"), root.Remove(dataset.Manifest))
	}
	bundle, err = Validate(ctx, project, name)
	if err != nil {
		return nil, errors.Join(err, root.Remove(dataset.Manifest))
	}
	return bundle, nil
}
