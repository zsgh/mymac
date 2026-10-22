package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// marshalIndent renders a value as indented JSON.
func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// YAML renders the document as YAML.
//
// The document is built as Go values, so it is turned into a plain tree
// first and written by a small emitter. The emitter only has to handle the
// handful of shapes the document uses, and it writes them in a shape every
// YAML parser reads back the same way.
func YAML() ([]byte, error) {
	raw, err := json.Marshal(Spec())
	if err != nil {
		return nil, err
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writeYAML(&buf, tree, 0, false)
	return buf.Bytes(), nil
}

// writeYAML writes a value as YAML.
//
// inList marks a value that sits directly under a list marker, where the
// indentation of the first key has to line up with the marker itself.
func writeYAML(buf *bytes.Buffer, v any, indent int, inList bool) {
	pad := strings.Repeat("  ", indent)
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			buf.WriteString(pad + "{}\n")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 || !inList {
				buf.WriteString(pad)
			}
			buf.WriteString(yamlKey(k))
			buf.WriteString(":")
			writeYAMLValue(buf, t[k], indent)
		}
	case []any:
		if len(t) == 0 {
			buf.WriteString(pad + "[]\n")
			return
		}
		for i, item := range t {
			if i > 0 || !inList {
				buf.WriteString(pad)
			}
			switch item.(type) {
			case map[string]any, []any:
				buf.WriteString("- ")
				writeYAML(buf, item, indent+1, true)
			default:
				buf.WriteString("- " + yamlScalar(item) + "\n")
			}
		}
	default:
		buf.WriteString(pad + yamlScalar(v) + "\n")
	}
}

// writeYAMLValue writes the value of a mapping key.
func writeYAMLValue(buf *bytes.Buffer, v any, indent int) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			buf.WriteString(" {}\n")
			return
		}
		buf.WriteString("\n")
		writeYAML(buf, v, indent+1, false)
	case []any:
		if len(t) == 0 {
			buf.WriteString(" []\n")
			return
		}
		buf.WriteString("\n")
		writeYAML(buf, v, indent+1, false)
	default:
		buf.WriteString(" " + yamlScalar(v) + "\n")
	}
}

// yamlKey writes a mapping key.
func yamlKey(k string) string {
	return yamlQuote(k)
}

// yamlScalar writes a scalar value.
func yamlScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return yamlQuote(t)
	}
	return fmt.Sprintf("%v", v)
}

// yamlQuote writes a string as a double quoted scalar.
//
// Double quotes are the one style every YAML parser understands, so the
// strings never need a case-by-case decision about plain style.
func yamlQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
