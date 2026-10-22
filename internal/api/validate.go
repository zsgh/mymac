package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Validate checks a decoded JSON value against a schema.
//
// It understands the part of JSON Schema the mymac API uses: the types, the
// properties, the required list, the items, the references and the closed
// value sets. It is not a general validator and does not have to be: the
// schemas in this package are generated, so a full implementation would only
// ever see what is written here.
func Validate(doc *Document, schema *Schema, value any) error {
	if schema == nil {
		return nil
	}
	return validate(doc, schema, value, "")
}

// validate checks a value and reports the path it failed at.
func validate(doc *Document, schema *Schema, value any, path string) error {
	if schema.Ref != "" {
		target, ok := resolve(doc, schema.Ref)
		if !ok {
			return fmt.Errorf("%s: unknown reference %s", at(path), schema.Ref)
		}
		return validate(doc, target, value, path)
	}
	for _, sub := range schema.AllOf {
		if err := validate(doc, sub, value, path); err != nil {
			return err
		}
	}
	if len(schema.OneOf) > 0 {
		for _, sub := range schema.OneOf {
			if err := validate(doc, sub, value, path); err == nil {
				return nil
			}
		}
		return fmt.Errorf("%s: matches none of the expected shapes", at(path))
	}
	if value == nil {
		if schema.Nullable {
			return nil
		}
		if schema.Type == "" {
			return nil
		}
		return fmt.Errorf("%s: expected %s, got null", at(path), schema.Type)
	}
	switch schema.Type {
	case "":
		return nil
	case "string":
		s, ok := value.(string)
		if !ok {
			return typeError(path, "string", value)
		}
		if len(schema.Enum) > 0 && !contains(schema.Enum, s) {
			return fmt.Errorf("%s: %q is not one of %s", at(path), s, strings.Join(schema.Enum, ", "))
		}
	case "number":
		f, ok := value.(float64)
		if !ok {
			return typeError(path, "number", value)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("%s: %v is not a number", at(path), f)
		}
	case "integer":
		f, ok := value.(float64)
		if !ok {
			return typeError(path, "integer", value)
		}
		if f != math.Trunc(f) {
			return fmt.Errorf("%s: %v is not an integer", at(path), f)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return typeError(path, "boolean", value)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return typeError(path, "array", value)
		}
		for i, item := range items {
			if err := validate(doc, schema.Items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return typeError(path, "object", value)
		}
		for _, name := range schema.Required {
			if _, ok := obj[name]; !ok {
				return fmt.Errorf("%s: missing field %q", at(path), name)
			}
		}
		for name, item := range obj {
			sub, known := schema.Properties[name]
			if !known {
				if closed, ok := schema.AdditionalProperties.(bool); ok && !closed {
					return fmt.Errorf("%s: unknown field %q", at(path), name)
				}
				if sub, ok := schema.AdditionalProperties.(*Schema); ok {
					if err := validate(doc, sub, item, join(path, name)); err != nil {
						return err
					}
				}
				continue
			}
			if err := validate(doc, sub, item, join(path, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolve looks a reference up in the components of the document.
func resolve(doc *Document, ref string) (*Schema, bool) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) || doc == nil || doc.Components == nil {
		return nil, false
	}
	s, ok := doc.Components.Schemas[strings.TrimPrefix(ref, prefix)]
	return s, ok
}

// typeError reports a value of the wrong type.
func typeError(path, want string, got any) error {
	return fmt.Errorf("%s: expected %s, got %s", at(path), want, kindOf(got))
}

// kindOf names the JSON type of a value.
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "value"
}

// at names the place a value sits in the document.
func at(path string) string {
	if path == "" {
		return "body"
	}
	return path
}

// join appends a field name to a path.
func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// contains reports whether a value is in a list.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SchemaFor returns the schema of a response of an endpoint.
//
// It is used by the contract test, which checks the live answer against the
// description.
func SchemaFor(doc *Document, method, path, status string) *Schema {
	item, ok := doc.Paths[path]
	if !ok {
		return nil
	}
	op := item.operation(method)
	if op == nil {
		return nil
	}
	resp, ok := op.Responses[status]
	if !ok {
		return nil
	}
	for _, mt := range resp.Content {
		if mt.Schema != nil {
			return mt.Schema
		}
	}
	return nil
}

// FormatExample renders a value as a string, for the documentation.
func FormatExample(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprintf("%v", v)
}
