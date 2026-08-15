package lang

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

func baseLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
		Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
		MaxFileSize: 16 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
	}
}

// The shipped manifests must load: a typo here breaks every submission.
func shipped(t *testing.T) *Registry {
	t.Helper()
	r, err := LoadRegistry(filepath.Join("..", "..", "configs", "languages.toml"))
	if err != nil {
		t.Fatalf("configs/languages.toml does not load: %v", err)
	}
	return r
}

func TestShippedRegistry(t *testing.T) {
	r := shipped(t)
	// The consumer sends these Judge0 ids; they must keep resolving.
	for id, name := range map[judge.LanguageID]string{50: "c", 54: "cpp", 62: "java", 71: "python"} {
		l, err := r.ByID(id)
		if err != nil {
			t.Errorf("language id %d (%s) missing: %v", id, name, err)
			continue
		}
		if l.Name() != name {
			t.Errorf("id %d resolved to %q, want %q", id, l.Name(), name)
		}
	}
	if _, err := r.ByID(9999); err == nil {
		t.Error("unknown id should error")
	}
	if _, err := r.ByName("cobol"); err == nil {
		t.Error("unknown name should error")
	}
}

func TestArgvRendering(t *testing.T) {
	r := shipped(t)
	tests := []struct {
		lang        string
		source      []byte
		wantSrc     string
		wantBin     string
		compileHas  []string
		runHas      []string
		runNotEmpty bool
	}{
		{
			lang: "c", source: []byte("int main(){}"),
			wantSrc: "main.c", wantBin: "main",
			compileHas: []string{"gcc", "main.c", "main"},
			runHas:     []string{"./main"},
		},
		{
			lang: "python", source: []byte("print(1)"),
			wantSrc: "main.py",
			runHas:  []string{"python3", "main.py"},
		},
		{
			lang: "java", source: []byte("public class Solution { }"),
			wantSrc: "Solution.java", wantBin: "Solution",
			compileHas: []string{"javac", "Solution.java"},
			runHas:     []string{"java", "Solution", "-Xss65536k", "-Xmx256m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			l, err := r.ByName(tt.lang)
			if err != nil {
				t.Fatal(err)
			}
			src, bin := l.Files(tt.source)
			if src != tt.wantSrc {
				t.Errorf("source = %q, want %q", src, tt.wantSrc)
			}
			if tt.wantBin != "" && bin != tt.wantBin {
				t.Errorf("binary = %q, want %q", bin, tt.wantBin)
			}

			base := baseLimits()
			ctx := Context{Source: src, Binary: bin, Dir: "/box", Limits: l.Limits(base), BaseMem: base.Memory}
			compile, err := l.CompileArgv(ctx)
			if err != nil {
				t.Fatalf("compile argv: %v", err)
			}
			run, err := l.RunArgv(ctx)
			if err != nil {
				t.Fatalf("run argv: %v", err)
			}
			joinedC, joinedR := strings.Join(compile, " "), strings.Join(run, " ")
			for _, want := range tt.compileHas {
				if !strings.Contains(joinedC, want) {
					t.Errorf("compile argv %q missing %q", joinedC, want)
				}
			}
			for _, want := range tt.runHas {
				if !strings.Contains(joinedR, want) {
					t.Errorf("run argv %q missing %q", joinedR, want)
				}
			}
			if strings.Contains(joinedC+joinedR, "{{") {
				t.Errorf("unrendered template left in argv: %q %q", joinedC, joinedR)
			}
		})
	}
}

// Java heap must be the requested memory, while the enforced ceiling is higher — the
// JVM's non-heap overhead has to live somewhere or every Java submission OOMs.
func TestJavaGetsHeadroomAboveItsHeap(t *testing.T) {
	r := shipped(t)
	java, err := r.ByName("java")
	if err != nil {
		t.Fatal(err)
	}
	base := baseLimits()
	got := java.Limits(base)
	if got.Memory <= base.Memory {
		t.Errorf("java memory ceiling %d MB should exceed the base %d MB", got.Memory.MB(), base.Memory.MB())
	}
	if got.MaxProcesses <= base.MaxProcesses {
		t.Errorf("java needs more pids than %d (JVM threads are pids)", base.MaxProcesses)
	}
	if got.WallTime <= base.WallTime {
		t.Error("java needs a longer wall clock than the baseline")
	}

	argv, err := java.RunArgv(Context{Source: "Main.java", Binary: "Main", Limits: got, BaseMem: base.Memory})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-Xmx256m") {
		t.Errorf("heap should be the requested 256m, not the padded ceiling: %q", joined)
	}
}

func TestJavaClassNameExtraction(t *testing.T) {
	tests := []struct {
		name, source, wantSrc string
	}{
		{"public class", "public class Foo {}", "Foo.java"},
		{"final", "public final class Bar {}", "Bar.java"},
		{"record", "public record Point(int x) {}", "Point.java"},
		{"leading imports", "import java.util.*;\n\npublic class Baz {}", "Baz.java"},
		{"no public class falls back", "class Helper {}", "Main.java"},
		{"non-public keyword in a comment", "// public class Sneaky\nclass X {}", "Main.java"},
		{"empty source falls back", "", "Main.java"},
	}
	m := Manifest{Source: "Main.java", Binary: "Main"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, bin := javaHook{}.Files([]byte(tt.source), m)
			if src != tt.wantSrc {
				t.Errorf("source = %q, want %q", src, tt.wantSrc)
			}
			if bin+".java" != src {
				t.Errorf("binary %q inconsistent with source %q", bin, src)
			}
		})
	}
}

// The class name reaches a filename and an argv element, so a hostile one must not
// escape the identifier shape.
func TestJavaClassNameIsSanitized(t *testing.T) {
	safe := regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*\.java$`)
	m := Manifest{Source: "Main.java", Binary: "Main"}
	for _, src := range []string{
		"public class ../../etc/passwd {}",
		"public class Foo;rm -rf / {}",
		"public class $(id) {}",
		"public class " + strings.Repeat("A", 200) + " {}",
	} {
		got, bin := javaHook{}.Files([]byte(src), m)
		if !safe.MatchString(got) {
			t.Errorf("unsanitized filename from %q: %q", src, got)
		}
		if strings.ContainsAny(bin, "/;$ .") {
			t.Errorf("unsanitized binary name from %q: %q", src, bin)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	tests := []struct {
		name, toml, want string
	}{
		{"no languages", "", "no languages defined"},
		{"missing id", "[[language]]\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n", "no id"},
		{"missing name", "[[language]]\nid=1\nsource=\"a\"\nrun=[\"a\"]\n", "no name"},
		{"missing run", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\n", "no run command"},
		{"unknown hook", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\nhook=\"nope\"\n", "unknown hook"},
		{"unknown key", "[[language]]\nid=1\nname=\"x\"\nsourse=\"a\"\nrun=[\"a\"]\n", "unknown key"},
		{"duplicate id", "[[language]]\nid=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\n[[language]]\nid=1\nname=\"y\"\nsource=\"b\"\nrun=[\"b\"]\n", "duplicate id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, err := parseManifests([]byte(tt.toml))
			if err == nil {
				_, err = newRegistry(ms)
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

// A new language should need no Go changes.
func TestAddingALanguageIsConfigOnly(t *testing.T) {
	ms, err := parseManifests([]byte(`
[[language]]
id = 60
name = "go"
label = "Go"
source = "main.go"
binary = "main"
compile = ["go", "build", "-o", "{{.Binary}}", "{{.Source}}"]
run = ["./{{.Binary}}"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r, err := newRegistry(ms)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
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
}
