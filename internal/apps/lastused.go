package apps

import (
	"os/exec"
	"strings"
	"time"
)

// LastUsed returns the last time the application was opened.
//
// macOS keeps the date in the Spotlight metadata of the bundle. A bundle
// that was never opened has no date at all, and Spotlight does not know
// every bundle either.
func LastUsed(path string) (time.Time, bool) {
	cmd := exec.Command("/usr/bin/mdls", "-raw", "-name", "kMDItemLastUsedDate", path)
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, false
	}
	text := strings.TrimSpace(string(out))
	if text == "" || text == "(null)" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05 -0700", text)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DaysAgo counts the calendar days between a moment and now.
//
// Zero means "today". The days are counted by the calendar, not by 24
// hours: an application that was opened last night was opened "yesterday",
// not "0 days ago".
func DaysAgo(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	now := time.Now()
	tMid := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	nMid := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(nMid.Sub(tMid).Hours() / 24)
}
