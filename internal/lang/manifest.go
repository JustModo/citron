package lang

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/JustModo/citron/internal/judge"
)

// Manifest is a pack's language.toml. Compile and Run are text/template argv whose
// fields are those of renderCtx.
type Manifest struct {
	ID     int    `toml:"id"`
	Name   string `toml:"name"`
	Label  string `toml:"label"`
	Source string `toml:"source"`
	Binary string `toml:"binary"`

	Compile []string `toml:"compile"`
	Run     []string `toml:"run"`
	Probe   []string `toml:"probe"`

	// Mounts are extra absolute paths the language needs read-only in the jail.
	Mounts []string `toml:"mounts"`
	// Env is extra KEY=VALUE environment for both compile and run.
	Env []string `toml:"env"`

	Limits        ManifestLimits `toml:"limits"`
	CompileLimits ManifestLimits `toml:"compile_limits"`
}

// ManifestLimits adjusts configured limits for one language, for
// runtimes whose fixed overhead would otherwise exhaust the baseline. Zero fields
// leave the base limit unchanged.
type ManifestLimits struct {
	MemoryExtraMB  int64   `toml:"memory_extra_mb"`
	MaxProcesses   int     `toml:"max_processes"`
	WallMultiplier float64 `toml:"wall_multiplier"`
	CPUMultiplier  float64 `toml:"cpu_multiplier"`
}

func scale(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * f)
}

// Apply returns l adjusted for this language. Extra memory raises the enforced
// ceiling, not the amount the submission is told it may use.
func (ml ManifestLimits) Apply(l judge.Limits) judge.Limits {
	out := l
	if ml.WallMultiplier > 0 {
		out.WallTime = scale(l.WallTime, ml.WallMultiplier)
	}
	if ml.CPUMultiplier > 0 {
		out.CPUTime = scale(l.CPUTime, ml.CPUMultiplier)
	}
	if ml.MemoryExtraMB > 0 {
		out.Memory = l.Memory + judge.MemoryBytes(ml.MemoryExtraMB<<20)
	}
	if ml.MaxProcesses > 0 {
		out.MaxProcesses = ml.MaxProcesses
	}
	return out
}

// renderCtx holds every value a manifest template may reference. None of it comes
// from submitted source.
type renderCtx struct {
	Source  string
	Binary  string
	Dir     string
	Pack    string
	StackKB int64
	HeapMB  int64
	MemMB   int64
}

// Language is a validated manifest with its argv templates parsed.
type Language struct {
	manifest Manifest
	pack     string
	compile  []*template.Template
	run      []*template.Template
}

// ID returns the language's numeric id.
func (l *Language) ID() judge.LanguageID { return judge.LanguageID(l.manifest.ID) }

// Name returns the language's unique lower-case name.
func (l *Language) Name() string { return l.manifest.Name }

// Label returns the language's display name.
func (l *Language) Label() string { return l.manifest.Label }

// ProbeCommand returns the argv that prints the toolchain version, or nil.
func (l *Language) ProbeCommand() []string { return l.manifest.Probe }

// Source returns the filename the submitted source is written to.
func (l *Language) Source() string { return l.manifest.Source }

// Mounts returns the extra read-only paths the language needs in the jail.
func (l *Language) Mounts() []string { return l.manifest.Mounts }

// Env returns the language's extra environment.
func (l *Language) Env() []string { return l.manifest.Env }

// Limits returns the execution limits base adjusted for this language.
func (l *Language) Limits(base judge.Limits) judge.Limits { return l.manifest.Limits.Apply(base) }

// CompileLimits returns the compile limits base adjusted for this language.
func (l *Language) CompileLimits(base judge.Limits) judge.Limits {
	return l.manifest.CompileLimits.Apply(base)
}

// CompileArgv renders the compile command. It returns nil when the language has no
// compile step.
func (l *Language) CompileArgv(c Context) ([]string, error) { return l.render(l.compile, c) }

// RunArgv renders the run command.
func (l *Language) RunArgv(c Context) ([]string, error) { return l.render(l.run, c) }

// Context supplies the per-execution values needed to render argv.
type Context struct {
	Dir     string
	Limits  judge.Limits
	BaseMem judge.MemoryBytes // memory before the language's extra headroom; sizes HeapMB
}

func (l *Language) render(tmpls []*template.Template, c Context) ([]string, error) {
	if len(tmpls) == 0 {
		return nil, nil
	}
	rc := renderCtx{
		Source:  l.manifest.Source,
		Binary:  l.manifest.Binary,
		Dir:     c.Dir,
		Pack:    l.pack,
		StackKB: int64(c.Limits.Stack) >> 10,
		HeapMB:  c.BaseMem.MB(),
		MemMB:   c.Limits.Memory.MB(),
	}
	if rc.HeapMB <= 0 {
		rc.HeapMB = rc.MemMB
	}
	argv := make([]string, len(tmpls))
	var buf bytes.Buffer
	for i, t := range tmpls {
		buf.Reset()
		if err := t.Execute(&buf, rc); err != nil {
			return nil, fmt.Errorf("lang: rendering argv: %w", err)
		}
		argv[i] = buf.String()
	}
	return argv, nil
}

func compileTemplates(name string, argv []string) ([]*template.Template, error) {
	out := make([]*template.Template, len(argv))
	for i, a := range argv {
		t, err := template.New(name).Option("missingkey=error").Parse(a)
		if err != nil {
			return nil, fmt.Errorf("lang %s: argv[%d] %q: %w", name, i, a, err)
		}
		out[i] = t
	}
	return out, nil
}

// ErrInvalidManifest is wrapped by every manifest validation error.
var ErrInvalidManifest = errors.New("invalid language manifest")

func (m Manifest) validate() error {
	for _, e := range m.Env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" {
			return fmt.Errorf("%w: %q env %q is not KEY=VALUE", ErrInvalidManifest, m.Name, e)
		}
	}
	for _, p := range m.Mounts {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%w: %q mount %q is not absolute", ErrInvalidManifest, m.Name, p)
		}
	}
	switch {
	case m.ID <= 0:
		return fmt.Errorf("%w: %q has no id", ErrInvalidManifest, m.Name)
	case m.Name == "":
		return fmt.Errorf("%w: id %d has no name", ErrInvalidManifest, m.ID)
	case m.Source == "":
		return fmt.Errorf("%w: %q has no source filename", ErrInvalidManifest, m.Name)
	case len(m.Run) == 0:
		return fmt.Errorf("%w: %q has no run command", ErrInvalidManifest, m.Name)
	}
	return nil
}

func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		if sErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return Manifest{}, fmt.Errorf("unknown key(s):\n%s", sErr.String())
		}
		if dErr, ok := errors.AsType[*toml.DecodeError](err); ok {
			return Manifest{}, fmt.Errorf("\n%s", dErr.String())
		}
		return Manifest{}, err
	}
	return m, nil
}
