package run

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/JustModo/citron/internal/judge"
)

// writeArtifact returns a build func that leaves a file of size bytes in the entry.
func writeArtifact(size int) func(string) (judge.CompileResult, error) {
	return func(dir string) (judge.CompileResult, error) {
		err := os.WriteFile(filepath.Join(dir, "main"), bytes.Repeat([]byte{1}, size), 0o755)
		return judge.CompileResult{Success: true}, err
	}
}

func entryCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// The cache shares its filesystem with workspaces, so its bytes must stay bounded
// however many distinct sources arrive.
func TestCacheEvictsByBytes(t *testing.T) {
	root := t.TempDir()
	c, err := NewCompileCache(root, 100, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		e, err := c.Build(key, writeArtifact(1<<20))
		if err != nil {
			t.Fatal(err)
		}
		c.Release(e)
	}
	if got := dirSize(root); got > 4<<20 {
		t.Errorf("cache holds %d bytes, budget is %d", got, 4<<20)
	}
}

// An entry a submission is still copying from must survive eviction.
func TestCacheKeepsPinnedEntries(t *testing.T) {
	root := t.TempDir()
	c, err := NewCompileCache(root, 1, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := c.Build("pinned", writeArtifact(10))
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.Build("other", writeArtifact(10))
	if err != nil {
		t.Fatal(err)
	}
	c.Release(other)
	if _, err := os.Stat(pinned.Dir); err != nil {
		t.Fatalf("pinned entry was evicted: %v", err)
	}
	c.Release(pinned)
	next, _ := c.Build("next", writeArtifact(10))
	c.Release(next)
	if _, err := os.Stat(pinned.Dir); !os.IsNotExist(err) {
		t.Error("released entry was not evicted once over the limit")
	}
	if n := entryCount(t, root); n != 1 {
		t.Errorf("%d entries, limit is 1", n)
	}
}

func TestOversizedCompileOutputFails(t *testing.T) {
	c, err := NewCompileCache(t.TempDir(), 10, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	e, err := c.Build("big", writeArtifact(2<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release(e)
	if e.Result.Success || !bytes.Contains(e.Result.Output, []byte("exceeds")) {
		t.Errorf("result = %+v, want a failed compile", e.Result)
	}
	if size := dirSize(e.Dir); size > 1<<10 {
		t.Errorf("oversized artifacts kept: %d bytes", size)
	}
}
