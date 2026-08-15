// Package lang turns declarative language manifests into the argv the sandbox runs.
// Adding a language is normally a block in languages.toml; the Hook interface exists
// for the few that need behaviour a template cannot express.
package lang

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/JustModo/citron/internal/judge"
)

type Manifest struct {
	ID     int    `toml:"id"`
	Name   string `toml:"name"`
	Label  string `toml:"label"`
	Source string `toml:"source"`
	Binary string `toml:"binary"`

	Compile []string `toml:"compile"`
	Run     []string `toml:"run"`
	Probe   []string `toml:"probe"`

	// Hook names a Go-side behaviour for languages a template cannot describe.
	Hook string `toml:"hook"`

	Limits ManifestLimits `toml:"limits"`
}

// ManifestLimits adjusts the configured execution limits for one language. A runtime
// with a fixed startup cost needs more than the baseline, or every submission in that
// language fails for reasons unrelated to the submitted code.
type ManifestLimits struct {
	MemoryExtraMB  int64   `toml:"memory_extra_mb"`
	MaxProcesses   int     `toml:"max_processes"`
	WallMultiplier float64 `toml:"wall_multiplier"`
	CPUMultiplier  float64 `toml:"cpu_multiplier"`
}

func scale(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * f)
}

// Apply returns limits adjusted for this language. The extra memory is added to the
// enforced ceiling, not to what the submission is told it may use.
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

// renderCtx holds every value a manifest template may reference. All of it is
// citron-controlled: filenames come from the manifest or a sanitized hook, numbers
// from configuration. Submitted source never reaches a template.
type renderCtx struct {
	Source  string
	Binary  string
	Dir     string
	StackKB int64
	HeapMB  int64
	MemMB   int64
}

type Language struct {
	manifest Manifest
	hook     Hook
	compile  []*template.Template
	run      []*template.Template
}

func (l *Language) ID() judge.LanguageID   { return judge.LanguageID(l.manifest.ID) }
func (l *Language) Name() string           { return l.manifest.Name }
func (l *Language) Label() string          { return l.manifest.Label }
func (l *Language) Manifest() Manifest     { return l.manifest }
func (l *Language) ProbeCommand() []string { return l.manifest.Probe }

// Compiled reports whether the language produces an artifact that testcases share.
// Interpreted languages still have a compile step here (a syntax check), so this asks
// whether the artifact is a binary rather than whether a compile command exists.
func (l *Language) Compiled() bool { return l.manifest.Binary != "" }

// Limits adjusts the configured limits for this language.
func (l *Language) Limits(base judge.Limits) judge.Limits { return l.manifest.Limits.Apply(base) }

// Files returns the source filename and artifact name for a submission. The hook may
// derive them from the source; Java must name the file after its public class.
func (l *Language) Files(source []byte) (src, binary string) {
	src, binary = l.manifest.Source, l.manifest.Binary
	if l.hook != nil {
		src, binary = l.hook.Files(source, l.manifest)
	}
	return src, binary
}

// CompileArgv is nil when the language has no compile step.
func (l *Language) CompileArgv(c Context) ([]string, error) { return render(l.compile, c) }
func (l *Language) RunArgv(c Context) ([]string, error)     { return render(l.run, c) }

// Context is what a caller must supply to build argv.
type Context struct {
	Source  string
	Binary  string
	Dir     string
	Limits  judge.Limits
	BaseMem judge.MemoryBytes // memory before the language's extra headroom
}

func render(tmpls []*template.Template, c Context) ([]string, error) {
	if len(tmpls) == 0 {
		return nil, nil
	}
	rc := renderCtx{
		Source:  c.Source,
		Binary:  c.Binary,
		Dir:     c.Dir,
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

var ErrInvalidManifest = errors.New("invalid language manifest")

func (m Manifest) validate(hooks Hooks) error {
	switch {
	case m.ID <= 0:
		return fmt.Errorf("%w: %q has no id", ErrInvalidManifest, m.Name)
	case m.Name == "":
		return fmt.Errorf("%w: id %d has no name", ErrInvalidManifest, m.ID)
	case m.Source == "":
		return fmt.Errorf("%w: %q has no source filename", ErrInvalidManifest, m.Name)
	case len(m.Run) == 0:
		return fmt.Errorf("%w: %q has no run command", ErrInvalidManifest, m.Name)
	case m.Hook != "" && hooks[m.Hook] == nil:
		return fmt.Errorf("%w: %q names unknown hook %q", ErrInvalidManifest, m.Name, m.Hook)
	}
	return nil
}

type manifestFile struct {
	Language []Manifest `toml:"language"`
}

func parseManifests(data []byte) ([]Manifest, error) {
	var f manifestFile
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		if sErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return nil, fmt.Errorf("languages: unknown key(s):\n%s", sErr.String())
		}
		if dErr, ok := errors.AsType[*toml.DecodeError](err); ok {
			return nil, fmt.Errorf("languages:\n%s", dErr.String())
		}
		return nil, fmt.Errorf("languages: %w", err)
	}
	if len(f.Language) == 0 {
		return nil, fmt.Errorf("%w: no languages defined", ErrInvalidManifest)
	}
	return f.Language, nil
}

func loadFile(path string) ([]Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("languages: %w", err)
	}
	return parseManifests(data)
}
