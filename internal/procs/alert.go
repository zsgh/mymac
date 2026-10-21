package procs

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// AlertRule describes a threshold that triggers a notification.
type AlertRule struct {
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Metric    string        `json:"metric"` // cpu | memory
	Threshold float64       `json:"threshold"`
	Duration  time.Duration `json:"duration"`
	Enabled   bool          `json:"enabled"`
}

// Notifier delivers a notification to the user.
type Notifier interface {
	Notify(title, message string) error
}

// OSNotifier shows a macOS notification.
type OSNotifier struct{}

// Notify posts a notification through osascript.
//
// The message is truncated: a very long text makes the notification window
// grow, and osascript has a limit on the argument size.
func (OSNotifier) Notify(title, message string) error {
	if len(title) > 80 {
		title = title[:80]
	}
	if len(message) > 200 {
		message = message[:200] + "…"
	}
	script := fmt.Sprintf(`display notification %s with title %s`,
		appleString(message), appleString(title))
	return exec.Command("/usr/bin/osascript", "-e", script).Run()
}

// appleString renders a Go string as an AppleScript string literal.
func appleString(s string) string {
	out := ""
	for _, r := range s {
		switch r {
		case '"':
			out += `\"`
		case '\\':
			out += `\\`
		case '\n':
			out += " "
		default:
			out += string(r)
		}
	}
	return `"` + out + `"`
}

// DefaultRules are the thresholds mymac watches out of the box.
//
// The CPU threshold is the load of the whole machine, so 80 means "eight of
// ten cores are busy".
func DefaultRules() []AlertRule {
	return []AlertRule{
		{ID: "cpu", Label: "CPU 持续高占用", Metric: "cpu", Threshold: 80, Duration: 60 * time.Second, Enabled: true},
		{ID: "cpu90", Label: "CPU 极高占用", Metric: "cpu", Threshold: 90, Duration: 30 * time.Second, Enabled: false},
	}
}

// Alerter watches the process table and fires notifications.
type Alerter struct {
	mu       sync.Mutex
	rules    []AlertRule
	since    map[string]time.Time
	fired    map[string]bool
	notifier Notifier
}

// NewAlerter returns an alerter with the given rules.
func NewAlerter(notifier Notifier, rules []AlertRule) *Alerter {
	return &Alerter{
		rules:    rules,
		since:    map[string]time.Time{},
		fired:    map[string]bool{},
		notifier: notifier,
	}
}

// Rules returns the configured rules.
func (a *Alerter) Rules() []AlertRule {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AlertRule, len(a.rules))
	copy(out, a.rules)
	return out
}

// SetRules replaces the rule set.
func (a *Alerter) SetRules(rules []AlertRule) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rules = rules
	a.since = map[string]time.Time{}
	a.fired = map[string]bool{}
}

// Check evaluates the rules against a snapshot.
//
// A rule fires only when the condition has held for the whole duration, so
// a short spike does not produce a notification.
func (a *Alerter) Check(snap *Collection, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, rule := range a.rules {
		if !rule.Enabled {
			continue
		}
		value, ok := metricValue(rule.Metric, snap)
		if !ok {
			continue
		}
		if value <= rule.Threshold {
			delete(a.since, rule.ID)
			delete(a.fired, rule.ID)
			continue
		}
		start, seen := a.since[rule.ID]
		if !seen {
			a.since[rule.ID] = now
			continue
		}
		if a.fired[rule.ID] {
			continue
		}
		if now.Sub(start) < rule.Duration {
			continue
		}
		a.fired[rule.ID] = true
		if a.notifier == nil {
			continue
		}
		msg := fmt.Sprintf("%.0f%% 持续超过 %s", value, rule.Duration)
		_ = a.notifier.Notify("mymac: "+rule.Label, msg)
	}
}

// metricValue computes the value of a metric for the whole table.
//
// The CPU metric is the load of the machine, not the sum of the per process
// percentages: on a ten core machine the sum easily reaches several hundred
// percent, which would trip every threshold all the time.
func metricValue(metric string, snap *Collection) (float64, bool) {
	switch metric {
	case "cpu":
		if snap == nil || len(snap.Processes) == 0 {
			return 0, false
		}
		// The busy share of the machine: the sum of the process load
		// divided by the number of cores, capped at 100.
		var sum float64
		for _, p := range snap.Processes {
			sum += p.CPU
		}
		cores := float64(runtime.NumCPU())
		if cores < 1 {
			cores = 1
		}
		return sum / cores, true
	case "memory":
		if snap == nil || len(snap.Processes) == 0 {
			return 0, false
		}
		var total uint64
		for _, p := range snap.Processes {
			total += p.Mem
		}
		return float64(total) / (1024 * 1024), true
	}
	return 0, false
}

// Start runs the alert watcher in the background.
//
// The snapshot is peeked, not collected: the watcher must never make the
// machine busier than the condition it is watching for.
func (a *Alerter) Start(ctx context.Context, c *Collector, every time.Duration) {
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				a.Check(c.Peek(), now)
			}
		}
	}()
}
