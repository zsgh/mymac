package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"mymac/internal/api"
)

// TestRoutesMatchTheSpec makes sure the router and the description agree.
//
// The routes are registered from the endpoint table, so a route that is not
// in the table cannot exist. The other direction is checked here: a handler
// that nobody documented would answer requests the description does not
// mention.
func TestRoutesMatchTheSpec(t *testing.T) {
	srv, _ := newTestServer(t)
	handlers := srv.apiHandlers()
	documented := map[string]bool{}
	for _, key := range api.Keys() {
		documented[key] = true
	}
	for key := range handlers {
		if !documented[key] {
			t.Errorf("the route %s is not in the endpoint table", key)
		}
	}
	for _, key := range api.Keys() {
		if _, ok := handlers[key]; !ok {
			t.Errorf("the route %s has no handler", key)
		}
	}
}

// TestDocumentedAnswersMatchTheSpec calls the endpoints that are safe to
// call and checks the answer against the description.
//
// This is what keeps the document honest: the schemas are generated from
// the types, and this test makes sure the handlers really answer with them.
func TestDocumentedAnswersMatchTheSpec(t *testing.T) {
	_, ts := newTestServer(t)
	doc := api.Spec()

	for _, ep := range api.Endpoints() {
		if !ep.Probe {
			continue
		}
		res, err := http.Get(ts.URL + ep.Path)
		if err != nil {
			t.Errorf("%s: %v", ep.Path, err)
			continue
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s answered %d: %s", ep.Path, res.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		schema := api.SchemaFor(doc, ep.Method, ep.Path, "200")
		if schema == nil {
			continue
		}
		if !strings.Contains(res.Header.Get("Content-Type"), "json") {
			continue
		}
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Errorf("%s answered something that is not json: %v", ep.Path, err)
			continue
		}
		if err := api.Validate(doc, schema, value); err != nil {
			t.Errorf("%s does not match the description: %v", ep.Path, err)
		}
	}
}

// TestOpenAPIIsServed makes sure the description is reachable.
func TestOpenAPIIsServed(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the description answered %d", res.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("the description declares %v", doc["openapi"])
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Error("the description has no paths")
	}
}

// TestDocsPageIsServed makes sure the documentation page is reachable.
func TestDocsPageIsServed(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the page answered %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "<html") {
		t.Error("the answer is not a page")
	}
}
