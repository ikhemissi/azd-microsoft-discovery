package config

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

var dataPathPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func ValidateDataPath(value string) error {
	if value == "" || len(value) > 512 || path.IsAbs(value) || path.Clean(value) != value {
		return errors.New("data paths must be bounded, relative, canonical paths")
	}
	for part := range strings.SplitSeq(value, "/") {
		if !dataPathPart.MatchString(part) || part == "." || part == ".." || strings.HasPrefix(part, ".azd-") {
			return errors.New("data path contains an unsafe or reserved component")
		}
	}
	return nil
}

func validateData(settings *Settings, deferred bool) error {
	if settings.Data == nil {
		return nil
	}
	for _, source := range settings.Data.Sources {
		for _, field := range []struct{ value, kind string }{
			{source.StorageContainerResourceID, "Microsoft.Discovery/storageContainers"},
			{source.BlobContainerResourceID, "Microsoft.Storage/storageAccounts/blobServices/containers"},
		} {
			if deferred && variable.FindString(field.value) == field.value {
				continue
			}
			if _, err := resource(field.value, field.kind); err != nil {
				return errors.New("data source has an invalid Discovery/Blob container ARM identity")
			}
		}
		if err := ValidateDataPath(source.Prefix); err != nil {
			return err
		}
	}
	destinations := map[string]bool{}
	for name, dataset := range settings.Data.Datasets {
		source, exists := settings.Data.Sources[dataset.Source]
		if !exists {
			return errors.New("dataset refers to an undeclared source")
		}
		if dataset.Manifest == "" {
			dataset.Manifest = "bundle.yaml"
		}
		if err := ValidateDataPath(dataset.Manifest); err != nil {
			return err
		}
		if err := ValidateDataPath(dataset.Namespace); err != nil {
			return err
		}
		if err := ValidateDataPath(dataset.Version); err != nil || strings.Contains(dataset.Version, "/") {
			return errors.New("dataset version must be one safe path component")
		}
		if dataset.StorageAssetResourceID != "" && !(deferred && variable.FindString(dataset.StorageAssetResourceID) == dataset.StorageAssetResourceID) {
			asset, err := resource(dataset.StorageAssetResourceID, "Microsoft.Discovery/storageContainers/storageAssets")
			if err != nil {
				return errors.New("dataset storageAssetResourceId must be a Discovery storage asset ARM ID")
			}
			if !(deferred && variable.FindString(source.StorageContainerResourceID) == source.StorageContainerResourceID) &&
				!strings.EqualFold(asset.Parent.String(), source.StorageContainerResourceID) {
				return errors.New("dataset storage asset must belong to the declared Discovery storage container")
			}
		}
		key := strings.ToLower(source.BlobContainerResourceID) + "/" + source.Prefix + "/" + dataset.Namespace + "/" + dataset.Version
		if destinations[key] {
			return errors.New("multiple datasets declare the same immutable destination")
		}
		destinations[key] = true
		settings.Data.Datasets[name] = dataset
	}
	return nil
}
