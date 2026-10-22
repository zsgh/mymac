package server

import (
	"time"

	"mymac/internal/ai"
	"mymac/internal/settings"
)

// aiProviderFor builds a provider for a settings store.
//
// The settings can change while mymac runs, so the client is built from the
// settings every time it is asked for. Without a model there is no client,
// and the AI features say so instead of pretending to work.
func aiProviderFor(store *settings.Store) ai.Provider {
	return func() *ai.Client {
		if store == nil {
			return nil
		}
		cur, err := store.Get()
		if err != nil || !cur.LLM.Configured() {
			return nil
		}
		return ai.New(ai.Config{
			BaseURL: cur.LLM.BaseURL,
			APIKey:  cur.LLM.APIKey,
			Model:   cur.LLM.Model,
			// The analysis runs in the background and a long list
			// takes a while on a thinking model.
			Timeout: 10 * time.Minute,
		})
	}
}

// aiClient returns the client for the model the user configured.
func (s *Server) aiClient() *ai.Client {
	return aiProviderFor(s.settings)()
}

// docsEnabled reports whether the interface documentation may be reached.
//
// The documentation is a door into the API: it lists every endpoint and can
// call them. A user who does not want that door can close it.
func (s *Server) docsEnabled() bool {
	if s.settings == nil {
		return true
	}
	cur, err := s.settings.Get()
	if err != nil {
		return true
	}
	return cur.Docs.Enabled
}

// settingsOf reads the settings for the interface.
func (s *Server) settingsOf() settings.Settings {
	if s.settings == nil {
		return settings.Settings{Docs: settings.Docs{Enabled: true}}
	}
	cur, err := s.settings.Get()
	if err != nil {
		return settings.Settings{Docs: settings.Docs{Enabled: true}}
	}
	return cur
}
