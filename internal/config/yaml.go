package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

const MaxDocumentBytes = 1 << 20
const maxDepth = 64

func ReadDocument(path string) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open definition/configuration file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read definition/configuration file: %w", err)
	}
	return ParseDocument(data)
}

func ParseDocument(data []byte) (map[string]any, error) {
	if len(data) > MaxDocumentBytes {
		return nil, errors.New("YAML document exceeds 1 MiB limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, errors.New("invalid or empty YAML document")
	}
	if err := validateNode(&node, 0); err != nil {
		return nil, err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("YAML document must contain an object")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("exactly one YAML document is required")
	}
	var result map[string]any
	if err := node.Decode(&result); err != nil {
		return nil, errors.New("YAML document contains unsupported values")
	}
	return result, nil
}

func validateNode(node *yaml.Node, depth int) error {
	if depth > maxDepth {
		return errors.New("YAML document exceeds maximum nesting depth")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("YAML anchors and aliases are unsupported (line %d)", node.Line)
	}
	switch node.Tag {
	case "", "!!map", "!!seq", "!!str", "!!int", "!!float", "!!bool", "!!null":
	default:
		return fmt.Errorf("unsupported YAML tag (line %d)", node.Line)
	}
	if node.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("YAML mapping keys must be strings (line %d)", key.Line)
			}
			if keys[key.Value] {
				return fmt.Errorf("duplicate YAML mapping key (line %d)", key.Line)
			}
			keys[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func ResolvePath(root, base, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("definition/project path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve definition/project path: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("definition/project path escapes the azd project root")
	}
	return resolved, nil
}
