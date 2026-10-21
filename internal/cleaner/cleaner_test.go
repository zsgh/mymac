package cleaner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestFilterProtected makes sure the mymac config directory is never offered
// for deletion.
func TestFilterProtected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := filepath.Join(home, ".config", "mymac")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	items := []Item{
		{Path: filepath.Join(cfg, "env.json")},
		{Path: filepath.Join(home, "Library", "Caches", "something")},
		{Path: "/System/Library/Caches"},
	}
	out := filterProtected(items)
	for _, it := range out {
		if it.Path == filepath.Join(cfg, "env.json") {
			t.Error("the mymac config directory must be filtered out")
		}
	}
	if len(out) != 2 {
		t.Errorf("kept %d items, want 2", len(out))
	}
}

// TestScanInTempHome runs the scan against a fake home.
func TestScanInTempHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	caches := filepath.Join(home, "Library", "Caches")
	if err := os.MkdirAll(caches, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	if err := os.WriteFile(filepath.Join(caches, "thing.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner()
	res := s.Scan(context.Background(), true)
	if res == nil {
		t.Fatal("no result")
	}
	found := false
	for _, it := range res.Items {
		if it.Path == filepath.Join(caches, "thing.bin") {
			found = true
			if it.Size == 0 {
				t.Error("the size should not be zero")
			}
			if !it.Selected {
				t.Error("a cache should be selected by default")
			}
		}
	}
	if !found {
		t.Errorf("the cache file was not listed: %+v", res.Items)
	}
}

// TestScanReportsUnreadableDir checks that a directory that cannot be read
// is reported instead of silently skipped.
func TestScanReportsUnreadableDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A directory that does not exist produces an entry with the reason.
	s := NewScanner()
	res := s.Scan(context.Background(), true)
	for _, it := range res.Items {
		if it.Path == filepath.Join(home, "Library", "Caches") {
			if it.Readable {
				t.Error("a missing directory must be reported as unreadable")
			}
			if it.Note == "" {
				t.Error("an unreadable directory must carry a reason")
			}
		}
	}
}

// TestFindLargeFiles checks the large file search.
func TestFindLargeFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, 200*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "small.bin")
	if err := os.WriteFile(small, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := FindLargeFiles(context.Background(), []string{dir}, 100*1024, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("found %d files, want 1: %+v", len(files), files)
	}
	if files[0].Path != big {
		t.Errorf("found %q, want %q", files[0].Path, big)
	}
}

// TestFindDuplicates checks the three step duplicate detection.
func TestFindDuplicates(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("the very same content in two files")
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := []byte("completely different content here!!")
	if err := os.WriteFile(filepath.Join(dir, "d.bin"), other, 0o644); err != nil {
		t.Fatal(err)
	}

	groups, err := FindDuplicates(context.Background(), []string{dir}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("found %d groups, want 1: %+v", len(groups), groups)
	}
	if len(groups[0].Paths) != 3 {
		t.Errorf("group has %d paths, want 3: %+v", len(groups[0].Paths), groups[0].Paths)
	}
	if groups[0].Hash == "" {
		t.Error("the group should carry a hash")
	}
}

// TestPartialHashCoversHeadAndTail documents what the cheap filter does.
//
// The partial hash only covers the first and the last 4 KiB, so a difference
// in the middle is invisible to it. That is exactly why the full hash runs
// afterwards.
func TestPartialHashCoversHeadAndTail(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, offset int, content string) string {
		b := make([]byte, 16384)
		copy(b[offset:], content)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A difference in the head.
	headA := mk("headA", 10, "AAAA")
	headB := mk("headB", 10, "BBBB")
	ha, err := partialHash(headA)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := partialHash(headB)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Error("a difference in the head must be noticed by the partial hash")
	}

	// A difference in the middle is only caught by the full hash.
	midA := mk("midA", 8000, "AAAA")
	midB := mk("midB", 8000, "BBBB")
	ma, err := partialHash(midA)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := partialHash(midB)
	if err != nil {
		t.Fatal(err)
	}
	if ma != mb {
		t.Error("the partial hash does not cover the middle, so it must match")
	}
	fa, err := fullHash(midA)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := fullHash(midB)
	if err != nil {
		t.Fatal(err)
	}
	if fa == fb {
		t.Error("the full hash must notice a difference in the middle")
	}
}

// TestDevCachesAreKnown checks that the developer cache list covers the
// common tools.
func TestDevCachesAreKnown(t *testing.T) {
	home := t.TempDir()
	names := map[string]bool{}
	for _, dc := range devCaches(home) {
		names[dc.name] = true
	}
	for _, want := range []string{"Xcode DerivedData", "Go 编译缓存", "npm 缓存", "Homebrew 缓存", "Gradle 缓存", "Maven 仓库", "Docker 虚拟磁盘"} {
		if !names[want] {
			t.Errorf("the developer cache list is missing %q", want)
		}
	}
}
