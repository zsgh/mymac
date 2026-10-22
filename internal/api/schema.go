package api

import (
	"reflect"
	"strings"
	"time"
)

// Schema is a JSON Schema fragment, in the dialect OpenAPI 3.0 uses.
//
// The struct is deliberately small: it covers everything the mymac API
// returns, and nothing more. A field that is not part of the dialect would
// have to be dropped by every tool that reads the document.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Title                string             `json:"title,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
	AllOf                []*Schema          `json:"allOf,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Nullable             bool               `json:"nullable,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Default              any                `json:"default,omitempty"`
	Example              any                `json:"example,omitempty"`
}

// builder walks Go types and turns them into schemas.
//
// Named types become components so the document stays readable: a process
// list shows "procs.Process" instead of a wall of inline properties.
type builder struct {
	components map[string]*Schema
	order      []string
	building   map[reflect.Type]bool
}

func newBuilder() *builder {
	return &builder{
		components: map[string]*Schema{},
		building:   map[reflect.Type]bool{},
	}
}

var (
	timeType     = reflect.TypeOf(time.Time{})
	durationType = reflect.TypeOf(time.Duration(0))
)

// schemaOf builds the schema of a Go type.
func (b *builder) schemaOf(t reflect.Type) *Schema {
	if t == nil {
		return &Schema{}
	}
	switch t {
	case timeType:
		return &Schema{Type: "string", Format: "date-time"}
	case durationType:
		return &Schema{Type: "integer", Format: "int64"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		s := b.schemaOf(t.Elem())
		if s.Ref != "" {
			// A $ref ignores the keys next to it, so the nullable flag
			// has to sit next to an allOf wrapper.
			return &Schema{Nullable: true, AllOf: []*Schema{s}}
		}
		s.Nullable = true
		return s
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: b.schemaOf(t.Elem())}
	case reflect.Map:
		s := &Schema{Type: "object"}
		if t.Elem().Kind() != reflect.Interface && t.Elem().Kind() != reflect.Map {
			s.AdditionalProperties = b.schemaOf(t.Elem())
		}
		return s
	case reflect.Interface:
		// Any value is allowed, so the schema stays empty.
		return &Schema{}
	}
	if t.Name() != "" && t.PkgPath() != "" {
		return b.component(t)
	}
	return b.primitive(t)
}

// primitive builds the schema of a built-in type.
func (b *builder) primitive(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	}
	return &Schema{}
}

// component registers a named type and returns a reference to it.
func (b *builder) component(t reflect.Type) *Schema {
	name := ComponentName(t)
	if _, ok := b.components[name]; ok {
		return &Schema{Ref: refPrefix + name}
	}
	if b.building[t] {
		// A type that contains itself would loop forever. The reference
		// is still correct, the schema behind it is simply incomplete.
		return &Schema{Ref: refPrefix + name}
	}
	b.building[t] = true
	defer delete(b.building, t)

	s := &Schema{}
	switch t.Kind() {
	case reflect.Struct:
		s.Type = "object"
		b.fillStruct(s, t)
	case reflect.String:
		s.Type = "string"
		if values, ok := enums[name]; ok {
			s.Enum = values
		}
	default:
		s = b.primitive(t)
	}
	b.components[name] = s
	b.order = append(b.order, name)
	return &Schema{Ref: refPrefix + name}
}

// fillStruct fills the properties of an object schema from a Go struct.
func (b *builder) fillStruct(s *Schema, t reflect.Type) {
	props := map[string]*Schema{}
	var required []string
	b.collectFields(t, props, &required)
	if len(props) > 0 {
		s.Properties = props
	}
	s.Required = required
}

// collectFields walks a struct and collects the JSON fields.
//
// An embedded struct without a JSON name is flattened: encoding/json
// promotes its fields into the outer object, so the schema has to do the
// same.
func (b *builder) collectFields(t reflect.Type, props map[string]*Schema, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			// Unexported field.
			continue
		}
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && opts == "" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != timeType {
				b.collectFields(ft, props, required)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		props[name] = b.schemaOf(f.Type)
		if !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer {
			*required = append(*required, name)
		}
	}
}

// ComponentName returns the component name of a named type.
//
// The package is part of the name so two types with the same name in
// different packages do not collide.
func ComponentName(t reflect.Type) string {
	if t.Name() == "" {
		return ""
	}
	pkg := t.PkgPath()
	if pkg == "" {
		return t.Name()
	}
	parts := strings.Split(pkg, "/")
	return parts[len(parts)-1] + "." + t.Name()
}

// refPrefix is where the component schemas live inside the document.
const refPrefix = "#/components/schemas/"

// enums lists the closed value sets of the API.
//
// They are written down here instead of being derived from the Go constants:
// a constant list would have to be rebuilt for every package, and the values
// are part of the wire contract anyway.
var enums = map[string][]string{
	"errs.Kind": {
		"unauthorized", "permission_denied", "sip_protected", "process_gone",
		"file_busy", "not_found", "invalid_request", "unsupported", "internal",
	},
	"perm.Target": {"full_disk_access", "automation", "admin"},
	"perm.State":  {"granted", "denied", "unknown", "not_applicable"},
	"envs.Scope":  {"user", "path"},
	"envs.MarkerState": {
		"ok", "missing", "damaged", "conflict",
	},
	"loginitems.Kind": {
		"launchAgent", "launchDaemon", "loginItem", "service",
	},
	"procs.State": {"running", "sleeping", "stopped", "zombie", ""},
}
