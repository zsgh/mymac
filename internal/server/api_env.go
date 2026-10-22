package server

import (
	"net/http"
	"strings"

	"mymac/internal/api"
	"mymac/internal/audit"
	"mymac/internal/envs"
	"mymac/internal/errs"
)

// handleEnvVars returns the full multi-layer environment view.
func (s *Server) handleEnvVars(w http.ResponseWriter, r *http.Request) {
	snap, err := s.env.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// The verdict of the AI, if there was an analysis.
	for i := range snap.Managed {
		if v := s.verdictOf(AnalysisEnv, snap.Managed[i].Name); v != nil {
			snap.Managed[i].AI = v
		}
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleEnvCreate adds a variable.
func (s *Server) handleEnvCreate(w http.ResponseWriter, r *http.Request) {
	var body api.EnvVarCreateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	mv, err := s.env.Create(r.Context(), name, body.Value, body.Note)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, mv)
}

// handleEnvUpdate changes a variable.
func (s *Server) handleEnvUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body api.EnvVarUpdateRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mv, err := s.env.Update(r.Context(), name, body.Value, body.Note, body.Enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, mv)
}

// handleEnvDelete removes a variable from every layer.
func (s *Server) handleEnvDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.env.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, api.EnvVarDeleteResponse{OK: true, Name: name})
}

// handleEnvApply re-pushes a variable to the launchd layer.
func (s *Server) handleEnvApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	mv, err := s.env.Reapply(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, mv)
}

// handleEnvPathGet returns the PATH editor data.
func (s *Server) handleEnvPathGet(w http.ResponseWriter, r *http.Request) {
	view, err := s.env.Path(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleEnvPathSet stores the PATH fragments.
func (s *Server) handleEnvPathSet(w http.ResponseWriter, r *http.Request) {
	var body api.EnvPathSetRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.env.SetPath(r.Context(), body.Prepend, body.Append)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleEnvExport renders the managed variables.
func (s *Server) handleEnvExport(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("format") {
	case "env":
		text, err := s.env.ExportEnv()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=mymac.env")
		_, _ = w.Write([]byte(text))
	default:
		doc, err := s.env.ExportJSON()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename=mymac-env.json")
		writeJSON(w, http.StatusOK, doc)
	}
}

// handleEnvImportPreview shows what an import would change.
func (s *Server) handleEnvImportPreview(w http.ResponseWriter, r *http.Request) {
	var body api.EnvImportRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	changes, err := s.env.ImportPreview(body.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, api.EnvImportPreviewResponse{
		Add:    filterChanges(changes, "add"),
		Update: filterChanges(changes, "update"),
		All:    changes,
	})
}

func filterChanges(changes []envs.ImportChange, action string) []envs.ImportChange {
	out := []envs.ImportChange{}
	for _, c := range changes {
		if c.Action == action {
			out = append(out, c)
		}
	}
	return out
}

// handleEnvImport applies an import.
func (s *Server) handleEnvImport(w http.ResponseWriter, r *http.Request) {
	var body api.EnvImportRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	changes, err := s.env.Import(r.Context(), body.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, api.EnvImportResponse{Changes: changes, Applied: len(changes)})
}

// handleEnvBackups lists the stored backups.
func (s *Server) handleEnvBackups(w http.ResponseWriter, r *http.Request) {
	items, err := envs.ListBackups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.EnvBackupsResponse{Items: items})
}

// handleEnvRestore restores one backup.
func (s *Server) handleEnvRestore(w http.ResponseWriter, r *http.Request) {
	var body api.EnvRestoreRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	path, err := envs.RestoreBackup(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.audit.Log(audit.Entry{Action: audit.ActionEnvRestore, Target: path,
		Detail: "从备份恢复: " + body.Name, Result: audit.ResultOK})
	writeJSON(w, http.StatusOK, api.EnvRestoreResponse{OK: true, Path: path})
}

// handleEnvStatus reports the health of every layer.
func (s *Server) handleEnvStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := s.env.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.EnvStatusResponse{
		Marker:   snap.Marker,
		Agent:    snap.Agent,
		Managed:  len(snap.Managed),
		Warnings: snap.Warnings,
	})
}

// handleEnvMarkerRepair rewrites the managed block in ~/.zshenv.
func (s *Server) handleEnvMarkerRepair(w http.ResponseWriter, r *http.Request) {
	rep, err := s.env.RepairMarker(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleEnvMarkerRemove strips the managed block from ~/.zshenv.
func (s *Server) handleEnvMarkerRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.env.RemoveMarker(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OKResponse{OK: true})
}

// handleEnvAgentInstall creates the login launch agent.
func (s *Server) handleEnvAgentInstall(w http.ResponseWriter, r *http.Request) {
	if err := s.env.InstallAgent(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OKResponse{OK: true})
}

// handleEnvAgentUninstall removes the login launch agent.
func (s *Server) handleEnvAgentUninstall(w http.ResponseWriter, r *http.Request) {
	if err := s.env.UninstallAgent(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OKResponse{OK: true})
}

// ensure the errs package stays referenced even if handlers change.
var _ = errs.KindInternal
