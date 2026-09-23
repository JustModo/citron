package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manager creates workspaces under a single root directory.
type Manager struct {
	root string
}

// NewManager creates root if needed and returns a Manager for it.
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

// Root returns the absolute root directory.
func (m *Manager) Root() string { return m.root }

// Workspace is a directory that is removed by Close.
type Workspace struct {
	Dir    string
	closed bool
}

// New creates a uniquely named workspace. The sanitized prefix only aids debugging.
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

// Close removes the workspace. It is safe to call more than once and on nil.
func (w *Workspace) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	return os.RemoveAll(w.Dir)
}

// Sweep removes everything under the root, including workspaces orphaned by a crash.
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
