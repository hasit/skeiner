// Package yamljson reads a single YAML or JSON document using JSON field names.
package yamljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// Decode uses the YAML decoder's duplicate-key and alias-expansion checks.
// YAML permits numeric map keys (commonly response codes), JSON requires strings.
func Decode(data []byte, out any) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	var tree any
	if err := d.Decode(&tree); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("expected exactly one document")
		}
		return err
	}
	tree, err := jsonKeys(tree)
	if err != nil {
		return err
	}
	b, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func jsonKeys(v any) (any, error) {
	switch v := v.(type) {
	case map[any]any:
		o := make(map[string]any, len(v))
		for key, child := range v {
			var name string
			switch k := key.(type) {
			case string:
				name = k
			case int, uint64:
				name = fmt.Sprint(k)
			default:
				return nil, fmt.Errorf("unsupported YAML map key %T", key)
			}
			if _, exists := o[name]; exists {
				return nil, fmt.Errorf("duplicate key %q", name)
			}
			o[name] = child
		}
		return jsonKeys(o)
	case map[string]any:
		for k, child := range v {
			val, err := jsonKeys(child)
			if err != nil {
				return nil, err
			}
			v[k] = val
		}
	case []any:
		for i, child := range v {
			val, err := jsonKeys(child)
			if err != nil {
				return nil, err
			}
			v[i] = val
		}
	}
	return v, nil
}
