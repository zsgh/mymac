package procs

import (
	"runtime"
	"testing"
	"time"
)

// fakeNotifier records the notifications.
type fakeNotifier struct {
	notes []string
}

func (f *fakeNotifier) Notify(title, message string) error {
	f.notes = append(f.notes, title+": "+message)
	return nil
}

// TestAlerterNeedsSustainedCondition makes sure a short spike does not fire.
func TestAlerterNeedsSustainedCondition(t *testing.T) {
	n := &fakeNotifier{}
	// The threshold is the load of the machine, so it is scaled to the
	// number of cores.
	cores := float64(runtime.NumCPU())
	a := NewAlerter(n, []AlertRule{
		{ID: "cpu", Label: "CPU", Metric: "cpu", Threshold: 80, Duration: 30 * time.Second, Enabled: true},
	})
	// A load of 100 percent: every core fully busy.
	snap := &Collection{Processes: []Process{
		{PID: 1, CPU: cores * 50}, {PID: 2, CPU: cores * 50},
	}}
	base := time.Now()

	// The first check only starts the timer.
	a.Check(snap, base)
	if len(n.notes) != 0 {
		t.Fatalf("fired too early: %v", n.notes)
	}
	// Not yet long enough.
	a.Check(snap, base.Add(10*time.Second))
	if len(n.notes) != 0 {
		t.Fatalf("fired too early: %v", n.notes)
	}
	// Long enough now.
	a.Check(snap, base.Add(31*time.Second))
	if len(n.notes) != 1 {
		t.Fatalf("did not fire: %v", n.notes)
	}
	// It must not fire again for the same condition.
	a.Check(snap, base.Add(60*time.Second))
	if len(n.notes) != 1 {
		t.Fatalf("fired twice: %v", n.notes)
	}
	// Once the condition is gone the rule resets.
	a.Check(&Collection{Processes: []Process{{PID: 1, CPU: 1}}}, base.Add(70*time.Second))
	a.Check(snap, base.Add(80*time.Second))
	if len(n.notes) != 1 {
		t.Fatalf("the rule did not reset: %v", n.notes)
	}
}

// TestAlerterIgnoresDisabledRules checks the enabled flag.
func TestAlerterIgnoresDisabledRules(t *testing.T) {
	n := &fakeNotifier{}
	a := NewAlerter(n, []AlertRule{
		{ID: "cpu", Label: "CPU", Metric: "cpu", Threshold: 1, Duration: 0, Enabled: false},
	})
	base := time.Now()
	a.Check(&Collection{Processes: []Process{{PID: 1, CPU: 90}}}, base)
	a.Check(&Collection{Processes: []Process{{PID: 1, CPU: 90}}}, base.Add(time.Minute))
	if len(n.notes) != 0 {
		t.Fatalf("a disabled rule fired: %v", n.notes)
	}
}

// TestMetricValue checks the metrics.
//
// The CPU metric is the load of the machine: the sum of the process load
// divided by the number of cores. Without that division the sum would reach
// several hundred percent on a multi core machine and every threshold
// would trip all the time.
func TestMetricValue(t *testing.T) {
	cores := float64(runtime.NumCPU())
	snap := &Collection{Processes: []Process{
		{PID: 1, CPU: cores * 30, Mem: 100 * 1024 * 1024},
		{PID: 2, CPU: cores * 20, Mem: 200 * 1024 * 1024},
	}}
	v, ok := metricValue("cpu", snap)
	if !ok {
		t.Fatal("the cpu metric must report a value")
	}
	if v < 49.9 || v > 50.1 {
		t.Errorf("cpu = %v, want 50 (the load of the machine)", v)
	}
	if v, ok := metricValue("memory", snap); !ok || v != 300 {
		t.Errorf("memory = %v %v", v, ok)
	}
	if _, ok := metricValue("unknown", snap); ok {
		t.Error("an unknown metric must not report a value")
	}
	if _, ok := metricValue("cpu", &Collection{}); ok {
		t.Error("an empty table must not report a value")
	}
}

// TestAppleStringEscaping checks the notification escaping.
func TestAppleStringEscaping(t *testing.T) {
	got := appleString(`say "hi"`)
	if got != `"say \"hi\""` {
		t.Errorf("appleString = %q", got)
	}
	if got := appleString("line1\nline2"); got != `"line1 line2"` {
		t.Errorf("newline = %q", got)
	}
}
