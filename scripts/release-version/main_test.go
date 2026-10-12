package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetVersion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/cmd"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"extension.yaml":          "id: fixture\nversion: 0.1.0\n",
		"internal/cmd/version.go": "package cmd\nvar Version = \"0.1.0\"\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := setVersion(root, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "v0.2.0", "0.2", "0.2.0-preview.1", "0.2.0+build", "0.2.0;command"} {
		if err := setVersion(root, bad); err == nil {
			t.Fatalf("invalid release version accepted: %q", bad)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "internal/cmd/version.go"), []byte("package cmd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := setVersion(root, "0.3.0"); err == nil {
		t.Fatal("missing version field accepted")
	}
	data, err := os.ReadFile(filepath.Join(root, "extension.yaml"))
	if err != nil || string(data) != "id: fixture\nversion: 0.2.0\n" {
		t.Fatalf("validation failure partially changed manifest: %s %v", data, err)
	}
}
