// Package settings holds the things the user can configure.
//
// The settings live in a JSON file next to the other files of mymac, so
// they can be looked at and edited by hand. The file is small and is read
// on every change: there is no cache to keep in sync.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"mymac/internal/config"
	"mymac/internal/errs"
)

// FileName is the name of the settings file.
const FileName = "settings.json"

// LLM describes the model the AI features talk to.
//
// Any service that speaks the OpenAI chat API works: OpenAI, DeepSeek,
// Moonshot, Ollama, LM Studio, one-api and the like.
type LLM struct {
	// BaseURL is the root of the API, for example
	// https://api.openai.com/v1.
	BaseURL string `json:"baseUrl"`
	// APIKey is the token. It is never sent anywhere but the endpoint
	// above.
	APIKey string `json:"apiKey"`
	// Model is the name of the model, for example gpt-4o-mini.
	Model string `json:"model"`
}

// Configured reports whether the model can be reached.
func (l LLM) Configured() bool {
	return l.BaseURL != "" && l.Model != ""
}

// Docs holds the settings of the interface documentation.
type Docs struct {
	// Enabled reports whether the documentation can be reached.
	//
	// The documentation is a door into the API: it shows every endpoint
	// and can call them. A user who does not want that door can close it.
	Enabled bool `json:"enabled"`
}

// Settings is the whole file.
type Settings struct {
	// LLM is the model configuration.
	LLM LLM `json:"llm"`
	// Docs is the documentation configuration.
	Docs Docs `json:"docs"`
}

// Store reads and writes the settings file.
type Store struct {
	mu sync.Mutex
}

// NewStore returns a settings store.
func NewStore() *Store { return &Store{} }

// Path returns the path of the settings file.
func Path() string {
	return filepath.Join(config.Root(), FileName)
}

// Get reads the settings.
//
// A missing file means the defaults: no model, no documentation.
func (s *Store) Get() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// read reads the settings. The caller holds the lock.
func (s *Store) read() (Settings, error) {
	out := Settings{Docs: Docs{Enabled: true}}
	raw, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// A broken file must not take the whole tool down: the defaults
		// are better than nothing.
		return Settings{Docs: Docs{Enabled: true}}, nil
	}
	return out, nil
}

// Set writes the settings.
func (s *Store) Set(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(v)
}

// write writes the settings. The caller holds the lock.
func (s *Store) write(v Settings) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		return err
	}
	return nil
}

// Update changes the settings and writes them back.
func (s *Store) Update(fn func(*Settings)) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.read()
	if err != nil {
		return cur, err
	}
	fn(&cur)
	if err := s.write(cur); err != nil {
		return cur, err
	}
	return cur, nil
}

// ErrNotConfigured reports that no model has been set up.
var ErrNotConfigured = errs.New(errs.KindInvalid, "还没有配置大模型")
