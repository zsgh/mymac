package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mymac/internal/audit"
	"mymac/internal/config"
	"mymac/internal/envs"
	"mymac/internal/procs"
	"mymac/internal/sysinfo"
)

// newTestServer builds a server with a temporary environment.
//
// Nothing in the test touches the real ~/.config/mymac, the real launchd or
// the real process table.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	lc := envs.NewFakeLaunchctl()
	mgr := envs.NewManager(envs.Options{
		StorePath:       filepath.Join(home, ".config", "mymac", "env.json"),
		EnvSh:           filepath.Join(home, ".config", "mymac", "env.sh"),
		Launchctl:       lc,
		LaunchctlBinary: filepath.Join(home, "no-launchctl"),
	})
	srv, err := New(Options{
		Audit:   audit.New(filepath.Join(home, ".config", "mymac", "audit.log")),
		Env:     mgr,
		Procs:   procs.NewCollector(time.Second),
		Sysinfo: sysinfo.NewCollector(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	// The host check compares against the port the server runs on, so the
	// test server's port has to be known to the Server.
	if u, err := url.Parse(ts.URL); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			srv.port = p
		}
	}
	return srv, ts
}

// request performs a request against the test server.
func request(t *testing.T, ts *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestAPINeedsNoToken makes sure the local API is reachable without a token.
//
// The server only listens on the loopback interface and every request still
// has to pass the Host, Origin and Fetch Metadata checks.
func TestAPINeedsNoToken(t *testing.T) {
	_, ts := newTestServer(t)
	for _, path := range []string{"/api/health", "/api/env/vars", "/api/procs", "/api/apps"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s without a token = %d, want 200", path, res.StatusCode)
		}
	}
}

// TestRejectsForeignHost blocks DNS rebinding.
func TestRejectsForeignHost(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/health", nil)
	req.Host = "evil.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("a request with a foreign Host must be rejected")
	}
}

// TestRejectsForeignOrigin blocks CSRF from another site.
func TestRejectsForeignOrigin(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/health", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("a request with a foreign Origin must be rejected")
	}
}

// TestRejectsCrossSiteFetch blocks a page on another origin even when it
// manages to leave the Origin header out.
func TestRejectsCrossSiteFetch(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/health", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("a cross site request must be rejected")
	}
}

// TestIndexNeedsNoToken covers the page flow.
func TestIndexNeedsNoToken(t *testing.T) {
	_, ts := newTestServer(t)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("index = %d, want 200", res.StatusCode)
	}

	// A stale token from an older version must not break the page.
	res, err = http.Get(ts.URL + "/?t=whatever")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("index with a stale token = %d, want 200", res.StatusCode)
	}
}

// TestUninstallTakesSeveralApps makes sure the whole uninstall list goes
// through in one request.
//
// The uninstall list holds several applications, so the endpoint has to
// accept a list of bundle ids next to the list of paths.
func TestUninstallTakesSeveralApps(t *testing.T) {
	_, ts := newTestServer(t)

	res := request(t, ts, "POST", "/api/apps/uninstall", map[string]any{
		"bundleIDs": []string{"com.example.one", "com.example.two"},
		"paths":     []string{"/tmp/one", "/tmp/two"},
		"dryRun":    true,
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("uninstall = %d", res.StatusCode)
	}
	var out struct {
		DryRun  bool     `json:"dryRun"`
		Planned []string `json:"planned"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.DryRun {
		t.Error("the request must stay a dry run")
	}
	if len(out.Planned) != 2 {
		t.Errorf("planned = %v, want two paths", out.Planned)
	}
}

// TestUninstallRefusesEmptyList makes sure an empty request is rejected.
func TestUninstallRefusesEmptyList(t *testing.T) {
	_, ts := newTestServer(t)

	res := request(t, ts, "POST", "/api/apps/uninstall", map[string]any{
		"bundleIDs": []string{"com.example.one"},
		"paths":     []string{},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("uninstall without paths = %d, want 400", res.StatusCode)
	}
}

// TestStaticAssetsArePublic checks that the assets do not need a token.
func TestStaticAssetsArePublic(t *testing.T) {
	_, ts := newTestServer(t)
	for _, path := range []string{"/static/app.js", "/static/style.css", "/static/favicon.ico", "/static/favicon.svg", "/static/apple-touch-icon.png"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, res.StatusCode)
		}
	}
}

// TestEnvLifecycle walks through creating, updating and deleting a variable.
func TestEnvLifecycle(t *testing.T) {
	_, ts := newTestServer(t)

	res := request(t, ts, "POST", "/api/env/vars", map[string]any{
		"name": "MYMAC_TEST", "value": "bar",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", res.StatusCode)
	}

	// The generated files must exist in the temporary home.
	envSh := filepath.Join(os.Getenv("HOME"), ".config", "mymac", "env.sh")
	data, err := os.ReadFile(envSh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "export MYMAC_TEST='bar'") {
		t.Errorf("env.sh = %s", data)
	}
	zshenv := filepath.Join(os.Getenv("HOME"), ".zshenv")
	data, err = os.ReadFile(zshenv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), envs.MarkerStart) {
		t.Errorf("zshenv = %s", data)
	}

	// Update.
	res = request(t, ts, "PATCH", "/api/env/vars/MYMAC_TEST", map[string]any{"value": "baz"})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("update = %d", res.StatusCode)
	}

	// Delete.
	res = request(t, ts, "DELETE", "/api/env/vars/MYMAC_TEST", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("delete = %d", res.StatusCode)
	}
	data, _ = os.ReadFile(envSh)
	if strings.Contains(string(data), "MYMAC_TEST") {
		t.Errorf("env.sh still exports the variable: %s", data)
	}
}

// TestEnvRejectsBadName checks the name validation.
func TestEnvRejectsBadName(t *testing.T) {
	_, ts := newTestServer(t)
	for _, name := range []string{"", "1BAD", "HAS SPACE", "HAS-DASH"} {
		res := request(t, ts, "POST", "/api/env/vars", map[string]any{"name": name, "value": "x"})
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("create %q = %d, want 400", name, res.StatusCode)
		}
	}
	// PATH goes through its own editor.
	res := request(t, ts, "POST", "/api/env/vars", map[string]any{"name": "PATH", "value": "/x"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("create PATH = %d, want 400", res.StatusCode)
	}
}

// TestAuditTrail checks that the mutating calls are recorded.
func TestAuditTrail(t *testing.T) {
	_, ts := newTestServer(t)
	res := request(t, ts, "POST", "/api/env/vars", map[string]any{
		"name": "MYMAC_SECRET_TOKEN", "value": "s3cr3t-value",
	})
	res.Body.Close()

	res = request(t, ts, "GET", "/api/audit", nil)
	defer res.Body.Close()
	var doc struct {
		Items []audit.Entry `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) == 0 {
		t.Fatal("no audit entries")
	}
	for _, e := range doc.Items {
		if strings.Contains(e.Detail, "s3cr3t-value") {
			t.Errorf("the audit log leaked a secret: %q", e.Detail)
		}
	}
}

// TestCheckDeletable refuses the dangerous paths.
func TestCheckDeletable(t *testing.T) {
	bad := []string{
		"/",
		"/System/Library/CoreServices",
		"/usr/bin",
		"/bin",
		"/etc/passwd",
		"/private/var/db/thing",
	}
	for _, p := range bad {
		if err := checkDeletable(p); err == nil {
			t.Errorf("checkDeletable(%q) = nil, want an error", p)
		}
	}
	ok := []string{"/tmp/something", "/Users/me/Library/Caches/x"}
	for _, p := range ok {
		if err := checkDeletable(p); err != nil {
			t.Errorf("checkDeletable(%q) = %v, want nil", p, err)
		}
	}
}

// TestCheckDeletableProtectsConfig refuses the mymac config directory.
func TestCheckDeletableProtectsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := checkDeletable(filepath.Join(config.Root(), "env.json")); err == nil {
		t.Error("the mymac config must be protected")
	}
}

// TestHealthEndpoint checks the health payload.
func TestHealthEndpoint(t *testing.T) {
	_, ts := newTestServer(t)
	res := request(t, ts, "GET", "/api/health", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", res.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != config.Version {
		t.Errorf("version = %v", doc["version"])
	}
}

// TestPermStatus checks that the permission probe answers.
func TestPermStatus(t *testing.T) {
	_, ts := newTestServer(t)
	res := request(t, ts, "GET", "/api/perm/status", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("perm status = %d", res.StatusCode)
	}
	var doc struct {
		Items []struct {
			Target string `json:"target"`
			State  string `json:"state"`
			HowTo  string `json:"howTo"`
		} `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(doc.Items))
	}
	for _, it := range doc.Items {
		if it.HowTo == "" {
			t.Errorf("%s has no instructions", it.Target)
		}
	}
}

// TestProcsEndpoint checks that the process list is served.
func TestProcsEndpoint(t *testing.T) {
	_, ts := newTestServer(t)
	res := request(t, ts, "GET", "/api/procs", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("procs = %d", res.StatusCode)
	}
	var doc struct {
		Count     int `json:"count"`
		Processes []struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		} `json:"processes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count == 0 {
		t.Error("no processes were collected")
	}
}

// TestSignalRejectsUnknownSignal checks the signal validation.
func TestSignalRejectsUnknownSignal(t *testing.T) {
	_, ts := newTestServer(t)
	// A pid that is not in the table, so only the signal name is wrong.
	res := request(t, ts, "POST", "/api/procs/999999/signal", map[string]any{"sig": "NOPE"})
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("signal = %d, want 400", res.StatusCode)
	}
}

// TestSignalRefusesProtectedProcess checks the protection of pid 1.
func TestSignalRefusesProtectedProcess(t *testing.T) {
	srv, ts := newTestServer(t)
	// The process list must know about pid 1 for the check to trigger.
	_ = srv.procs.Collect(context.Background())
	res := request(t, ts, "POST", "/api/procs/1/signal", map[string]any{"sig": "KILL"})
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("signal on a protected process = %d, want 403", res.StatusCode)
	}
}
