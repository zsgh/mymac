package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndpointTableIsConsistent checks the table itself.
//
// The table is the source of truth for both the router and the document, so
// a broken entry would show up in both places at once.
func TestEndpointTableIsConsistent(t *testing.T) {
	known := map[string]bool{}
	for _, tag := range tags {
		known[tag.Name] = true
	}
	keys := map[string]bool{}
	ids := map[string]bool{}
	for _, ep := range Endpoints() {
		key := ep.Method + " " + ep.Path
		if keys[key] {
			t.Errorf("%s is listed twice", key)
		}
		keys[key] = true

		if !known[ep.Tag] {
			t.Errorf("%s has the unknown tag %q", key, ep.Tag)
		}
		if ep.Summary == "" {
			t.Errorf("%s has no summary", key)
		}
		if ep.Description == "" {
			t.Errorf("%s has no description", key)
		}
		if ep.OperationID == "" {
			t.Errorf("%s has no operation id", key)
		} else if ids[ep.OperationID] {
			t.Errorf("%s reuses the operation id %q", key, ep.OperationID)
		} else {
			ids[ep.OperationID] = true
		}
		if len(ep.Responses) == 0 {
			t.Errorf("%s documents no response", key)
		}
		for _, p := range ep.Params {
			if p.Name == "" || p.In != "path" && p.In != "query" {
				t.Errorf("%s has the broken parameter %q", key, p.Name)
			}
			if p.In == "path" && !p.Required {
				t.Errorf("%s has the path parameter %q without the required flag", key, p.Name)
			}
		}
	}
}

// TestDocumentCoversEveryEndpoint makes sure the document holds what the
// table holds.
func TestDocumentCoversEveryEndpoint(t *testing.T) {
	doc := Spec()
	for _, ep := range Endpoints() {
		item, ok := doc.Paths[ep.Path]
		if !ok {
			t.Errorf("%s is missing from the document", ep.Path)
			continue
		}
		op := item.operation(ep.Method)
		if op == nil {
			t.Errorf("%s %s is missing from the document", ep.Method, ep.Path)
			continue
		}
		if op.Summary != ep.Summary {
			t.Errorf("%s %s has the summary %q, want %q", ep.Method, ep.Path, op.Summary, ep.Summary)
		}
		if len(op.Responses) == 0 {
			t.Errorf("%s %s has no response", ep.Method, ep.Path)
		}
	}
	if doc.OpenAPI != "3.0.3" {
		t.Errorf("the document declares the version %q", doc.OpenAPI)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Error("the document has no component schemas")
	}
}

// TestCheckedInDocumentIsCurrent makes sure the committed document matches
// the table.
//
// The files in docs/ are what other tools read, so they have to be
// regenerated whenever the API changes.
func TestCheckedInDocumentIsCurrent(t *testing.T) {
	jsonDoc, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	yamlDoc, err := YAML()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"openapi.json": jsonDoc,
		"openapi.yaml": yamlDoc,
	}
	for name, generated := range files {
		path := filepath.Join("..", "..", "docs", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, generated) {
			t.Errorf("docs/%s is out of date, run make openapi", name)
		}
	}
}

// TestJSONIsValid makes sure the document can be read back.
func TestJSONIsValid(t *testing.T) {
	raw, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("the document declares %v", doc["openapi"])
	}
}

// TestYAMLHoldsTheDocument makes sure the YAML rendering carries the same
// paths and operations as the JSON one.
//
// The two are written by different code, so this is the cheapest way to
// catch a renderer that drops something.
func TestYAMLHoldsTheDocument(t *testing.T) {
	raw, err := YAML()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, ep := range Endpoints() {
		if !strings.Contains(text, ep.Path) {
			t.Errorf("the yaml document is missing the path %s", ep.Path)
		}
		if !strings.Contains(text, ep.OperationID) {
			t.Errorf("the yaml document is missing the operation %s", ep.OperationID)
		}
	}
}

// TestSchemaOfARequest checks the schema of a known type.
func TestSchemaOfARequest(t *testing.T) {
	doc := Spec()
	schema := doc.Components.Schemas["api.EnvVarCreateRequest"]
	if schema == nil {
		t.Fatal("the request type has no schema")
	}
	if schema.Type != "object" {
		t.Errorf("the schema is of the type %q", schema.Type)
	}
	for _, name := range []string{"name", "value", "note"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("the schema has no field %q", name)
		}
	}
	if len(schema.Required) != 3 {
		t.Errorf("the schema requires %v", schema.Required)
	}
}

// TestSchemaOfAnEmbeddedStruct checks that an embedded struct is flattened.
//
// encoding/json promotes the fields of an embedded struct into the outer
// object, and the schema has to do the same.
func TestSchemaOfAnEmbeddedStruct(t *testing.T) {
	doc := Spec()
	schema := doc.Components.Schemas["envs.ManagedVar"]
	if schema == nil {
		t.Fatal("the type has no schema")
	}
	for _, name := range []string{"name", "value", "shellApplied", "launchdValue"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("the schema has no field %q", name)
		}
	}
}

// TestValidateRejectsAWrongAnswer checks the validator itself.
func TestValidateRejectsAWrongAnswer(t *testing.T) {
	doc := Spec()
	schema := SchemaFor(doc, "GET", "/api/health", "200")
	if schema == nil {
		t.Fatal("the endpoint has no schema")
	}
	good := map[string]any{
		"version": "0.1.0",
		"uptime":  "1s",
		"pid":     float64(1),
		"port":    float64(8888),
	}
	if err := Validate(doc, schema, good); err != nil {
		t.Errorf("a good answer was rejected: %v", err)
	}
	bad := map[string]any{
		"version": "0.1.0",
		"uptime":  float64(3),
		"pid":     float64(1),
		"port":    float64(8888),
	}
	if err := Validate(doc, schema, bad); err == nil {
		t.Error("a wrong answer was accepted")
	}
	missing := map[string]any{"version": "0.1.0"}
	if err := Validate(doc, schema, missing); err == nil {
		t.Error("an answer with a missing field was accepted")
	}
}
