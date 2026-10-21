package server

import (
	"net/http"
	"strings"

	"mymac/internal/audit"
	"mymac/internal/errs"
	"mymac/internal/loginitems"
)

// handleLoginItems lists the startup items.
func (s *Server) handleLoginItems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loginItems.Collect(r.Context()))
}

// handleLoginItemState enables or disables a startup item.
func (s *Server) handleLoginItemState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label   string `json:"label"`
		Enabled bool   `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := loginitems.SetEnabled(r.Context(), body.Label, body.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state := "已禁用"
	if body.Enabled {
		state = "已启用"
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionPermGrant,
		Target: body.Label,
		Detail: state,
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": body.Label, "enabled": body.Enabled})
}

// handleLoginItemRemove moves a LaunchAgent definition to the Trash.
func (s *Server) handleLoginItemRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "缺少路径"))
		return
	}
	if err := loginitems.Remove(r.Context(), body.Path); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionAppUninst,
		Target: body.Path,
		Detail: "启动项已移入废纸篓",
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
