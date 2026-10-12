package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Masterminds/semver/v3"
)

func setVersion(root, version string) error {
	parsed, err := semver.StrictNewVersion(version)
	if err != nil || parsed.Prerelease() != "" || parsed.Metadata() != "" {
		return fmt.Errorf("release version must be normal canonical SemVer: %q", version)
	}
	changes := []struct {
		name, pattern, replacement string
		content                    []byte
	}{
		{name: "extension.yaml", pattern: `(?m)^version: [^\n]+$`, replacement: "version: " + version},
		{name: "internal/cmd/version.go", pattern: `\bVersion\s*=\s*"[^"]+"`, replacement: `Version   = "` + version + `"`},
	}
	for i := range changes {
		change := &changes[i]
		data, err := os.ReadFile(filepath.Join(root, change.name))
		if err != nil {
			return err
		}
		pattern := regexp.MustCompile(change.pattern)
		if len(pattern.FindAllIndex(data, -1)) != 1 {
			return fmt.Errorf("expected exactly one version field in %s", change.name)
		}
		change.content = pattern.ReplaceAll(data, []byte(change.replacement))
	}
	for _, change := range changes {
		if err := os.WriteFile(filepath.Join(root, change.name), change.content, 0600); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/release-version <version>")
		os.Exit(1)
	}
	if err := setVersion(".", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
