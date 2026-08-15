package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "box"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWorkspaceIsRemovedOnClose(t *testing.T) {
	m := newManager(t)
	w, err := m.New("tc-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write("out.txt", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		t.Error("workspace survived Close; testcase state would leak to the next run")
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close must be idempotent, got %v", err)
	}
}

// Two testcases of the same submission must not share a directory.
func TestWorkspacesAreIsolated(t *testing.T) {
	m := newManager(t)
	a, err := m.New("tc")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := m.New("tc")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if a.Dir == b.Dir {
		t.Fatal("two workspaces share a directory")
	}
	if err := a.Write("secret", []byte("from A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Path("secret")); !os.IsNotExist(err) {
		t.Error("a file written by one testcase is visible to another")
	}
}

func TestConcurrentCreation(t *testing.T) {
	m := newManager(t)
	var wg sync.WaitGroup
	dirs := make([]string, 50)
	for i := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := m.New("tc")
			if err != nil {
				t.Error(err)
				return
			}
			dirs[i] = w.Dir
			defer w.Close()
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if seen[d] {
			t.Fatalf("duplicate workspace %s", d)
		}
		seen[d] = true
	}
}

func TestSweepClearsOrphans(t *testing.T) {
	m := newManager(t)
	for range 3 {
		if _, err := m.New("orphan"); err != nil { // deliberately never closed
			t.Fatal(err)
		}
	}
	if err := m.Sweep(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(m.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("sweep left %d entries behind", len(entries))
	}
}

func TestWriteRejectsPaths(t *testing.T) {
	m := newManager(t)
	w, err := m.New("tc")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, name := range []string{"../escape", "sub/file", `back\slash`} {
		if err := w.Write(name, []byte("x"), 0o644); err == nil {
			t.Errorf("Write(%q) should be rejected", name)
		}
	}
}

// A caller-supplied prefix must not steer the directory out of the root.
func TestPrefixCannotEscapeRoot(t *testing.T) {
	m := newManager(t)
	w, err := m.New("../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if !strings.HasPrefix(w.Dir, m.Root()) {
		t.Errorf("workspace %s escaped root %s", w.Dir, m.Root())
	}
}
