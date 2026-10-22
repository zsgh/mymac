package ai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mymac/internal/config"
)

// Levels of a verdict.
//
// The words behind the levels differ per page: a startup item is "risky",
// an application is "worth removing". The page says what its levels mean;
// the model only has to pick one.
const (
	LevelHigh   = "high"
	LevelMedium = "medium"
	LevelLow    = "low"
)

// Item is one thing to judge, as the model sees it.
//
// Every page builds its own list out of these fields, so the prompt of a
// page only has to explain what it is looking at.
type Item struct {
	// Key identifies the item. The verdicts are stored by key, so the
	// cache knows which item a verdict belongs to.
	Key string `json:"key"`
	// Name is what the item is called.
	Name string `json:"name"`
	// Kind is the kind of the item ("启动项", "应用", …).
	Kind string `json:"kind,omitempty"`
	// Path is where the item lives.
	Path string `json:"path,omitempty"`
	// Size is the size in bytes, if the item has one.
	Size int64 `json:"size,omitempty"`
	// State is the state of the item, if it has one.
	State string `json:"state,omitempty"`
	// Extra holds the fields that only this page has.
	Extra map[string]string `json:"extra,omitempty"`
}

// Verdict is what the model said about one item.
type Verdict struct {
	// Key is the item the verdict belongs to.
	Key string `json:"key"`
	// Level is high, medium or low.
	Level string `json:"level"`
	// Label is a short word for the verdict, in the words of the page
	// ("建议清理", "高风险" …).
	Label string `json:"label"`
	// Reason explains the verdict in one sentence.
	Reason string `json:"reason"`
	// Advice says what to do about it.
	Advice string `json:"advice,omitempty"`
}

// Judge asks the model to judge the items.
//
// The question holds the items and the criteria; the answer is one verdict
// per item.
func (c *Client) Judge(ctx context.Context, system, question string, items []Item) ([]Verdict, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out, err := c.Ask(ctx, system, question)
	if err != nil {
		return nil, err
	}
	return ParseVerdicts(out)
}

// ItemList renders the items for the question.
//
// The identifier comes first: the model is asked to copy it back, and a
// model copies what it sees first.
func ItemList(items []Item) string {
	var b strings.Builder
	for _, it := range items {
		parts := []string{it.Key}
		if it.Name != "" && it.Name != it.Key {
			parts = append(parts, it.Name)
		}
		if it.Kind != "" {
			parts = append(parts, it.Kind)
		}
		if it.Path != "" {
			parts = append(parts, it.Path)
		}
		if it.Size > 0 {
			parts = append(parts, humanSize(it.Size))
		}
		if it.State != "" {
			parts = append(parts, it.State)
		}
		for _, k := range sortedKeys(it.Extra) {
			parts = append(parts, k+"="+it.Extra[k])
		}
		b.WriteString(strings.Join(parts, " | "))
		b.WriteString("\n")
	}
	return b.String()
}

// sortedKeys returns the keys of a map in a fixed order.
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// humanSize renders a size the way a person would say it.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return itoa(n/div) + " " + []string{"KB", "MB", "GB", "TB"}[exp]
}

// itoa renders an integer.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ParseVerdicts reads the answer of the model.
//
// The model is asked for a bare JSON array, but a model is a model: the
// answer may come wrapped in a code block or with a sentence around it.
func ParseVerdicts(out string) ([]Verdict, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, errNotAnswered
	}
	if i := strings.Index(text, "["); i >= 0 {
		if j := strings.LastIndex(text, "]"); j > i {
			text = text[i : j+1]
		}
	}
	var list []Verdict
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		return nil, err
	}
	res := make([]Verdict, 0, len(list))
	for _, v := range list {
		v.Key = strings.TrimSpace(v.Key)
		v.Level = strings.ToLower(strings.TrimSpace(v.Level))
		v.Label = strings.TrimSpace(v.Label)
		v.Reason = strings.TrimSpace(v.Reason)
		v.Advice = strings.TrimSpace(v.Advice)
		if v.Key == "" {
			continue
		}
		switch v.Level {
		case LevelHigh, LevelMedium, LevelLow:
		default:
			// An unknown level would be a lie in the table.
			v.Level = LevelMedium
		}
		if v.Label == "" {
			v.Label = defaultLabel(v.Level)
		}
		res = append(res, v)
	}
	if len(res) == 0 {
		return nil, errNotAnswered
	}
	return res, nil
}

// defaultLabel names a level when the model did not.
func defaultLabel(level string) string {
	switch level {
	case LevelHigh:
		return "要注意"
	case LevelLow:
		return "没问题"
	default:
		return "留意"
	}
}

// errNotAnswered reports that the model said nothing useful.
var errNotAnswered = errors.New("大模型没有给出任何评估")

// ---------------------------------------------------------------- the cache

// Entry is one analysis and its verdicts.
type Entry struct {
	// At is when the analysis finished.
	At time.Time `json:"at"`
	// Model is the model that answered.
	Model string `json:"model,omitempty"`
	// Items are the verdicts, by the key of the item.
	Items []Verdict `json:"items"`
}

// Cache keeps the verdicts of the analyses on disk.
//
// The verdicts are expensive to get and hardly ever change: a program that
// was risky yesterday is still risky today. Keeping them means the model is
// only asked about what is new.
type Cache struct {
	mu   sync.Mutex
	path string
}

// CacheFileName is the name of the cache file.
const CacheFileName = "ai-cache.json"

// NewCache returns a cache that lives in the mymac config directory.
func NewCache() *Cache {
	return &Cache{path: filepath.Join(config.Root(), CacheFileName)}
}

// NewCacheAt returns a cache that lives in the given file.
func NewCacheAt(path string) *Cache { return &Cache{path: path} }

// file is the whole cache file.
type file struct {
	Version  int              `json:"version"`
	Analyses map[string]Entry `json:"analyses"`
}

// Get returns the entry of an analysis.
func (c *Cache) Get(name string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.read()
	e, ok := f.Analyses[name]
	return e, ok
}

// All returns every entry.
func (c *Cache) All() map[string]Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read().Analyses
}

// Put writes the entry of an analysis.
func (c *Cache) Put(name string, e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.read()
	if f.Analyses == nil {
		f.Analyses = map[string]Entry{}
	}
	f.Analyses[name] = e
	return c.write(f)
}

// Merge adds verdicts to the entry of an analysis.
//
// The verdicts that are already there stay: the cache is the point.
func (c *Cache) Merge(name string, verdicts []Verdict, model string) (Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.read()
	if f.Analyses == nil {
		f.Analyses = map[string]Entry{}
	}
	cur := f.Analyses[name]
	byKey := map[string]Verdict{}
	for _, v := range cur.Items {
		byKey[v.Key] = v
	}
	for _, v := range verdicts {
		byKey[v.Key] = v
	}
	merged := make([]Verdict, 0, len(byKey))
	for _, v := range byKey {
		merged = append(merged, v)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Key < merged[j].Key })
	cur.Items = merged
	cur.At = time.Now()
	if model != "" {
		cur.Model = model
	}
	f.Analyses[name] = cur
	if err := c.write(f); err != nil {
		return cur, err
	}
	return cur, nil
}

// VerdictOf returns the verdict of one item.
func (e Entry) VerdictOf(key string) (Verdict, bool) {
	for _, v := range e.Items {
		if v.Key == key {
			return v, true
		}
	}
	return Verdict{}, false
}

// read loads the cache file. The caller holds the lock.
func (c *Cache) read() file {
	out := file{Version: 1, Analyses: map[string]Entry{}}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return out
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		// A damaged cache must not take the tool down: the analyses
		// simply start over.
		return out
	}
	if f.Analyses == nil {
		f.Analyses = map[string]Entry{}
	}
	return f
}

// write saves the cache file. The caller holds the lock.
func (c *Cache) write(f file) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	f.Version = 1
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(c.path, raw, 0o600)
}
