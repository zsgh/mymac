package server

import (
	"net/http"
	"strconv"
	"strings"

	"mymac/internal/audit"
	"mymac/internal/errs"
	"mymac/internal/ports"
	"mymac/internal/rules"
)

// handlePortsSnapshot returns the port view.
func (s *Server) handlePortsSnapshot(w http.ResponseWriter, r *http.Request) {
	includeSystem := r.URL.Query().Get("include_system") == "1"
	snap := s.ports.Snapshot(r.Context())

	// The system entries are hidden by default, but their summary is still
	// reported so the user knows they are there.
	visible := make([]ports.Process, 0, len(snap.Processes))
	for _, p := range snap.Processes {
		if p.Decision.IsSystem && !includeSystem {
			continue
		}
		visible = append(visible, p)
	}
	out := *snap
	out.Processes = visible
	writeJSON(w, http.StatusOK, &out)
}

// handlePortAction stops, kills or restarts a process.
func (s *Server) handlePortAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 PID"))
		return
	}
	var body struct {
		CreateTime int64 `json:"createTime"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.CreateTime <= 0 {
		// Older clients may send create_time instead.
		if v := r.URL.Query().Get("create_time"); v != "" {
			body.CreateTime, _ = strconv.ParseInt(v, 10, 64)
		}
	}

	result, status := s.portActions.Do(r.Context(), action, int32(pid), body.CreateTime)
	s.audit.Log(audit.Entry{
		Action: audit.ActionProcSignal,
		Target: strconv.Itoa(pid),
		Detail: action + ": " + result.Message,
		Result: map[bool]string{true: audit.ResultOK, false: audit.ResultFailed}[result.OK],
	})
	if !result.OK && status == http.StatusOK {
		// A timeout is not a failure, the operation did happen.
		writeJSON(w, http.StatusOK, &result)
		return
	}
	if !result.OK {
		writeJSON(w, status, map[string]any{"ok": false, "error": errs.New(errs.KindInternal, "%s", result.Message)})
		return
	}
	writeJSON(w, http.StatusOK, &result)
}

// handlePortRules returns the rule summary.
func (s *Server) handlePortRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.portRules.Summary())
}

// handlePortRulesReload reads the user rule file again.
func (s *Server) handlePortRulesReload(w http.ResponseWriter, r *http.Request) {
	if err := s.portRules.Reload(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.portRules.Summary())
}

// handlePortRulesOverride adds or removes one user rule.
func (s *Server) handlePortRulesOverride(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode   string `json:"mode"`
		Kind   string `json:"kind"`
		Value  string `json:"value"`
		Remove bool   `json:"remove"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.portRules.Override(body.Mode, body.Kind, body.Value, body.Remove); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionProcSignal,
		Target: "rules",
		Detail: body.Mode + "/" + body.Kind + "=" + body.Value,
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, s.portRules.Summary())
}

// ensure the rules package stays referenced.
var _ = rules.SourceBuiltin
var _ = strings.TrimSpace
