package hooks_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustModo/judge/internal/judge"
	"github.com/JustModo/judge/internal/lang"
	"github.com/JustModo/judge/internal/lang/hooks"
)

// This package is where the shipped configuration meets the real hooks, so the tests
// that need both live here.

var manifestPath = filepath.Join("..", "..", "..", "configs", "languages.toml")

func baseLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
		Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
		MaxFileSize: 16 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
	}
}

func shipped(t *testing.T) *lang.Registry {
	t.Helper()
	r, err := lang.LoadRegistry(manifestPath, hooks.All())
	if err != nil {
		t.Fatalf("configs/languages.toml does not load: %v", err)
	}
	return r
}

// The consumer submits these Judge0 ids; they must keep resolving.
func TestShippedRegistry(t *testing.T) {
	r := shipped(t)
	for id, name := range map[judge.LanguageID]string{50: "c", 54: "cpp", 62: "java", 71: "python"} {
		l, err := r.ByID(id)
		if err != nil {
			t.Errorf("language id %d (%s) missing: %v", id, name, err)
			continue
		}
		if l.Name() != name {
			t.Errorf("id %d resolved to %q, want %q", id, l.Name(), name)
		}
		if len(l.ProbeCommand()) == 0 {
			t.Errorf("%s has no probe command; a missing toolchain would go unnoticed", name)
		}
	}
}

// Every hook a manifest names must be registered in All. Loading with no hooks at
// all must fail loudly, which is what makes a forgotten entry a boot error rather
// than a language that quietly misbehaves.
func TestManifestHooksAreRegistered(t *testing.T) {
	if _, err := lang.LoadRegistry(manifestPath, nil); err == nil {
		t.Fatal("loading without hooks should fail; a manifest names one")
	} else if !strings.Contains(err.Error(), "unknown hook") {
		t.Errorf("error should name the missing hook, got: %v", err)
	}
}

func TestShippedArgvRendering(t *testing.T) {
	r := shipped(t)
	tests := []struct {
		language   string
		source     string
		wantSource string
		wantBinary string
		compileHas []string
		runHas     []string
	}{
		{
			language: "c", source: "int main(){}",
			wantSource: "main.c", wantBinary: "main",
			compileHas: []string{"gcc", "main.c", "main"},
			runHas:     []string{"./main"},
		},
		{
			language: "cpp", source: "int main(){}",
			wantSource: "main.cpp", wantBinary: "main",
			compileHas: []string{"g++", "main.cpp"},
			runHas:     []string{"./main"},
		},
		{
			language: "python", source: "print(1)",
			wantSource: "main.py",
			runHas:     []string{"python3", "main.py"},
		},
		{
			// The Java hook renames the file after the public class.
			language: "java", source: "public class Solution { }",
			wantSource: "Solution.java", wantBinary: "Solution",
			compileHas: []string{"javac", "Solution.java"},
			runHas:     []string{"java", "Solution", "-Xss65536k", "-Xmx256m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			l, err := r.ByName(tt.language)
			if err != nil {
				t.Fatal(err)
			}
			source, binary := l.Files([]byte(tt.source))
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
			if tt.wantBinary != "" && binary != tt.wantBinary {
				t.Errorf("binary = %q, want %q", binary, tt.wantBinary)
			}

			base := baseLimits()
			ctx := lang.Context{
				Source: source, Binary: binary, Dir: "/box",
				Limits: l.Limits(base), BaseMem: base.Memory,
			}
			compile, err := l.CompileArgv(ctx)
			if err != nil {
				t.Fatalf("compile argv: %v", err)
			}
			run, err := l.RunArgv(ctx)
			if err != nil {
				t.Fatalf("run argv: %v", err)
			}

			joinedCompile, joinedRun := strings.Join(compile, " "), strings.Join(run, " ")
			for _, want := range tt.compileHas {
				if !strings.Contains(joinedCompile, want) {
					t.Errorf("compile argv %q is missing %q", joinedCompile, want)
				}
			}
			for _, want := range tt.runHas {
				if !strings.Contains(joinedRun, want) {
					t.Errorf("run argv %q is missing %q", joinedRun, want)
				}
			}
			if strings.Contains(joinedCompile+joinedRun, "{{") {
				t.Errorf("unrendered template: %q %q", joinedCompile, joinedRun)
			}
		})
	}
}

// The JVM's non-heap overhead has to live somewhere. Without headroom above the
// heap, a "256 MB" Java submission OOMs on memory it never asked for.
func TestJavaGetsHeadroomAboveItsHeap(t *testing.T) {
	java, err := shipped(t).ByName("java")
	if err != nil {
		t.Fatal(err)
	}
	base := baseLimits()
	got := java.Limits(base)

	if got.Memory <= base.Memory {
		t.Errorf("java ceiling %d MB should exceed the promised %d MB", got.Memory.MB(), base.Memory.MB())
	}
	if got.MaxProcesses <= base.MaxProcesses {
		t.Errorf("java needs more than %d pids; JVM threads are pids", base.MaxProcesses)
	}
	if got.WallTime <= base.WallTime {
		t.Error("java needs a longer wall clock than the baseline")
	}

	argv, err := java.RunArgv(lang.Context{
		Source: "Main.java", Binary: "Main", Limits: got, BaseMem: base.Memory,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-Xmx256m") {
		t.Errorf("heap should be the promised 256m, not the padded ceiling: %q", joined)
	}
	// A JVM that spawns a GC thread per core will not fit in a small pid budget.
	if !strings.Contains(joined, "UseSerialGC") || !strings.Contains(joined, "ActiveProcessorCount=1") {
		t.Errorf("java run flags should keep the thread count down: %q", joined)
	}
}
