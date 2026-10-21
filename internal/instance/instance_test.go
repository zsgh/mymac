package instance

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestSecondAcquireFails makes sure a second instance is turned away.
func TestSecondAcquireFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mymac.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := Acquire(path); !errors.Is(err, ErrRunning) {
		t.Fatalf("second acquire = %v, want ErrRunning", err)
	}
}

// TestReleaseAllowsAnotherAcquire makes sure the lock frees up again.
func TestReleaseAllowsAnotherAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mymac.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	first.Release()

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second.Release()
}

// TestInfoRoundTrip covers the bookkeeping a second instance reads.
func TestInfoRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mymac.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()

	want := Info{PID: 4242, Port: 8888, Addr: "http://127.0.0.1:8888"}
	if err := lock.WriteInfo(want); err != nil {
		t.Fatalf("write info: %v", err)
	}

	got, err := ReadInfo(path)
	if err != nil {
		t.Fatalf("read info: %v", err)
	}
	if got != want {
		t.Errorf("info = %+v, want %+v", got, want)
	}
}
