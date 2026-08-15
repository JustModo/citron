package lang

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

var ErrUnknownLanguage = errors.New("unknown language")

type Registry struct {
	byID   map[judge.LanguageID]*Language
	byName map[string]*Language
	order  []*Language
}

// LoadRegistry reads a languages.toml file. Hooks supply the Go implementations for
// manifests that name one; pass hooks.All() unless a test needs something narrower.
func LoadRegistry(path string, hooks Hooks) (*Registry, error) {
	manifests, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	return newRegistry(manifests, hooks)
}

func newRegistry(manifests []Manifest, hooks Hooks) (*Registry, error) {
	r := &Registry{
		byID:   make(map[judge.LanguageID]*Language, len(manifests)),
		byName: make(map[string]*Language, len(manifests)),
	}
	for _, m := range manifests {
		if err := m.validate(hooks); err != nil {
			return nil, err
		}
		id := judge.LanguageID(m.ID)
		if _, dup := r.byID[id]; dup {
			return nil, fmt.Errorf("%w: duplicate id %d", ErrInvalidManifest, m.ID)
		}
		if _, dup := r.byName[m.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate name %q", ErrInvalidManifest, m.Name)
		}
		compile, err := compileTemplates(m.Name, m.Compile)
		if err != nil {
			return nil, err
		}
		run, err := compileTemplates(m.Name, m.Run)
		if err != nil {
			return nil, err
		}
		l := &Language{manifest: m, hook: hooks[m.Hook], compile: compile, run: run}
		r.byID[id] = l
		r.byName[m.Name] = l
		r.order = append(r.order, l)
	}
	sort.Slice(r.order, func(i, j int) bool { return r.order[i].manifest.ID < r.order[j].manifest.ID })
	return r, nil
}

func (r *Registry) ByID(id judge.LanguageID) (*Language, error) {
	if l, ok := r.byID[id]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("%w: id %d", ErrUnknownLanguage, id)
}

func (r *Registry) ByName(name string) (*Language, error) {
	if l, ok := r.byName[strings.ToLower(name)]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, name)
}

func (r *Registry) All() []*Language { return r.order }

// Toolchain is what a language's probe reported.
type Toolchain struct {
	Language  string
	Available bool
	Version   string
	Err       error
}

// Probe runs each language's version command. Accepting submissions for a language
// whose compiler is missing produces confusing failures at execution time, so the
// composition root calls this at startup and refuses to start when required.
func (r *Registry) Probe(ctx context.Context) []Toolchain {
	out := make([]Toolchain, 0, len(r.order))
	for _, l := range r.order {
		t := Toolchain{Language: l.Name()}
		argv := l.ProbeCommand()
		if len(argv) == 0 {
			t.Available = true
			t.Version = "unknown"
			out = append(out, t)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// Version output goes to stderr for javac and to stdout for gcc.
		raw, err := exec.CommandContext(cctx, argv[0], argv[1:]...).CombinedOutput()
		cancel()
		if err != nil {
			t.Err = err
		} else {
			t.Available = true
			t.Version = firstLine(raw)
		}
		out = append(out, t)
	}
	return out
}

func firstLine(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
