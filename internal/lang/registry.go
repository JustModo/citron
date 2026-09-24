package lang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// ErrUnknownLanguage is returned when a lookup matches no language.
var ErrUnknownLanguage = errors.New("unknown language")

// Registry holds the loaded languages, indexed by id and name.
type Registry struct {
	byID   map[judge.LanguageID]*Language
	byName map[string]*Language
	order  []*Language
}

// ManifestFile is the name of the manifest inside a language pack directory.
const ManifestFile = "language.toml"

// pack is a parsed manifest and the absolute directory it was loaded from.
type pack struct {
	manifest Manifest
	dir      string
}

// LoadRegistry loads every language pack under dir, each a subdirectory holding a
// language.toml.
func LoadRegistry(dir string) (*Registry, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("languages: %w", err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*", ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("languages: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: no language packs in %s", ErrInvalidManifest, dir)
	}
	packs := make([]pack, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("languages: %w", err)
		}
		m, err := parseManifest(data)
		if err != nil {
			return nil, fmt.Errorf("languages: %s: %w", path, err)
		}
		abs, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("languages: %w", err)
		}
		packs = append(packs, pack{manifest: m, dir: abs})
	}
	return newRegistry(packs)
}

func newRegistry(packs []pack) (*Registry, error) {
	r := &Registry{
		byID:   make(map[judge.LanguageID]*Language, len(packs)),
		byName: make(map[string]*Language, len(packs)),
	}
	for _, p := range packs {
		m := p.manifest
		if err := m.validate(); err != nil {
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
		l := &Language{manifest: m, pack: p.dir, compile: compile, run: run}
		r.byID[id] = l
		r.byName[m.Name] = l
		r.order = append(r.order, l)
	}
	sort.Slice(r.order, func(i, j int) bool { return r.order[i].manifest.ID < r.order[j].manifest.ID })
	return r, nil
}

// ByID returns the language with the given id.
func (r *Registry) ByID(id judge.LanguageID) (*Language, error) {
	if l, ok := r.byID[id]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("%w: id %d", ErrUnknownLanguage, id)
}

// ByName returns the language with the given name, matched case-insensitively.
func (r *Registry) ByName(name string) (*Language, error) {
	if l, ok := r.byName[strings.ToLower(name)]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownLanguage, name)
}

// All returns every language ordered by id.
func (r *Registry) All() []*Language { return r.order }

// Toolchain is the result of probing one language's toolchain.
type Toolchain struct {
	Language  string
	Available bool
	Version   string
	Err       error
}

// Probe runs each language's version command. A language with no probe command is
// reported available with version "unknown".
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
