package run

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustModo/citron/internal/compare"
	"github.com/JustModo/citron/internal/judge"
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/sandbox"
	"github.com/JustModo/citron/internal/workspace"
)

func testRunner(t *testing.T) *Runner {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := lang.LoadRegistry(filepath.Join("..", "..", "languages"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ws, err := workspace.NewManager(filepath.Join(root, "box"))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewCompileCache(filepath.Join(root, "cache"), 32, 256<<20)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunner(registry, sandbox.NewLocal(log), ws, cache, compare.Default(), nil, Options{
		CompileLimits: judge.Limits{
			CPUTime: 20 * time.Second, WallTime: 25 * time.Second,
			Memory: 512 << 20, Stack: 64 << 20, MaxProcesses: 128,
			MaxFileSize: 64 << 20, MaxStdout: 64 << 10, MaxStderr: 64 << 10,
		},
		MaxParallel:     4,
		SubmissionLimit: 60 * time.Second,
	}, log)
}

func execLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, CPUExtraTime: 500 * time.Millisecond,
		WallTime: 3 * time.Second,
		Memory:   256 << 20, Stack: 16 << 20, MaxProcesses: 64,
		MaxFileSize: 16 << 20, MaxStdout: 64 << 10, MaxStderr: 64 << 10,
	}
}

// requireToolchain skips unless bin is on the PATH submissions see, which is where
// compilers and pack scripts look for it.
func requireToolchain(t *testing.T, bin string) {
	t.Helper()
	path, _ := strings.CutPrefix(sandboxEnv[0], "PATH=")
	for _, dir := range filepath.SplitList(path) {
		if _, err := exec.LookPath(filepath.Join(dir, bin)); err == nil {
			return
		}
	}
	t.Skipf("%s not on the sandbox PATH %s", bin, path)
}

func submit(id string, language judge.LanguageID, source string, cases ...judge.TestCase) judge.Submission {
	return judge.Submission{
		ID: judge.SubmissionID(id), Language: language,
		Source: []byte(source), TestCases: cases, Limits: execLimits(),
	}
}

func tc(i int, stdin, expected string) judge.TestCase {
	return judge.TestCase{
		Index: judge.TestCaseIndex(i), Stdin: []byte(stdin), ExpectedOutput: []byte(expected),
	}
}

const pySumSource = `
import sys
a, b = map(int, sys.stdin.read().split())
print(a + b)
`

func TestAllTestCasesRunDespiteFailures(t *testing.T) {
	requireToolchain(t, "python3")

	sub := submit("mixed", 71, `
import sys
data = sys.stdin.read().split()
mode = data[0]
if mode == "ok":
    print("correct")
elif mode == "wrong":
    print("nonsense")
elif mode == "crash":
    raise SystemExit(3)
elif mode == "hang":
    while True: pass
`,
		tc(0, "ok", "correct"),
		tc(1, "wrong", "correct"),
		tc(2, "crash", "correct"),
		tc(3, "hang", "correct"),
		tc(4, "ok", "correct"),
	)

	res, err := testRunner(t).Run(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.TestCases) != 5 {
		t.Fatalf("got %d results, want 5 — a failure stopped the run", len(res.TestCases))
	}

	want := []judge.Status{
		judge.StatusAccepted,
		judge.StatusWrongAnswer,
		judge.StatusRuntimeErrorNonZeroExit,
		judge.StatusTimeLimitExceeded,
		judge.StatusAccepted,
	}
	for i, w := range want {
		if got := res.TestCases[i].Status; got != w {
			t.Errorf("testcase %d: got %v, want %v (stderr: %s)", i, got, w, res.TestCases[i].Stderr)
		}
		if res.TestCases[i].Index != judge.TestCaseIndex(i) {
			t.Errorf("result %d carries index %d; index is the identity", i, res.TestCases[i].Index)
		}
	}
	// The worst testcase decides the submission.
	if res.Status != judge.StatusTimeLimitExceeded {
		t.Errorf("submission status = %v, want %v", res.Status, judge.StatusTimeLimitExceeded)
	}
}

func TestCompilationErrorFailsEveryTestCaseWithoutRunning(t *testing.T) {
	requireToolchain(t, "gcc")

	sub := submit("broken", 50, `int main( { return 0 }`, tc(0, "", "x"), tc(1, "", "y"))
	res, err := testRunner(t).Run(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != judge.StatusCompilationError {
		t.Fatalf("status = %v, want Compilation Error", res.Status)
	}
	if res.Compile.Success {
		t.Error("compile reported success for source that does not compile")
	}
	if len(res.Compile.Output) == 0 {
		t.Error("compiler diagnostics were dropped; the student sees nothing")
	}
	if len(res.TestCases) != 2 {
		t.Fatalf("got %d testcase results, want 2", len(res.TestCases))
	}
	for _, r := range res.TestCases {
		if r.Status != judge.StatusCompilationError {
			t.Errorf("testcase %d: got %v", r.Index, r.Status)
		}
	}
}

func TestLanguages(t *testing.T) {
	tests := []struct {
		name   string
		bin    string
		id     judge.LanguageID
		source string
	}{
		{"c", "gcc", 50, `#include <stdio.h>
int main(){int a,b;scanf("%d %d",&a,&b);printf("%d\n",a+b);return 0;}`},
		{"cpp", "g++", 54, `#include <iostream>
int main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<std::endl;return 0;}`},
		{"python", "python3", 71, pySumSource},
		{"java", "javac", 62, `import java.util.Scanner;
public class Main {
    public static void main(String[] args) {
        Scanner s = new Scanner(System.in);
        System.out.println(s.nextInt() + s.nextInt());
    }
}`},
		{"javascript", "node", 63, `const [a, b] = require("fs").readFileSync(0, "utf8").trim().split(/\s+/).map(Number);
console.log(a + b);`},
		{"typescript", "tsc", 74, `import { readFileSync } from "fs";
const [a, b]: number[] = readFileSync(0, "utf8").trim().split(/\s+/).map(Number);
console.log(a + b);`},
		{"go", "go", 60, `package main

import "fmt"

func main() {
	var a, b int
	fmt.Scan(&a, &b)
	fmt.Println(a + b)
}`},
		{"rust", "rustc", 73, `use std::io::Read;
fn main() {
    let mut s = String::new();
    std::io::stdin().read_to_string(&mut s).unwrap();
    let n: Vec<i64> = s.split_whitespace().map(|x| x.parse().unwrap()).collect();
    println!("{}", n[0] + n[1]);
}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireToolchain(t, tt.bin)
			res, err := testRunner(t).Run(context.Background(),
				submit(tt.name, tt.id, tt.source, tc(0, "2 3", "5"), tc(1, "10 -4", "6")))
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != judge.StatusAccepted {
				t.Fatalf("status = %v; compile output: %s; stderr: %s",
					res.Status, res.Compile.Output, res.TestCases[0].Stderr)
			}
			for _, r := range res.TestCases {
				if r.WallTime <= 0 {
					t.Errorf("testcase %d has no wall time", r.Index)
				}
			}
		})
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct {
		name, bin string
		id        judge.LanguageID
		source    string
	}{
		{"javascript", "node", 63, "console.log(("},
		{"typescript", "tsc", 74, `const x: number = "text";`},
		{"go", "go", 60, "package main\nfunc main() { undefined() }"},
		{"rust", "rustc", 73, "fn main() { let x: i32 = \"text\"; }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireToolchain(t, tt.bin)
			res, err := testRunner(t).Run(context.Background(), submit(tt.name, tt.id, tt.source, tc(0, "", "")))
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != judge.StatusCompilationError || len(res.Compile.Output) == 0 {
				t.Errorf("status = %v, output %q; want a compilation error with diagnostics", res.Status, res.Compile.Output)
			}
		})
	}
}

// A pack variable replaces a base variable of the same name rather than duplicating it.
func TestPackEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "id=1\nname=\"x\"\nsource=\"a\"\nrun=[\"a\"]\nenv=[\"PATH=/opt/x/bin\", \"FOO=bar\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "x", lang.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := lang.LoadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := registry.ByName("x")

	env := envFor(l)
	var paths []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
	}
	if len(paths) != 1 || paths[0] != "PATH=/opt/x/bin" {
		t.Errorf("PATH entries = %v, want only the pack's", paths)
	}
	if !slices.Contains(env, "FOO=bar") || !slices.Contains(env, "HOME=/tmp") {
		t.Errorf("env = %v", env)
	}
}

func TestJavaClassNaming(t *testing.T) {
	requireToolchain(t, "javac")
	requireToolchain(t, "javap")
	tests := []struct {
		name, source string
		want         judge.Status
	}{
		{"public class not named Main", `
public class Solution {
    public static void main(String[] a) { System.out.println(5); }
}`, judge.StatusAccepted},
		{"package-private main beside a public class", `
public class Solution { int answer() { return 5; } }
class Main {
    public static void main(String[] a) { System.out.println(new Solution().answer()); }
}`, judge.StatusAccepted},
		{"no public class", `
class Helper {
    public static void main(String[] a) { System.out.println(5); }
}`, judge.StatusAccepted},
		{"no main", `public class Library { }`, judge.StatusCompilationError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := testRunner(t).Run(context.Background(), submit(tt.name, 62, tt.source, tc(0, "", "5")))
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.want {
				t.Errorf("status = %v, want %v; compile output: %s", res.Status, tt.want, res.Compile.Output)
			}
		})
	}
}

func TestCompilationHappensOncePerSubmission(t *testing.T) {
	requireToolchain(t, "gcc")

	cases := make([]judge.TestCase, 8)
	for i := range cases {
		cases[i] = tc(i, "1 1", "2")
	}
	source := `#include <stdio.h>
int main(){int a,b;scanf("%d %d",&a,&b);printf("%d\n",a+b);return 0;}`

	r := testRunner(t)
	start := time.Now()
	res, err := r.Run(context.Background(), submit("once", 50, source, cases...))
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if res.Status != judge.StatusAccepted {
		t.Fatalf("status = %v", res.Status)
	}
	t.Logf("8 testcases in %v (compile %v)", elapsed, res.Compile.Duration)

	second, err := r.Run(context.Background(), submit("again", 50, source, cases...))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Compile.Cached {
		t.Error("identical source recompiled instead of hitting the compile cache")
	}
}

// Concurrent identical compiles collapse into one via singleflight.
func TestConcurrentIdenticalSubmissionsCompileOnce(t *testing.T) {
	requireToolchain(t, "gcc")

	source := `#include <stdio.h>
int main(){int a,b;scanf("%d %d",&a,&b);printf("%d\n",a+b);return 0;}`
	r := testRunner(t)

	var wg sync.WaitGroup
	results := make([]judge.SubmissionResult, 6)
	errs := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = r.Run(context.Background(),
				submit("concurrent", 50, source, tc(0, "2 2", "4")))
		}()
	}
	wg.Wait()

	compiles := 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("submission %d: %v", i, errs[i])
		}
		if res.Status != judge.StatusAccepted {
			t.Errorf("submission %d: status %v", i, res.Status)
		}
		if !res.Compile.Cached {
			compiles++
		}
	}
	if compiles != 1 {
		t.Errorf("%d of 6 identical submissions compiled; want exactly 1", compiles)
	}
}

func TestOutputLimitProducesItsOwnVerdict(t *testing.T) {
	requireToolchain(t, "python3")

	sub := submit("bomb", 71, `
import sys
while True:
    sys.stdout.write("A" * 4096)
`, tc(0, "", "nothing"))
	sub.Limits.MaxStdout = 8 << 10

	res, err := testRunner(t).Run(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	got := res.TestCases[0]
	if got.Status != judge.StatusOutputLimitExceeded {
		t.Errorf("status = %v, want Output Limit Exceeded", got.Status)
	}
	if int64(len(got.Stdout)) > sub.Limits.MaxStdout {
		t.Errorf("captured %d bytes past the %d byte limit", len(got.Stdout), sub.Limits.MaxStdout)
	}
}

func TestTrailingWhitespaceDoesNotDecideAVerdict(t *testing.T) {
	requireToolchain(t, "python3")

	res, err := testRunner(t).Run(context.Background(),
		submit("ws", 71, `print("hello")`, tc(0, "", "hello"), tc(1, "", "hello\n\n")))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.TestCases {
		if r.Status != judge.StatusAccepted {
			t.Errorf("testcase %d: %v — trailing whitespace changed the verdict", r.Index, r.Status)
		}
	}
}

func TestUnknownLanguageIsRejected(t *testing.T) {
	_, err := testRunner(t).Run(context.Background(), submit("x", 9999, "print(1)", tc(0, "", "")))
	if err == nil || !strings.Contains(err.Error(), "unknown language") {
		t.Errorf("got %v, want an unknown-language error", err)
	}
}

func TestWorkspacesAreCleanedUp(t *testing.T) {
	requireToolchain(t, "python3")

	r := testRunner(t)
	if _, err := r.Run(context.Background(),
		submit("clean", 71, `print("x")`, tc(0, "", "x"), tc(1, "", "x"))); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(r.workspaces.Root(), "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d workspaces left behind: %v", len(entries), entries)
	}
}

// fakeSandbox completes compiles only if their context stays live for the compile
// duration, and echoes a fixed output for runs.
type fakeSandbox struct {
	compileTime time.Duration
	output      []byte
}

func (f fakeSandbox) Name() string { return "fake" }

func (f fakeSandbox) Run(ctx context.Context, spec sandbox.Spec) (sandbox.Result, error) {
	if spec.Stdin == nil && f.compileTime > 0 {
		select {
		case <-time.After(f.compileTime):
			return sandbox.Result{}, nil
		case <-ctx.Done():
			return sandbox.Result{TimedOut: true}, nil
		}
	}
	return sandbox.Result{Stdout: f.output}, nil
}

func fakeRunner(t *testing.T, sb sandbox.Sandbox, maxOutput int64) *Runner {
	t.Helper()
	r := testRunner(t)
	r.sandbox = sb
	r.opts.MaxReturnedOutput = maxOutput
	return r
}

// A client that disconnects mid-compile must not leave a cached compile failure for
// the next client with the same source.
func TestCancelledCompileIsNotCached(t *testing.T) {
	r := fakeRunner(t, fakeSandbox{compileTime: 200 * time.Millisecond, output: []byte("5\n")}, 0)
	src := "int main(){}"

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _ = r.Run(ctx, submit("a", 50, src, tc(0, "", "5")))

	res, err := r.Run(context.Background(), submit("b", 50, src, tc(0, "", "5")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Compile.Success || res.Status != judge.StatusAccepted {
		t.Errorf("second client got %v, compile output %q", res.Status, res.Compile.Output)
	}
}

// Program output kept for one submission is bounded in aggregate, not just per testcase.
func TestReturnedOutputIsBoundedPerSubmission(t *testing.T) {
	out := bytes.Repeat([]byte("x"), 1000)
	r := fakeRunner(t, fakeSandbox{output: out}, 2500)
	cases := make([]judge.TestCase, 5)
	for i := range cases {
		cases[i] = tc(i, "", string(out))
	}
	res, err := r.Run(context.Background(), submit("o", 71, "print()", cases...))
	if err != nil {
		t.Fatal(err)
	}
	var kept int
	for _, c := range res.TestCases {
		kept += len(c.Stdout)
		if c.Status != judge.StatusAccepted {
			t.Errorf("testcase %d: %v; dropping output must not change the verdict", c.Index, c.Status)
		}
	}
	if kept > 2500 {
		t.Errorf("kept %d bytes of output, budget is 2500", kept)
	}
}
