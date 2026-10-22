package server

import (
	"net/http"
	"strconv"
	"strings"

	"mymac/internal/api"
	"mymac/internal/audit"
	"mymac/internal/errs"
	"mymac/internal/loginitems"
)

// handleLoginItems lists the startup items.
func (s *Server) handleLoginItems(w http.ResponseWriter, r *http.Request) {
	list := s.loginItems.Collect(r.Context())
	// The verdict of the AI, if there was an analysis.
	for i := range list.Items {
		if v := s.verdictOf(AnalysisLoginItems, list.Items[i].Label); v != nil {
			list.Items[i].AI = v
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleLoginItemState enables or disables a startup item.
func (s *Server) handleLoginItemState(w http.ResponseWriter, r *http.Request) {
	var body api.LoginItemStateRequest
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
	writeJSON(w, http.StatusOK, api.LoginItemStateResponse{
		OK:      true,
		Label:   body.Label,
		Enabled: body.Enabled,
	})
}

// handleLoginItemRemove moves a LaunchAgent definition to the Trash.
func (s *Server) handleLoginItemRemove(w http.ResponseWriter, r *http.Request) {
	var body api.LoginItemRemoveRequest
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
	writeJSON(w, http.StatusOK, api.OKResponse{OK: true})
}

// handleLoginItemGroupRemove deletes every startup item of one program.
//
// The files go to the Trash, so a mistake can be undone. The items that have
// no file of their own — the services the system submits for an app, the old
// login items — come back with the app and are reported as skipped.
func (s *Server) handleLoginItemGroupRemove(w http.ResponseWriter, r *http.Request) {
	var body api.LoginItemGroupRemoveRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.loginItems.RemoveGroup(r.Context(), body.Group)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	detail := "移入废纸篓 " + strconv.Itoa(len(res.Moved)) + " 项"
	if len(res.Skipped) > 0 {
		detail += "，跳过 " + strconv.Itoa(len(res.Skipped)) + " 项"
	}
	if len(res.Failed) > 0 {
		detail += "，失败 " + strconv.Itoa(len(res.Failed)) + " 项"
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionAppUninst,
		Target: res.Name,
		Detail: detail,
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, api.LoginItemGroupRemoveResponse{
		OK:      len(res.Failed) == 0,
		Group:   res.Key,
		Name:    res.Name,
		Total:   res.Total,
		Moved:   res.Moved,
		Failed:  res.Failed,
		Skipped: res.Skipped,
	})
}

// handleLoginItemGroupState turns every startup item of one program on or
// off.
//
// One app often brings several startup items with it, and the user thinks
// of "the program that starts at login", not of the individual labels.
func (s *Server) handleLoginItemGroupState(w http.ResponseWriter, r *http.Request) {
	var body api.LoginItemGroupStateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.loginItems.SetGroupEnabled(r.Context(), body.Group, body.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	state := "已禁用"
	if body.Enabled {
		state = "已启用"
	}
	detail := state + " " + strconv.Itoa(res.Changed) + " 项"
	if len(res.Skipped) > 0 {
		detail += "，跳过 " + strconv.Itoa(len(res.Skipped)) + " 项旧式登录项"
	}
	if len(res.Failed) > 0 {
		detail += "，失败 " + strconv.Itoa(len(res.Failed)) + " 项"
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionPermGrant,
		Target: res.Name,
		Detail: detail,
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, api.LoginItemGroupStateResponse{
		OK:      len(res.Failed) == 0,
		Group:   res.Key,
		Name:    res.Name,
		Total:   res.Total,
		Changed: res.Changed,
		Skipped: res.Skipped,
		Failed:  res.Failed,
	})
}
