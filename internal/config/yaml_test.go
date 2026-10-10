package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsafeYAML(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate":          "name: first\nname: second\n",
		"multiple documents": "name: first\n---\nname: second\n",
		"custom tag":         "value: !execute dangerous\n",
		"alias":              "value: &shared test\ncopy: *shared\n",
		"nonstring key":      "1: value\n",
		"wrong root":         "- value\n",
		"excessive size":     "value: " + strings.Repeat("a", MaxDocumentBytes),
		"excessive depth":    "value: " + strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDocument([]byte(data)); err == nil {
				t.Fatal("unsafe YAML accepted")
			}
		})
	}
}

func TestPathEscapes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "inside.yaml", "name: inside")
	writeFile(t, parent, "outside.yaml", "name: outside")
	if _, err := ResolvePath(root, root, "inside.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePath(root, root, "../outside.yaml"); err == nil {
		t.Fatal("relative escape accepted")
	}
	if err := os.Symlink(filepath.Join(parent, "outside.yaml"), filepath.Join(root, "escape.yaml")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := ResolvePath(root, root, "escape.yaml"); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
