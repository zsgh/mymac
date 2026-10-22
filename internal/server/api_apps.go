package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mymac/internal/apps"
	"mymac/internal/audit"
	"mymac/internal/cleaner"
	"mymac/internal/config"
	"mymac/internal/errs"
	"mymac/internal/trash"
)

// handleApps lists the installed applications.
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	rescan := r.URL.Query().Get("rescan") == "1"
	list, err := s.apps.Scan(r.Context(), rescan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	running := apps.RunningApps()
	items := make([]*apps.App, 0, len(list))
	pending := 0
	for _, a := range list {
		a.Running = running[a.Path]
		if !a.SizeKnown {
			pending++
		}
		items = append(items, a)
	}
	// A rescan also triggers a fresh measurement, so the sizes show up
	// without waiting for the background warmer.
	//
	// The request context is dropped on purpose: it is cancelled as soon as
	// the response is written, and that would abort the measurement.
	if rescan {
		go s.apps.WarmSizes(context.WithoutCancel(r.Context()))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   items,
		"count":   len(items),
		"pending": pending,
	})
}

// handleAppResiduals lists the leftovers of one application.
func (s *Server) handleAppResiduals(w http.ResponseWriter, r *http.Request) {
	bundleID := r.PathValue("id")
	list, err := s.apps.Scan(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var target *apps.App
	for _, a := range list {
		if a.BundleID == bundleID {
			target = a
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, errs.New(errs.KindNotFound, "找不到应用 %s", bundleID))
		return
	}
	residuals, err := apps.FindResiduals(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": residuals,
		"app":   target,
		"count": len(residuals),
	})
}

// handleTrashStatus reports whether the Finder automation is usable.
func (s *Server) handleTrashStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, trash.Check(r.Context()))
}

// handleAppResidualsGrouped returns the leftovers of several applications,
// grouped per application.
//
// It is the data behind the batch uninstall: one request, one list, every
// item knows which application it belongs to.
func (s *Server) handleAppResidualsGrouped(w http.ResponseWriter, r *http.Request) {
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	list, err := s.apps.Scan(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	byID := map[string]*apps.App{}
	for _, a := range list {
		byID[a.BundleID] = a
	}

	groups := make([]apps.Group, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		app, ok := byID[id]
		if !ok {
			continue
		}
		residuals, err := apps.FindResiduals(app)
		if err != nil {
			continue
		}
		groups = append(groups, apps.Group{
			BundleID: app.BundleID,
			Name:     app.Name,
			Path:     app.Path,
			Version:  app.Version,
			BundleSz: app.BundleSz,
			Running:  app.Running,
			IsSystem: app.IsSystem,
			Items:    residuals,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "count": len(groups)})
}

// handleAppUninstall moves the selected leftovers to the Trash.
//
// The application bundle itself is only removed when its path is part of the
// request, so the residual list and the bundle can be handled together.
//
// The whole uninstall list goes through here in one request: the Finder
// automation is asked for once instead of once per application.
func (s *Server) handleAppUninstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BundleID  string   `json:"bundleID"`
		BundleIDs []string `json:"bundleIDs"`
		Paths     []string `json:"paths"`
		DryRun    bool     `json:"dryRun"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Paths) == 0 {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "没有选择要删除的项目"))
		return
	}

	// Refuse to touch the system volume or the mymac config directory.
	for _, p := range body.Paths {
		if err := checkDeletable(p); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
	}

	if body.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"dryRun":  true,
			"moved":   []string{},
			"failed":  []trash.FailedItem{},
			"planned": body.Paths,
		})
		return
	}

	res, err := trash.Move(r.Context(), body.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionAppUninst,
		Target: uninstallTarget(body.BundleIDs, body.BundleID, len(body.Paths)),
		Detail: summarize(res),
		Result: resultOf(res),
	})
	writeJSON(w, http.StatusOK, res)
}

// uninstallTarget names the applications an uninstall request covers.
//
// The list can hold several applications, so the audit log gets all of
// them; the single bundle id is kept for older callers.
func uninstallTarget(ids []string, single string, count int) string {
	if len(ids) == 0 && single != "" {
		return single
	}
	if len(ids) == 0 {
		return strconv.Itoa(count) + " 项"
	}
	return strings.Join(ids, ", ")
}

// handleAppIcon serves the icon of an application.
//
// The path is looked up in the scanned list, so only known bundles can be
// asked for; this keeps the endpoint from turning into a file reader.
func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	bundleID := r.URL.Query().Get("id")
	if bundleID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	list, err := s.apps.Scan(r.Context(), false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var target *apps.App
	for _, a := range list {
		if a.BundleID == bundleID {
			target = a
			break
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	icon, err := apps.Icon(target.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(icon)
}

// handleCleanScan returns the cleanable items.
func (s *Server) handleCleanScan(w http.ResponseWriter, r *http.Request) {
	rescan := r.URL.Query().Get("rescan") == "1"
	writeJSON(w, http.StatusOK, s.cleaner.Scan(r.Context(), rescan))
}

// handleCleanExecute moves the selected items to the Trash.
func (s *Server) handleCleanExecute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths  []string `json:"paths"`
		DryRun bool     `json:"dryRun"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Paths) == 0 {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "没有选择要清理的项目"))
		return
	}
	for _, p := range body.Paths {
		if err := checkDeletable(p); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
	}
	if body.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"dryRun":  true,
			"moved":   []string{},
			"failed":  []trash.FailedItem{},
			"planned": body.Paths,
		})
		return
	}
	res, err := trash.Move(r.Context(), body.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionCleanExec,
		Target: strings.Join(body.Paths, ", "),
		Detail: summarize(res),
		Result: resultOf(res),
	})
	writeJSON(w, http.StatusOK, res)
}

// handleCleanLarge starts a large file scan.
//
// The scan takes tens of seconds, so it runs in the background and the UI
// polls the job.
func (s *Server) handleCleanLarge(w http.ResponseWriter, r *http.Request) {
	minSize := uint64(500 * 1024 * 1024)
	if v := r.URL.Query().Get("minSize"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			minSize = n
		}
	}
	job := s.cleanJobs.Start(r.Context(), cleaner.JobLargeFiles, minSize)
	writeJSON(w, http.StatusOK, job)
}

// handleCleanDuplicates starts a duplicate file scan.
func (s *Server) handleCleanDuplicates(w http.ResponseWriter, r *http.Request) {
	minSize := uint64(1024 * 1024)
	if v := r.URL.Query().Get("minSize"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			minSize = n
		}
	}
	job := s.cleanJobs.Start(r.Context(), cleaner.JobDuplicates, minSize)
	writeJSON(w, http.StatusOK, job)
}

// handleCleanJob returns the state of a scan job.
func (s *Server) handleCleanJob(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id != "" {
		if job, ok := s.cleanJobs.Get(id); ok {
			writeJSON(w, http.StatusOK, job)
			return
		}
	}
	// Without an id the latest job of the requested kind is returned.
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = cleaner.JobLargeFiles
	}
	job := s.cleanJobs.Latest(kind)
	if job == nil {
		writeJSON(w, http.StatusNotFound, errs.New(errs.KindNotFound, "没有扫描任务"))
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// checkDeletable refuses paths that must never be deleted.
func checkDeletable(path string) error {
	if path == "" {
		return errs.New(errs.KindInvalid, "路径为空")
	}
	clean := filepath.Clean(path)
	if clean == "/" {
		return errs.New(errs.KindPermission, "拒绝删除根目录")
	}
	// The read-only system volume is never touched.
	if clean == "/System" || strings.HasPrefix(clean, "/System/") {
		return errs.New(errs.KindSIP, "拒绝修改只读系统卷 /System")
	}
	// The system command directories are never touched either.
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/etc", "/var", "/opt"} {
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return errs.New(errs.KindSIP, "拒绝修改系统目录 %s", dir)
		}
	}
	if strings.HasPrefix(clean, "/private/var/db/") {
		return errs.New(errs.KindSIP, "拒绝修改系统数据库")
	}
	if isProtectedConfig(clean) {
		return errs.New(errs.KindPermission, "拒绝删除 mymac 的配置目录")
	}
	return nil
}

// isProtectedConfig reports whether the path is inside the mymac config.
func isProtectedConfig(path string) bool {
	root := config.Root()
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// summarize renders a trash result for the audit log.
func summarize(res *trash.Result) string {
	return "已移入废纸篓 " + itoa(len(res.Moved)) + " 项" +
		"，失败 " + itoa(len(res.Failed)) + " 项"
}

// resultOf maps a trash result to an audit result.
func resultOf(res *trash.Result) string {
	if len(res.Failed) > 0 {
		return audit.ResultFailed
	}
	return audit.ResultOK
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
