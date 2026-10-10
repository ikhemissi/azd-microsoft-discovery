package schemas

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed discovery.schema.json
var discovery []byte

func Entry(name string) (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal(discovery, &document); err != nil {
		return nil, fmt.Errorf("decode embedded configuration schema: %w", err)
	}
	definitions, ok := document["$defs"].(map[string]any)
	if !ok || definitions[name] == nil {
		return nil, fmt.Errorf("unknown configuration schema entry %q", name)
	}
	document["$ref"] = "#/$defs/" + name
	return document, nil
}

func Compile(name string) (*jsonschema.Schema, error) {
	document, err := Entry(name)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://schemas.azd-discovery.invalid/configuration.json"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, fmt.Errorf("load embedded configuration schema: %w", err)
	}
	return compiler.Compile(location)
}
