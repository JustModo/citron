package lang

import (
	"strings"
	"testing"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// Tests here cover the manifest machinery only, using inline manifests and a stub
// hook. The shipped languages.toml and the real hooks are exercised in
// internal/lang/hooks, which can import both without a cycle.

func baseLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
		Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
		MaxFileSize: 16 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
	}
}

// stubHook stands in for a real language hook: it renames the source after the first
// line of the submission, which is enough to prove the hook is consulted.
type stubHook struct{}

func (stubHook) Files(source []byte, m Manifest) (string, string) {
	name, _, _ := strings.Cut(string(source), "\n")
	if name == "" {
		return m.Source, m.Binary
	}
	return name + ".src", name
}

func registryFrom(t *testing.T, body string, hooks Hooks) *Registry {
	t.Helper()
	manifests, err := parseManifests([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r, err := newRegistry(manifests, hooks)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}

const twoLanguages = `
[[language]]
id = 1
name = "compiled"
label = "Compiled"
source = "main.x"
binary = "main"
compile = ["xc", "-o", "{{.Binary}}", "{{.Source}}"]
run = ["./{{.Binary}}"]
probe = ["xc", "--version"]

[[language]]
id = 2
name = "interpreted"
label = "Interpreted"
source = "main.y"
run = ["yrun", "{{.Source}}", "--dir", "{{.Dir}}"]
`

func TestRegistryLookups(t *testing.T) {
	r := registryFrom(t, twoLanguages, nil)

	l, err := r.ByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if l.Name() != "compiled" || l.Label() != "Compiled" {
		t.Errorf("id 1 resolved to %q/%q", l.Name(), l.Label())
	}
	if _, err := r.ByName("interpreted"); err != nil {
		t.Errorf("lookup by name failed: %v", err)
	}
	if _, err := r.ByID(9999); err == nil {
		t.Error("unknown id should error")
	}
	if _, err := r.ByName("cobol"); err == nil {
		t.Error("unknown name should error")
	}
	if len(r.All()) != 2 {
		t.Errorf("All() returned %d languages, want 2", len(r.All()))
	}
}

func TestArgvRendering(t *testing.T) {
	r := registryFrom(t, twoLanguages, nil)
	base := baseLimits()

	compiled, _ := r.ByName("compiled")
	src, bin := compiled.Files(nil)
	ctx := Context{Source: src, Binary: bin, Dir: "/box", Limits: base, BaseMem: base.Memory}

	compile, err := compiled.CompileArgv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(compile, " "); got != "xc -o main main.x" {
		t.Errorf("compile argv = %q", got)
	}
	run, err := compiled.RunArgv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(run, " "); got != "./main" {
		t.Errorf("run argv = %q", got)
	}

	interpreted, _ := r.ByName("interpreted")
	src, bin = interpreted.Files(nil)
	ctx = Context{Source: src, Binary: bin, Dir: "/box", Limits: base, BaseMem: base.Memory}
	if argv, _ := interpreted.CompileArgv(ctx); argv != nil {
		t.Errorf("a language with no compile command should render nil, got %v", argv)
	}
	run, err = interpreted.RunArgv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(run, " ")
	if joined != "yrun main.y --dir /box" {
		t.Errorf("run argv = %q", joined)
	}
	if strings.Contains(joined, "{{") {
		t.Errorf("unrendered template left in argv: %v", run)
	}
}

// A hook, when configured, decides the filenames instead of the manifest.
func TestHookOverridesFilenames(t *testing.T) {
	body := `
[[language]]
id = 3
name = "hooked"
source = "default.src"
binary = "default"
run = ["run", "{{.Source}}"]
hook = "stub"
`
	r := registryFrom(t, body, Hooks{"stub": stubHook{}})
	l, _ := r.ByName("hooked")

	src, bin := l.Files([]byte("Derived\nrest of the source"))
	if src != "Derived.src" || bin != "Derived" {
		t.Errorf("hook not consulted: source=%q binary=%q", src, bin)
	}

	// A hook that cannot derive a name falls back to the manifest.
	if src, bin = l.Files(nil); src != "default.src" || bin != "default" {
		t.Errorf("fallback failed: source=%q binary=%q", src, bin)
	}
}

func TestLimitMultipliers(t *testing.T) {
	body := `
[[language]]
id = 4
name = "heavy"
source = "main.z"
run = ["z", "{{.Source}}"]

[language.limits]
memory_extra_mb = 256
max_processes = 64
wall_multiplier = 2.0
cpu_multiplier = 1.5
`
	r := registryFrom(t, body, nil)
	l, _ := r.ByName("heavy")

	base := baseLimits()
	got := l.Limits(base)

	if got.Memory != base.Memory+(256<<20) {
		t.Errorf("memory = %d MB, want %d", got.Memory.MB(), base.Memory.MB()+256)
	}
	if got.MaxProcesses != 64 {
		t.Errorf("max processes = %d, want 64", got.MaxProcesses)
	}
	if got.WallTime != 8*time.Second {
		t.Errorf("wall time = %v, want 8s", got.WallTime)
	}
	if got.CPUTime != 3*time.Second {
		t.Errorf("cpu time = %v, want 3s", got.CPUTime)
	}
	// Untouched fields must survive.
	if got.MaxStdout != base.MaxStdout {
		t.Error("multipliers altered an unrelated limit")
	}
}

// A runtime that sizes a heap must be given what the submission was promised, not
// the padded ceiling — otherwise the headroom is handed straight back to the heap.
func TestBaseMemoryIsSeparateFromTheCeiling(t *testing.T) {
	body := `
[[language]]
id = 5
name = "heaped"
source = "main.h"
run = ["run", "-Xmx{{.HeapMB}}m", "-ceiling{{.MemMB}}", "-Xss{{.StackKB}}k"]

[language.limits]
memory_extra_mb = 256
`
	r := registryFrom(t, body, nil)
	l, _ := r.ByName("heaped")

	base := baseLimits()
	argv, err := l.RunArgv(Context{
		Source: "main.h", Limits: l.Limits(base), BaseMem: base.Memory,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-Xmx256m") {
		t.Errorf("heap should be the promised 256m: %q", joined)
	}
	if !strings.Contains(joined, "-ceiling512") {
		t.Errorf("ceiling should include the headroom: %q", joined)
	}
	if !strings.Contains(joined, "-Xss65536k") {
		t.Errorf("stack should render in KB: %q", joined)
	}
}

func TestManifestValidation(t *testing.T) {
	tests := []struct {
		name, toml, want string
	}{
		{"no languages", "", "no languages defined"},
		{"missing id", "[[language]]\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n", "no id"},
		{"missing name", "[[language]]\nid=1\nsource=\"a\"\nrun=[\"a\"]\n", "no name"},
		{"missing source", "[[language]]\nid=1\nname=\"x\"\nrun=[\"a\"]\n", "no source filename"},
		{"missing run", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\n", "no run command"},
		{"unknown hook", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\nhook=\"nope\"\n", "unknown hook"},
		{"unknown key", "[[language]]\nid=1\nname=\"x\"\nsourse=\"a\"\nrun=[\"a\"]\n", "unknown key"},
		{"duplicate id", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n[[language]]\nid=1\nname=\"y\"\nsource=\"b\"\nrun=[\"b\"]\n", "duplicate id"},
		{"duplicate name", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n[[language]]\nid=2\nname=\"x\"\nsource=\"b\"\nrun=[\"b\"]\n", "duplicate name"},
		{"bad template", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"{{.Broken\"]\n", "argv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifests, err := parseManifests([]byte(tt.toml))
			if err == nil {
				_, err = newRegistry(manifests, nil)
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
		})
	}
}

// A language needing no custom behaviour should need no Go code at all.
func TestAddingALanguageIsConfigOnly(t *testing.T) {
	r := registryFrom(t, `
[[language]]
id = 60
name = "go"
label = "Go"
source = "main.go"
binary = "main"
compile = ["go", "build", "-o", "{{.Binary}}", "{{.Source}}"]
run = ["./{{.Binary}}"]
probe = ["go", "version"]
`, nil)

	l, err := r.ByName("go")
	if err != nil {
		t.Fatal(err)
	}
	src, bin := l.Files(nil)
	argv, err := l.RunArgv(Context{Source: src, Binary: bin, Limits: baseLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 1 || argv[0] != "./main" {
		t.Errorf("run argv = %v, want [./main]", argv)
	}
	if probe := l.ProbeCommand(); len(probe) != 2 || probe[0] != "go" {
		t.Errorf("probe = %v", probe)
	}
}
