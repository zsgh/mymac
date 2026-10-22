package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mymac/internal/api"
	"mymac/internal/audit"
	"mymac/internal/errs"
	"mymac/internal/procs"
	"mymac/internal/sysinfo"
)

// handleProcs returns the cached process list.
func (s *Server) handleProcs(w http.ResponseWriter, r *http.Request) {
	snap := s.procs.Snapshot(r.Context())
	writeJSON(w, http.StatusOK, api.ProcListResponse{
		Processes:   snap.Processes,
		Count:       snap.Count,
		CollectedAt: snap.CollectedAt,
		DurationMS:  snap.Duration,
	})
}

// handleProcsStream streams the process table over SSE.
//
// The collector keeps running on its own schedule; the stream only reads
// the cache, so a slow client cannot slow the collection down.
func (s *Server) handleProcsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errs.New(errs.KindInternal, "不支持流式响应"))
		return
	}

	interval := 2000
	switch r.URL.Query().Get("interval") {
	case "1000":
		interval = 1000
	case "5000":
		interval = 5000
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			snap := s.procs.Snapshot(r.Context())
			overview := s.sysinfo.Collect(r.Context())
			payload := api.ProcStreamResponse{
				Processes: snap.Processes,
				Count:     snap.Count,
				Overview:  overview,
				At:        time.Now().Format(time.RFC3339),
			}
			b, err := json.Marshal(normalize(payload))
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// handleProcDetail returns the extended information of one process.
func (s *Server) handleProcDetail(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 pid"))
		return
	}
	detail, err := s.procs.Detail(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ProcDetailResponse{Process: detail})
}

// handleProcSignal sends a signal to a process.
func (s *Server) handleProcSignal(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 pid"))
		return
	}
	var body api.ProcSignalRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sig := strings.ToUpper(strings.TrimSpace(body.Sig))
	if sig == "" {
		sig = "TERM"
	}
	if !procs.ValidSignal(sig) {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "未知信号 %q", body.Sig))
		return
	}

	// Protected processes need an explicit confirmation flag.
	if !body.Confirmed {
		if p, ok := procs.Find(s.procs.Snapshot(r.Context()), pid); ok && p.Protected {
			writeError(w, http.StatusConflict, errs.New(errs.KindPermission,
				"「%s」是受系统保护进程，终止它可能导致系统不稳定，请确认后再试", p.Name))
			return
		}
	}

	if err := procs.Signal(r.Context(), pid, sig); err != nil {
		s.audit.Log(audit.Entry{Action: audit.ActionProcSignal, Target: strconv.Itoa(pid),
			Detail: sig + " 失败: " + err.Error(), Result: audit.ResultFailed})
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.audit.Log(audit.Entry{Action: audit.ActionProcSignal, Target: strconv.Itoa(pid),
		Detail: "已发送 " + sig, Result: audit.ResultOK})
	writeJSON(w, http.StatusOK, api.ProcSignalResponse{OK: true, PID: pid, Sig: sig})
}

// handleProcRenice changes the priority of a process.
func (s *Server) handleProcRenice(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 pid"))
		return
	}
	var body api.ProcReniceRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := procs.Renice(r.Context(), pid, body.Nice); err != nil {
		s.audit.Log(audit.Entry{Action: audit.ActionProcRenice, Target: strconv.Itoa(pid),
			Detail: fmt.Sprintf("nice=%d 失败: %s", body.Nice, err.Error()), Result: audit.ResultFailed})
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.audit.Log(audit.Entry{Action: audit.ActionProcRenice, Target: strconv.Itoa(pid),
		Detail: fmt.Sprintf("nice=%d", body.Nice), Result: audit.ResultOK})
	writeJSON(w, http.StatusOK, api.ProcReniceResponse{OK: true, PID: pid, Nice: body.Nice})
}

// handleProcSample captures a thread stack sample.
func (s *Server) handleProcSample(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 pid"))
		return
	}
	out, err := procs.Sample(r.Context(), pid)
	if err != nil {
		writeJSON(w, http.StatusOK, api.ProcSampleResponse{Output: out, Error: errs.As(err).Message})
		return
	}
	writeJSON(w, http.StatusOK, api.ProcSampleResponse{Output: out})
}

// handleProcFiles lists the open files of a process.
func (s *Server) handleProcFiles(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "非法的 pid"))
		return
	}
	files, err := procs.OpenFiles(r.Context(), pid)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ProcFilesResponse{Files: files})
}

// handleProcAnalysisStatus returns the state of the process explanation.
func (s *Server) handleProcAnalysisStatus(w http.ResponseWriter, r *http.Request) {
	st := s.procs.Status()
	writeJSON(w, http.StatusOK, api.ProcAnalyzeResponse{
		Available: st.Available,
		Analyzing: st.Analyzing,
		Count:     st.Count,
		At:        st.At,
		Error:     st.Error,
	})
}

// handleProcAnalyze starts an explanation of the running processes.
//
// The model is asked what the programs are; the answer shows up in the
// process list once it is in.
func (s *Server) handleProcAnalyze(w http.ResponseWriter, r *http.Request) {
	if err := s.procs.Analyze(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st := s.procs.Status()
	writeJSON(w, http.StatusOK, api.ProcAnalyzeResponse{
		Available: st.Available,
		Analyzing: st.Analyzing,
		Count:     st.Count,
		At:        st.At,
		Error:     st.Error,
	})
}

// handleOverview returns the system summary.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	o := s.sysinfo.Collect(r.Context())
	// The counts come from the caches, never from a fresh collection: the
	// dashboard refreshes every few seconds and must not make the machine
	// walk the whole process table each time.
	if snap := s.procs.Peek(); snap != nil {
		o.ProcCount = snap.Count
	}
	if snap := s.ports.Peek(); snap != nil {
		o.PortCount = snap.Stats.Listen
	}
	writeJSON(w, http.StatusOK, o)
}

// handleAlertRules returns the configured alert thresholds.
func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.AlertRulesResponse{Items: s.alerter.Rules()})
}

// handleAlertRulesUpdate replaces the alert thresholds.
func (s *Server) handleAlertRulesUpdate(w http.ResponseWriter, r *http.Request) {
	var body api.AlertRulesRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.alerter.SetRules(body.Items)
	writeJSON(w, http.StatusOK, api.AlertRulesResponse{Items: s.alerter.Rules()})
}

// ensure sysinfo stays referenced.
var _ = sysinfo.Overview{}
