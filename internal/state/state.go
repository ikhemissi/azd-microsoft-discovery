package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/ikhemissi/azd-microsoft-discovery/internal/config"
)

type Record struct {
	SchemaVersion int               `json:"schemaVersion"`
	Scope         string            `json:"scope"`
	Service       string            `json:"service"`
	Source        string            `json:"source"`
	Desired       string            `json:"desired"`
	Outputs       map[string]string `json:"outputs,omitempty"`
	Operation     string            `json:"operation,omitempty"`
	Phase         string            `json:"phase"`
}

type Store struct{ Directory string }

func Hash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func Open(root, environment string) (*Store, error) {
	if environment == "" || environment == "." || environment == ".." ||
		filepath.Base(environment) != environment {
		return nil, errors.New("an explicit current azd environment is required for deployment state")
	}
	envRoot, err := config.ResolvePath(root, root, filepath.Join(".azure", environment))
	if err != nil {
		return nil, errors.New("deployment environment must exist locally within the project")
	}
	directory := filepath.Join(envRoot, "discovery")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	directory, err = config.ResolvePath(root, root, directory)
	if err != nil {
		return nil, err
	}
	return &Store{Directory: directory}, nil
}

func (s *Store) path(service, extension string) string {
	sum := sha256.Sum256([]byte(service))
	return filepath.Join(s.Directory, hex.EncodeToString(sum[:])+extension)
}

func (s *Store) Lock(ctx context.Context, service string) (func() error, error) {
	path := s.path(service, ".lock")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state lock must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock := flock.New(path)
	acquired, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("cannot lock Discovery service state: %w", err)
	}
	if !acquired {
		return nil, fmt.Errorf("cannot lock Discovery service state: %w", ctx.Err())
	}
	return lock.Unlock, nil
}

func (s *Store) Read(service, scope string) (*Record, error) {
	path := s.path(service, ".json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, errors.New("Discovery recovery record must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record Record
	if len(data) > 65536 || json.Unmarshal(data, &record) != nil || record.SchemaVersion != 1 ||
		record.Service != service || record.Scope != scope {
		return nil, errors.New("Discovery recovery record is invalid or belongs to a different scope")
	}
	return &record, nil
}

func (s *Store) Write(record *Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.Directory, ".record-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path(record.Service, ".json"))
}
