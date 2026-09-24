package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

func baseLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
		Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
		MaxFileSize: 16 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
	}
}

// writePacks creates one pack directory per entry, named by the key.
func writePacks(t *testing.T, packs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, manifest := range packs {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, ManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, packs map[string]string) *Registry {
	t.Helper()
	r, err := LoadRegistry(writePacks(t, packs))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const compiledPack = `
id = 1
name = "compiled"
label = "Compiled"
source = "main.x"
binary = "main"
compile = ["{{.Pack}}/build.sh", "-o", "{{.Binary}}", "{{.Source}}"]
run = ["./{{.Binary}}"]
probe = ["xc", "--version"]
mounts = ["/opt/xc"]
`

const interpretedPack = `
id = 2
name = "interpreted"
label = "Interpreted"
source = "main.y"
run = ["yrun", "{{.Source}}", "--dir", "{{.Dir}}"]
`

func TestRegistryLookups(t *testing.T) {
	r := load(t, map[string]string{"compiled": compiledPack, "interpreted": interpretedPack})

	l, err := r.ByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if l.Name() != "compiled" || l.Label() != "Compiled" {
		t.Errorf("id 1 resolved to %q/%q", l.Name(), l.Label())
	}
	if _, err := r.ByName("INTERPRETED"); err != nil {
		t.Errorf("lookup by name failed: %v", err)
	}
	if _, err := r.ByID(9999); err == nil {
		t.Error("unknown id should error")
	}
	if _, err := r.ByName("cobol"); err == nil {
		t.Error("unknown name should error")
	}
	if all := r.All(); len(all) != 2 || all[0].ID() != 1 {
		t.Errorf("All() = %d languages, want 2 ordered by id", len(all))
	}
}

func TestArgvRendering(t *testing.T) {
	dir := writePacks(t, map[string]string{"compiled": compiledPack, "interpreted": interpretedPack})
	r, err := LoadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := baseLimits()
	ctx := Context{Dir: "/box", Limits: base, BaseMem: base.Memory}

	compiled, _ := r.ByName("compiled")
	compile, err := compiled.CompileArgv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "compiled") + "/build.sh -o main main.x"
	if got := strings.Join(compile, " "); got != want {
		t.Errorf("compile argv = %q, want %q", got, want)
	}
	if run, _ := compiled.RunArgv(ctx); strings.Join(run, " ") != "./main" {
		t.Errorf("run argv = %q", run)
	}
	if m := compiled.Mounts(); len(m) != 1 || m[0] != "/opt/xc" {
		t.Errorf("mounts = %v", m)
	}

	interpreted, _ := r.ByName("interpreted")
	if argv, _ := interpreted.CompileArgv(ctx); argv != nil {
		t.Errorf("a language with no compile command should render nil, got %v", argv)
	}
	run, err := interpreted.RunArgv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(run, " "); got != "yrun main.y --dir /box" {
		t.Errorf("run argv = %q", got)
	}
}

func TestCompileLimits(t *testing.T) {
	r := load(t, map[string]string{"slow": `
id = 6
name = "slow"
source = "main.s"
run = ["s"]
env = ["GOMAXPROCS=1"]

[compile_limits]
memory_extra_mb = 512
wall_multiplier = 2.0
`})
	l, _ := r.ByName("slow")
	base := baseLimits()
	got := l.CompileLimits(base)
	if got.Memory != base.Memory+(512<<20) || got.WallTime != 8*time.Second {
		t.Errorf("compile limits = %+v", got)
	}
	if run := l.Limits(base); run != base {
		t.Error("compile_limits changed the execution limits")
	}
	if env := l.Env(); len(env) != 1 || env[0] != "GOMAXPROCS=1" {
		t.Errorf("env = %v", env)
	}
}

func TestLimitMultipliers(t *testing.T) {
	r := load(t, map[string]string{"heavy": `
id = 4
name = "heavy"
source = "main.z"
run = ["z", "{{.Source}}"]

[limits]
memory_extra_mb = 256
max_processes = 64
wall_multiplier = 2.0
cpu_multiplier = 1.5
`})
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
	if got.MaxStdout != base.MaxStdout {
		t.Error("multipliers altered an unrelated limit")
	}
}

// HeapMB must come from BaseMem, not the ceiling, or the headroom goes to the heap.
func TestBaseMemoryIsSeparateFromTheCeiling(t *testing.T) {
	r := load(t, map[string]string{"heaped": `
id = 5
name = "heaped"
source = "main.h"
run = ["run", "-Xmx{{.HeapMB}}m", "-ceiling{{.MemMB}}", "-Xss{{.StackKB}}k"]

[limits]
memory_extra_mb = 256
`})
	l, _ := r.ByName("heaped")

	base := baseLimits()
	argv, err := l.RunArgv(Context{Limits: l.Limits(base), BaseMem: base.Memory})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-Xmx256m", "-ceiling512", "-Xss65536k"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q should contain %q", joined, want)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	const valid = "id=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n"
	tests := []struct {
		name  string
		packs map[string]string
		want  string
	}{
		{"no packs", map[string]string{}, "no language packs"},
		{"missing id", map[string]string{"x": "name=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n"}, "no id"},
		{"missing name", map[string]string{"x": "id=1\nsource=\"a\"\nrun=[\"a\"]\n"}, "no name"},
		{"missing source", map[string]string{"x": "id=1\nname=\"x\"\nrun=[\"a\"]\n"}, "no source filename"},
		{"missing run", map[string]string{"x": "id=1\nname=\"x\"\nsource=\"a\"\n"}, "no run command"},
		{"unknown key", map[string]string{"x": valid + "hook=\"java\"\n"}, "unknown key"},
		{"relative mount", map[string]string{"x": valid + "mounts=[\"etc/x\"]\n"}, "not absolute"},
		{"malformed env", map[string]string{"x": valid + "env=[\"NOEQUALS\"]\n"}, "not KEY=VALUE"},
		{"empty env key", map[string]string{"x": valid + "env=[\"=v\"]\n"}, "not KEY=VALUE"},
		{"bad template", map[string]string{"x": "id=1\nname=\"x\"\nsource=\"a\"\nrun=[\"{{.Broken\"]\n"}, "argv"},
		{"duplicate id", map[string]string{"x": valid, "y": "id=1\nname=\"y\"\nsource=\"b\"\nrun=[\"b\"]\n"}, "duplicate id"},
		{"duplicate name", map[string]string{"x": valid, "y": "id=2\nname=\"x\"\nsource=\"b\"\nrun=[\"b\"]\n"}, "duplicate name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRegistry(writePacks(t, tt.packs))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should mention %q", err, tt.want)
			}
		})
	}
	if _, err := LoadRegistry(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing directory should error")
	}
}

func TestShippedPacks(t *testing.T) {
	r, err := LoadRegistry(filepath.Join("..", "..", "languages"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[judge.LanguageID]string{
		50: "c", 54: "cpp", 60: "go", 62: "java", 63: "javascript", 71: "python", 73: "rust", 74: "typescript",
	}
	if len(r.All()) != len(want) {
		t.Fatalf("got %d languages, want %d", len(r.All()), len(want))
	}
	base := baseLimits()
	for id, name := range want {
		l, err := r.ByID(id)
		if err != nil || l.Name() != name {
			t.Fatalf("id %d: got %v, %v; want %q", id, l, err, name)
		}
		ctx := Context{Dir: "/box", Limits: l.Limits(base), BaseMem: base.Memory}
		for _, render := range []func(Context) ([]string, error){l.CompileArgv, l.RunArgv} {
			argv, err := render(ctx)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if strings.Contains(strings.Join(argv, " "), "{{") {
				t.Errorf("%s: unrendered template in %v", name, argv)
			}
		}
	}

	java, _ := r.ByName("java")
	compile, _ := java.CompileArgv(Context{Limits: base})
	if _, err := os.Stat(compile[0]); err != nil {
		t.Errorf("java compile script %s: %v", compile[0], err)
	}
	if got := java.Limits(base); got.Memory <= base.Memory || got.MaxProcesses <= base.MaxProcesses {
		t.Errorf("java needs memory and process headroom above the base limits, got %+v", got)
	}
}
