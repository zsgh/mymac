package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"mymac/internal/api"
	"mymac/internal/audit"
	"mymac/internal/errs"
	"mymac/internal/settings"
)

// handleSettingsGet returns the settings.
//
// The API key is not sent back: the interface only needs to know whether
// there is one.
func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cur := s.settingsOf()
	writeJSON(w, http.StatusOK, api.SettingsResponse{
		LLM: api.LLMSettings{
			BaseURL: cur.LLM.BaseURL,
			Model:   cur.LLM.Model,
			// Whether a key is there, not the key itself.
			HasKey: cur.LLM.APIKey != "",
		},
		Docs: api.DocsSettings{Enabled: cur.Docs.Enabled},
	})
}

// handleSettingsSet writes the settings.
//
// An empty key means "keep the one that is there": the interface never sees
// the key, so it cannot send it back.
func (s *Server) handleSettingsSet(w http.ResponseWriter, r *http.Request) {
	var body api.SettingsRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	baseURL := strings.TrimSpace(body.LLM.BaseURL)
	model := strings.TrimSpace(body.LLM.Model)
	if baseURL != "" && !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "接口地址要以 http:// 或 https:// 开头"))
		return
	}
	if baseURL != "" && model == "" {
		writeError(w, http.StatusBadRequest, errs.New(errs.KindInvalid, "填了接口地址就要填模型名"))
		return
	}
	key := strings.TrimSpace(body.LLM.APIKey)
	cur, err := s.settings.Update(func(v *settings.Settings) {
		if baseURL == "" && model == "" {
			// The model was cleared.
			v.LLM = settings.LLM{}
			return
		}
		v.LLM.BaseURL = baseURL
		v.LLM.Model = model
		if key != "" {
			v.LLM.APIKey = key
		}
		if body.Docs != nil {
			v.Docs.Enabled = body.Docs.Enabled
		}
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionPermGrant,
		Target: "settings",
		Detail: "更新了设置",
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, api.SettingsResponse{
		LLM: api.LLMSettings{
			BaseURL: cur.LLM.BaseURL,
			Model:   cur.LLM.Model,
			HasKey:  cur.LLM.APIKey != "",
		},
		Docs: api.DocsSettings{Enabled: cur.Docs.Enabled},
	})
}

// handleSettingsTest asks the model a small question.
//
// The point is to find out whether the address, the key and the model name
// are right before the user needs them.
func (s *Server) handleSettingsTest(w http.ResponseWriter, r *http.Request) {
	client := s.aiClient()
	if client == nil {
		writeJSON(w, http.StatusOK, api.SettingsTestResponse{
			OK:    false,
			Error: "还没有配置大模型",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	answer, err := client.Ask(ctx, "你是一个测试助手。", "只回答两个字：收到")
	if err != nil {
		writeJSON(w, http.StatusOK, api.SettingsTestResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, api.SettingsTestResponse{OK: true, Answer: strings.TrimSpace(answer)})
}

// handleDocsToggle turns the interface documentation on or off.
//
// The documentation is a door into the API: it lists every endpoint and can
// call them. Closing the door leaves the API itself alone.
func (s *Server) handleDocsToggle(w http.ResponseWriter, r *http.Request) {
	var body api.DocsToggleRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cur, err := s.settings.Update(func(v *settings.Settings) {
		v.Docs.Enabled = body.Enabled
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	state := "已禁用"
	if cur.Docs.Enabled {
		state = "已启用"
	}
	s.audit.Log(audit.Entry{
		Action: audit.ActionPermGrant,
		Target: "api/docs",
		Detail: state,
		Result: audit.ResultOK,
	})
	writeJSON(w, http.StatusOK, api.DocsSettings{Enabled: cur.Docs.Enabled})
}
