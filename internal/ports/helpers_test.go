package ports

import (
	"testing"
	"time"
)

// mustDuration parses a duration for the tests.
func mustDuration(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
