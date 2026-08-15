// Package workspace hands out disposable directories.
//
// The security boundary this serves: infrastructure is reusable, untrusted mutable
// state is not. Compiled artifacts are shared between testcases; the directory a
// testcase can write to is created fresh and destroyed afterwards, so nothing one
// testcase leaves behind is visible to the next.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Manager struct {
	root string
}

func NewManager(root string) (*Manager, error) {
	if root == "" {
		return nil, fmt.Errorf("workspace: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return &Manager{root: abs}, nil
}

func (m *Manager) Root() string { return m.root }

// Workspace is a directory that is removed when Close is called. Close is safe to
// call more than once, so callers can defer it and still close early.
type Workspace struct {
	Dir    string
	closed bool
}

// New creates a workspace. The prefix only aids debugging; uniqueness comes from
// MkdirTemp, so two executions never collide however they are named.
func (m *Manager) New(prefix string) (*Workspace, error) {
	dir, err := os.MkdirTemp(m.root, sanitize(prefix)+"-")
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	// The sandbox runs as a different user and must be able to write here.
	if err := os.Chmod(dir, 0o777); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("workspace: %w", err)
	}
	return &Workspace{Dir: dir}, nil
}

func (w *Workspace) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	return os.RemoveAll(w.Dir)
}

// Write puts a file into the workspace.
func (w *Workspace) Write(name string, data []byte, mode os.FileMode) error {
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("workspace: %q must be a bare filename", name)
	}
	return os.WriteFile(filepath.Join(w.Dir, name), data, mode)
}

func (w *Workspace) Path(name string) string { return filepath.Join(w.Dir, name) }

// Sweep removes everything under the root. It runs at startup to clear workspaces
// orphaned by a crash, and at shutdown after the last execution finishes.
func (m *Manager) Sweep() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(m.root, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("workspace: sweeping %s: %w", m.root, errs[0])
	}
	return nil
}

// sanitize keeps a caller-supplied prefix from escaping the root.
func sanitize(s string) string {
	if s == "" {
		return "exec"
	}
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, s)
	if out == "" {
		return "exec"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
