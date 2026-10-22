package server

import (
	"net/http"
	"strings"

	"mymac/internal/api"
	"mymac/internal/errs"
)

// handleAIAnalyze starts an analysis.
//
// The analysis runs in the background: asking a model takes a while, and
// the interface polls the status until it is done.
func (s *Server) handleAIAnalyze(w http.ResponseWriter, r *http.Request) {
	var body api.AIAnalyzeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Analysis)
	mode := strings.TrimSpace(body.Mode)
	if mode == "" {
		mode = api.AIModeCache
	}
	if err := s.startAnalysis(r.Context(), name, mode); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.aiState(name))
}

// handleAIStatus returns the state of the analyses.
//
// The interface shows how far the analysis got and, when it failed, why.
func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("analysis"))
	if name == "" {
		writeJSON(w, http.StatusOK, api.AIStatusResponse{Analyses: s.analysisStates()})
		return
	}
	if _, ok := analysisByName(name); !ok {
		writeError(w, http.StatusNotFound, errs.New(errs.KindNotFound, "没有这个分析：%s", name))
		return
	}
	writeJSON(w, http.StatusOK, api.AIStatusResponse{
		Analyses: []api.AIAnalysisState{s.aiState(name)},
	})
}

// aiState returns the state of one analysis.
func (s *Server) aiState(name string) api.AIAnalysisState {
	for _, st := range s.analysisStates() {
		if st.Name == name {
			return api.AIAnalysisState{
				Name:    st.Name,
				Running: st.Running,
				Count:   st.Count,
				At:      st.At,
				Model:   st.Model,
				Error:   st.Error,
			}
		}
	}
	return api.AIAnalysisState{Name: name}
}
