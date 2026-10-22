package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"mymac/internal/api"
	"mymac/internal/settings"
)

// newTestServerFor builds a test server around an existing server.
func newTestServerFor(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	// The host check compares against the port the server runs on, so the
	// test server's port has to be known to the Server.
	if u, err := url.Parse(ts.URL); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			srv.port = p
		}
	}
	return ts
}

// doJSON sends a request with a JSON body.
func doJSON(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// mustJSON renders a value as JSON.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// newSettingsServer builds a server whose settings live in a temp directory.
//
// The real settings file must not be touched by a test.
func newSettingsServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	srv, _ := newTestServer(t)
	srv.settings = settings.NewStore()
	return srv
}

// TestTheModelTakesEffectWithoutARestart checks that a change of the
// settings is picked up right away.
//
// The user configures the model in the interface and expects it to work
// without restarting mymac.
func TestTheModelTakesEffectWithoutARestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := settings.NewStore()
	provider := aiProviderFor(store)

	if provider() != nil {
		t.Error("without settings there must be no client")
	}
	if _, err := store.Update(func(v *settings.Settings) {
		v.LLM = settings.LLM{BaseURL: "https://api.example.com/v1", APIKey: "sk-1", Model: "m1"}
	}); err != nil {
		t.Fatal(err)
	}
	client := provider()
	if client == nil || !client.Configured() {
		t.Fatal("the client must follow the settings")
	}
	// And a change of the model is picked up too.
	if _, err := store.Update(func(v *settings.Settings) {
		v.LLM.Model = "m2"
	}); err != nil {
		t.Fatal(err)
	}
	if provider() == nil {
		t.Error("the client must still be there")
	}
}

// TestSettingsRoundTrip checks that the settings survive a round trip.
func TestSettingsRoundTrip(t *testing.T) {
	srv := newSettingsServer(t)
	ts := newTestServerFor(t, srv)

	body := `{"llm":{"baseUrl":"https://api.example.com/v1","model":"m1","apiKey":"sk-1"}}`
	res := doJSON(t, ts, http.MethodPut, "/api/settings", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the settings answered %d", res.StatusCode)
	}
	var got api.SettingsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got.LLM.BaseURL != "https://api.example.com/v1" || got.LLM.Model != "m1" {
		t.Errorf("settings = %+v", got.LLM)
	}
	// The key itself never comes back.
	if got.LLM.HasKey != true {
		t.Error("the server must say that there is a key")
	}
	if strings.Contains(mustJSON(t, got), "sk-1") {
		t.Error("the key must not be sent back")
	}

	// And the model is configured now.
	res = doJSON(t, ts, http.MethodGet, "/api/settings", "")
	defer res.Body.Close()
	var again api.SettingsResponse
	if err := json.NewDecoder(res.Body).Decode(&again); err != nil {
		t.Fatal(err)
	}
	if again.LLM.Model != "m1" {
		t.Errorf("model = %q", again.LLM.Model)
	}
}

// TestDocsToggle checks that the documentation can be closed.
func TestDocsToggle(t *testing.T) {
	srv := newSettingsServer(t)
	ts := newTestServerFor(t, srv)

	// It is reachable to begin with.
	res, err := http.Get(ts.URL + "/api/docs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the page answered %d", res.StatusCode)
	}

	res = doJSON(t, ts, http.MethodPost, "/api/settings/docs", `{"enabled":false}`)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the toggle answered %d", res.StatusCode)
	}

	res, err = http.Get(ts.URL + "/api/docs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("the page answered %d, want 404", res.StatusCode)
	}
	// The description is gone as well.
	res, err = http.Get(ts.URL + "/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("the description answered %d, want 404", res.StatusCode)
	}
	// The API itself is untouched.
	res, err = http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the API answered %d", res.StatusCode)
	}
}

// TestAnalyzeWithoutAModel checks that the AI features say what is missing
// instead of pretending to work.
func TestAnalyzeWithoutAModel(t *testing.T) {
	srv := newSettingsServer(t)
	ts := newTestServerFor(t, srv)

	for _, path := range []string{"/api/loginitems/analyze", "/api/procs/analyze"} {
		res := doJSON(t, ts, http.MethodPost, path, "")
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", path, res.StatusCode)
		}
		if !strings.Contains(string(body), "大模型") {
			t.Errorf("%s said %s", path, strings.TrimSpace(string(body)))
		}
	}
}
