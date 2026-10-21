// Package server hosts the local web UI and the JSON API.
//
// The server only ever binds to 127.0.0.1. Every request must clear two
// gates: a valid Host header and a same site request. Together those stop
// DNS rebinding and drive-by CSRF from a page the user happens to have open.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mymac/internal/apps"
	"mymac/internal/audit"
	"mymac/internal/cleaner"
	"mymac/internal/config"
	"mymac/internal/envs"
	"mymac/internal/errs"
	"mymac/internal/loginitems"
	"mymac/internal/perm"
	"mymac/internal/ports"
	"mymac/internal/procs"
	"mymac/internal/rules"
	"mymac/internal/sysinfo"
	"mymac/web"
)

// Server owns the HTTP listener and the API surface.
type Server struct {
	port     int
	listener net.Listener
	mux      *http.ServeMux
	audit    *audit.Logger
	logger   *log.Logger

	env *envs.Manager

	procs   *procs.Collector
	sysinfo *sysinfo.Collector
	apps    *apps.Scanner
	cleaner *cleaner.Scanner
	alerter *procs.Alerter

	ports       *ports.Collector
	portActions *ports.ActionManager
	portRules   *rules.Store

	loginItems *loginitems.Collector

	cleanJobs *cleaner.JobManager
}

// Options configures a Server.
type Options struct {
	Audit  *audit.Logger
	Logger *log.Logger
	// Env is the environment variable manager. When nil the server builds
	// one with the real filesystem and the real launchctl.
	Env *envs.Manager
	// Procs is the process collector. When nil the server creates one.
	Procs *procs.Collector
	// Sysinfo is the system overview collector. When nil the server
	// creates one.
	Sysinfo *sysinfo.Collector
	// Apps scans the installed applications. When nil the server creates one.
	Apps *apps.Scanner
	// Cleaner scans the cleanable files. When nil the server creates one.
	Cleaner *cleaner.Scanner
	// Alerter watches the thresholds. When nil the server creates one.
	Alerter *procs.Alerter
	// PortRules holds the system service rules. When nil the server
	// creates one.
	PortRules *rules.Store
	// Ports collects the port view. When nil the server creates one.
	Ports *ports.Collector
	// PortActions stops, kills and restarts processes. When nil the server
	// creates one.
	PortActions *ports.ActionManager
	// LoginItems collects the startup items. When nil the server creates one.
	LoginItems *loginitems.Collector
	// CleanJobs runs the long file scans. When nil the server creates one.
	CleanJobs *cleaner.JobManager
}

// New builds a server.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "mymac: ", log.LstdFlags)
	}
	if opts.Audit == nil {
		opts.Audit = audit.New(config.AuditLog())
	}
	if opts.Env == nil {
		opts.Env = envs.NewManager(envs.Options{Audit: opts.Audit})
	}
	if opts.Procs == nil {
		opts.Procs = procs.NewCollector(2 * time.Second)
	}
	if opts.Sysinfo == nil {
		opts.Sysinfo = sysinfo.NewCollector()
	}
	if opts.Apps == nil {
		opts.Apps = apps.NewScanner()
	}
	if opts.Cleaner == nil {
		opts.Cleaner = cleaner.NewScanner()
	}
	if opts.Alerter == nil {
		opts.Alerter = procs.NewAlerter(procs.OSNotifier{}, procs.DefaultRules())
	}
	if opts.PortRules == nil {
		opts.PortRules = rules.NewStore("")
	}
	if opts.Ports == nil {
		opts.Ports = ports.NewCollector(opts.PortRules, 2*time.Second)
	}
	if opts.PortActions == nil {
		opts.PortActions = ports.NewActionManager(opts.PortRules, 5*time.Second)
	}
	if opts.LoginItems == nil {
		opts.LoginItems = loginitems.NewCollector()
	}
	if opts.CleanJobs == nil {
		opts.CleanJobs = cleaner.NewJobManager()
	}
	s := &Server{
		mux:     http.NewServeMux(),
		audit:   opts.Audit,
		logger:  opts.Logger,
		env:     opts.Env,
		procs:   opts.Procs,
		sysinfo: opts.Sysinfo,
		apps:    opts.Apps,
		cleaner: opts.Cleaner,
		alerter: opts.Alerter,

		ports:       opts.Ports,
		portActions: opts.PortActions,
		portRules:   opts.PortRules,

		loginItems: opts.LoginItems,
		cleanJobs:  opts.CleanJobs,
	}
	s.routes()
	return s, nil
}

// Port returns the bound TCP port.
func (s *Server) Port() int { return s.port }

// Addr returns the base URL of the server.
func (s *Server) Addr() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// Listen binds a loopback listener. Port 0 picks a random free port.
func (s *Server) ListenOn(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	if tc, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tc.Port
	}
	s.listener = ln
	return nil
}

// Listener returns the bound listener.
func (s *Server) Listener() net.Listener { return s.listener }

// Listen binds a random free port on the loopback interface.
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	if tc, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tc.Port
	}
	s.listener = ln
	return ln, nil
}

// NewLogger returns the default stderr logger.
func NewLogger() *log.Logger {
	return log.New(os.Stderr, "mymac: ", log.LstdFlags)
}

// Serve runs the HTTP server until the context is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpSrv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

// ---------------------------------------------------------------- middleware

// withGuards applies the Host, Origin and Fetch Metadata checks.
//
// The server only listens on the loopback interface, so the only way to reach
// it from outside is a page in the browser. These three checks close that
// door: the Host check stops DNS rebinding, the Origin check stops cross site
// requests and the Fetch Metadata check catches the requests that arrive
// without an Origin header.
func (s *Server) withGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validHost(r.Host) {
			http.Error(w, "invalid host", http.StatusMisdirectedRequest)
			return
		}
		if !s.validOrigin(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if !s.validFetchSite(r) {
			http.Error(w, "invalid fetch site", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validHost rejects requests whose Host is not one of our loopback names.
// This is the primary defence against DNS rebinding: a hostile page may point
// a name at 127.0.0.1, but the browser will still send the attacker's
// hostname in the Host header.
func (s *Server) validHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		// No port: bare host header, only allow the loopback names.
		return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
	}
	switch h {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	// A port, when present, must be ours.
	port, err := strconv.Atoi(p)
	if err != nil {
		return false
	}
	return port == s.port
}

// validOrigin rejects cross-site requests. A missing Origin header is allowed
// because simple GET navigations do not send one.
func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	allowed := map[string]bool{
		fmt.Sprintf("http://127.0.0.1:%d", s.port): true,
		fmt.Sprintf("http://localhost:%d", s.port): true,
	}
	return allowed[origin]
}

// validFetchSite rejects requests a browser marked as coming from another
// site.
//
// Browsers send Sec-Fetch-Site on every request, so a page on another origin
// cannot drive the API even when it manages to leave the Origin header out.
// A missing header is allowed: requests from curl and other tools do not
// carry it, and those tools are local anyway.
func (s *Server) validFetchSite(r *http.Request) bool {
	switch strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")) {
	case "", "none", "same-origin", "same-site":
		return true
	}
	return false
}

// ------------------------------------------------------------------- routing

func (s *Server) routes() {
	// Page: the interface is served to anyone who can reach the loopback
	// port, so a stale ?t= from an older version is simply ignored.
	s.mux.Handle("GET /{$}", s.withGuards(http.HandlerFunc(s.handleIndex)))

	// Static assets carry no data, so they stay unauthenticated.
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))

	api := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle("GET "+pattern, s.withGuards(h))
		s.mux.Handle("POST "+pattern, s.withGuards(h))
		s.mux.Handle("PUT "+pattern, s.withGuards(h))
		s.mux.Handle("PATCH "+pattern, s.withGuards(h))
		s.mux.Handle("DELETE "+pattern, s.withGuards(h))
	}

	api("/api/health", s.handleHealth)
	api("/api/perm/status", s.handlePermStatus)
	api("/api/perm/open", s.handlePermOpen)
	api("/api/audit", s.handleAudit)

	// Environment variables.
	s.envRoutes()

	// Processes and system overview.
	s.mux.Handle("GET /api/procs", s.withGuards(http.HandlerFunc(s.handleProcs)))
	s.mux.Handle("GET /api/procs/stream", s.withGuards(http.HandlerFunc(s.handleProcsStream)))
	s.mux.Handle("GET /api/procs/{pid}", s.withGuards(http.HandlerFunc(s.handleProcDetail)))
	s.mux.Handle("GET /api/procs/{pid}/sample", s.withGuards(http.HandlerFunc(s.handleProcSample)))
	s.mux.Handle("GET /api/procs/{pid}/files", s.withGuards(http.HandlerFunc(s.handleProcFiles)))
	s.mux.Handle("POST /api/procs/{pid}/signal", s.withGuards(http.HandlerFunc(s.handleProcSignal)))
	s.mux.Handle("POST /api/procs/{pid}/renice", s.withGuards(http.HandlerFunc(s.handleProcRenice)))
	s.mux.Handle("GET /api/system/overview", s.withGuards(http.HandlerFunc(s.handleOverview)))

	// Port management.
	s.mux.Handle("GET /api/ports/snapshot", s.withGuards(http.HandlerFunc(s.handlePortsSnapshot)))
	s.mux.Handle("POST /api/ports/process/{pid}/{action}", s.withGuards(http.HandlerFunc(s.handlePortAction)))
	s.mux.Handle("GET /api/ports/rules", s.withGuards(http.HandlerFunc(s.handlePortRules)))
	s.mux.Handle("POST /api/ports/rules/reload", s.withGuards(http.HandlerFunc(s.handlePortRulesReload)))
	s.mux.Handle("POST /api/ports/rules/override", s.withGuards(http.HandlerFunc(s.handlePortRulesOverride)))

	// Startup items.
	s.mux.Handle("GET /api/loginitems", s.withGuards(http.HandlerFunc(s.handleLoginItems)))
	s.mux.Handle("POST /api/loginitems/state", s.withGuards(http.HandlerFunc(s.handleLoginItemState)))
	s.mux.Handle("POST /api/loginitems/remove", s.withGuards(http.HandlerFunc(s.handleLoginItemRemove)))
	s.mux.Handle("GET /api/procs/alerts", s.withGuards(http.HandlerFunc(s.handleAlertRules)))
	s.mux.Handle("POST /api/procs/alerts", s.withGuards(http.HandlerFunc(s.handleAlertRulesUpdate)))

	// Applications and cleaning.
	s.mux.Handle("GET /api/apps", s.withGuards(http.HandlerFunc(s.handleApps)))
	s.mux.Handle("GET /api/apps/icon", s.withGuards(http.HandlerFunc(s.handleAppIcon)))
	s.mux.Handle("GET /api/apps/{id}/residual", s.withGuards(http.HandlerFunc(s.handleAppResiduals)))
	s.mux.Handle("GET /api/apps/residuals", s.withGuards(http.HandlerFunc(s.handleAppResidualsGrouped)))
	s.mux.Handle("POST /api/apps/uninstall", s.withGuards(http.HandlerFunc(s.handleAppUninstall)))
	s.mux.Handle("GET /api/trash/status", s.withGuards(http.HandlerFunc(s.handleTrashStatus)))
	s.mux.Handle("GET /api/clean/scan", s.withGuards(http.HandlerFunc(s.handleCleanScan)))
	s.mux.Handle("GET /api/clean/large", s.withGuards(http.HandlerFunc(s.handleCleanLarge)))
	s.mux.Handle("GET /api/clean/duplicates", s.withGuards(http.HandlerFunc(s.handleCleanDuplicates)))
	s.mux.Handle("GET /api/clean/job", s.withGuards(http.HandlerFunc(s.handleCleanJob)))
	s.mux.Handle("POST /api/clean/execute", s.withGuards(http.HandlerFunc(s.handleCleanExecute)))
}

// envRoutes registers the environment variable endpoints.
//
// The routes are registered per method because the same path carries
// different verbs with different bodies.
func (s *Server) envRoutes() {
	g := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle("GET "+pattern, s.withGuards(h))
	}
	p := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle("POST "+pattern, s.withGuards(h))
	}
	patch := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle("PATCH "+pattern, s.withGuards(h))
	}
	del := func(pattern string, h http.HandlerFunc) {
		s.mux.Handle("DELETE "+pattern, s.withGuards(h))
	}

	g("/api/env/vars", s.handleEnvVars)
	p("/api/env/vars", s.handleEnvCreate)
	patch("/api/env/vars/{name}", s.handleEnvUpdate)
	del("/api/env/vars/{name}", s.handleEnvDelete)
	p("/api/env/vars/{name}/apply", s.handleEnvApply)

	g("/api/env/path", s.handleEnvPathGet)
	p("/api/env/path", s.handleEnvPathSet)

	g("/api/env/export", s.handleEnvExport)
	p("/api/env/import/preview", s.handleEnvImportPreview)
	p("/api/env/import", s.handleEnvImport)

	g("/api/env/backups", s.handleEnvBackups)
	p("/api/env/restore", s.handleEnvRestore)

	g("/api/env/status", s.handleEnvStatus)
	p("/api/env/marker/repair", s.handleEnvMarkerRepair)
	p("/api/env/marker/remove", s.handleEnvMarkerRemove)
	p("/api/env/agent/install", s.handleEnvAgentInstall)
	p("/api/env/agent/uninstall", s.handleEnvAgentUninstall)
}

// staticHandler serves the embedded assets.
func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		b, ct, err := web.File(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// handleIndex serves the single page app.
//
// The interface is not behind a token: mymac only listens on the loopback
// interface and every request still has to pass the Host, Origin and Fetch
// Metadata checks.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b, ct, err := web.File("index.html")
	if err != nil {
		http.Error(w, "assets missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// ------------------------------------------------------------------ handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": config.Version,
		"uptime":  time.Since(startTime).Round(time.Second).String(),
		"pid":     os.Getpid(),
		"port":    s.port,
	})
}

var startTime = time.Now()

func (s *Server) handlePermStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": perm.All(r.Context())})
}

func (s *Server) handlePermOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target perm.Target `json:"target"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := perm.OpenPrivacyPane(body.Target); err != nil {
		writeError(w, http.StatusInternalServerError, errs.Wrap(err, errs.KindInternal, "无法打开系统设置"))
		return
	}
	s.audit.Record(audit.ActionPermGrant, string(body.Target), "opened system settings", audit.ResultOK)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	items, err := s.audit.Read(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errs.Wrap(err, errs.KindInternal, "读取审计日志失败"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ------------------------------------------------------------------- helpers

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errs.New(errs.KindInvalid, "请求体为空")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return errs.Wrap(err, errs.KindInvalid, "JSON 解析失败")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The response is already committed; nothing useful left to do.
		return
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	e := errs.As(err)
	if e == nil {
		e = &errs.Error{Kind: errs.KindInternal, Message: "unknown error"}
	}
	code = statusFor(e.Kind, code)
	writeJSON(w, code, map[string]any{"error": e})
}

func statusFor(kind errs.Kind, fallback int) int {
	switch kind {
	case errs.KindInvalid:
		return http.StatusBadRequest
	case errs.KindUnauthorized:
		return http.StatusUnauthorized
	case errs.KindPermission, errs.KindSIP:
		return http.StatusForbidden
	case errs.KindNotFound:
		return http.StatusNotFound
	case errs.KindProcessGone, errs.KindFileBusy:
		return http.StatusConflict
	}
	return fallback
}

// OpenBrowser launches the default browser at the server URL.
//
// The child is reaped in the background: without that it would stay around as
// a zombie until mymac exits.
func OpenBrowser(url string) error {
	cmd := exec.Command("/usr/bin/open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// WaitForSignal blocks until the process is asked to terminate.
func WaitForSignal() os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return <-ch
}
