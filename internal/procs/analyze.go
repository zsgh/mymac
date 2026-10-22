package procs

import (
	"context"
	"sort"
	"strings"
	"time"

	"mymac/internal/ai"
	"mymac/internal/errs"
)

// AnalysisStatus tells the interface what the model is doing.
type AnalysisStatus struct {
	// Available reports whether a model can be asked at all.
	Available bool `json:"available"`
	// Analyzing is true while an explanation is in flight.
	Analyzing bool `json:"analyzing"`
	// Count is the number of programs that have an explanation.
	Count int `json:"count"`
	// At is when the last explanation finished.
	At time.Time `json:"at,omitempty"`
	// Error explains why the last run failed, if it did.
	Error string `json:"error,omitempty"`
}

// Status returns the state of the explanation.
func (c *Collector) Status() AnalysisStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return AnalysisStatus{
		Available: c.ai != nil,
		Analyzing: c.analyzing,
		Count:     len(c.notes),
		At:        c.analyzedAt,
		Error:     c.analyzeErr,
	}
}

// Analyze starts an explanation of the running programs in the background.
//
// Asking a model takes a while, so the request that starts it returns at
// once and the notes show up in the list once they are in.
func (c *Collector) Analyze(ctx context.Context) error {
	c.mu.RLock()
	provider := c.ai
	running := c.analyzing
	c.mu.RUnlock()
	if provider == nil {
		return errs.New(errs.KindUnsupported, "还没有配置大模型")
	}
	if running {
		return nil
	}
	client := provider()
	if client == nil || !client.Configured() {
		return errs.New(errs.KindInvalid, "还没有配置大模型")
	}

	// The processes of the moment are what the question is about.
	coll := c.Collect(ctx)
	subjects := processSubjects(coll.Processes)

	c.mu.Lock()
	c.analyzing = true
	c.analyzeErr = ""
	c.mu.Unlock()

	go func() {
		// The request that started the analysis is over long before the
		// model is; the work must not die with it.
		notes, err := client.AnalyzeProcesses(context.WithoutCancel(ctx), subjects)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.analyzing = false
		c.analyzedAt = time.Now()
		if err != nil {
			c.analyzeErr = err.Error()
			return
		}
		c.notes = make(map[string]ai.ProcessNote, len(notes))
		for _, n := range notes {
			c.notes[n.Name] = n
		}
	}()
	return nil
}

// processSubjects groups the processes by the name of the executable.
//
// The user does not know what "python3.11" is, but the paths and the
// command line say a lot more than the name does.
func processSubjects(list []Process) []ai.ProcessSubject {
	type group struct {
		count int
		paths []string
		users []string
		args  []string
	}
	groups := map[string]*group{}
	for _, p := range list {
		if p.Name == "" {
			continue
		}
		g, ok := groups[p.Name]
		if !ok {
			g = &group{}
			groups[p.Name] = g
		}
		g.count++
		if p.Path != "" && !contains(g.paths, p.Path) {
			g.paths = append(g.paths, p.Path)
		}
		if p.User != "" && !contains(g.users, p.User) {
			g.users = append(g.users, p.User)
		}
		if len(p.Args) > 0 && len(g.args) < 3 {
			arg := strings.TrimSpace(strings.Join(p.Args, " "))
			if arg != "" && !contains(g.args, arg) {
				g.args = append(g.args, arg)
			}
		}
	}
	out := make([]ai.ProcessSubject, 0, len(groups))
	for name, g := range groups {
		out = append(out, ai.ProcessSubject{
			Name:  name,
			Count: g.count,
			Paths: g.paths,
			Users: g.users,
			Args:  g.args,
		})
	}
	// The busiest programs come first: they are the ones worth looking at.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// contains reports whether a list holds a value.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// applyNotes copies the explanations of the model onto the processes.
func (c *Collector) applyNotes(list []Process) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.notes) == 0 {
		return
	}
	for i := range list {
		n, ok := c.notes[list[i].Name]
		if !ok {
			continue
		}
		list[i].Note = n.Summary
		list[i].NotePurpose = n.Purpose
		list[i].NoteRisk = n.Risk
	}
}
