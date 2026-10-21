package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinkCount checks the hard link count.
func TestLinkCount(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LinkCount(a); got != 1 {
		t.Errorf("a plain file has %d links, want 1", got)
	}
	if err := os.Link(a, b); err != nil {
		t.Skipf("hard links are not supported here: %v", err)
	}
	if got := LinkCount(a); got != 2 {
		t.Errorf("a hard linked file has %d links, want 2", got)
	}
	if got := LinkCount(b); got != 2 {
		t.Errorf("the other name has %d links, want 2", got)
	}
}

// TestSameInode checks the inode comparison.
func TestSameInode(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	c := filepath.Join(dir, "c")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if SameInode(a, c) {
		t.Error("two separate files must not be the same inode")
	}
	if err := os.Link(a, b); err != nil {
		t.Skipf("hard links are not supported here: %v", err)
	}
	if !SameInode(a, b) {
		t.Error("two names of one inode must be reported as the same file")
	}
}

// TestAllocatedSizeIsNotTheLogicalSize checks that the allocated size is
// used: a sparse file occupies far less than its length.
func TestAllocatedSizeIsNotTheLogicalSize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sparse")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(10 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	f.Close()

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 10*1024*1024 {
		t.Fatalf("the logical size is %d", info.Size())
	}
	allocated := AllocatedSize(info)
	if allocated >= uint64(info.Size()) {
		t.Errorf("the allocated size is %d, want less than %d", allocated, info.Size())
	}
}
