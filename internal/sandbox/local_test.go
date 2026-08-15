package sandbox

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

func testLimits() judge.Limits {
	return judge.Limits{
		CPUTime: 2 * time.Second, CPUExtraTime: 500 * time.Millisecond,
		WallTime: 3 * time.Second,
		Memory:   256 << 20, Stack: 8 << 20, MaxProcesses: 64,
		MaxFileSize: 16 << 20, MaxStdout: 4 << 10, MaxStderr: 4 << 10,
	}
}

func newTestLocal(t *testing.T) *Local {
	t.Helper()
	return NewLocal(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func run(t *testing.T, spec Spec) Result {
	t.Helper()
	if spec.Limits == (judge.Limits{}) {
		spec.Limits = testLimits()
	}
	if spec.Dir == "" {
		spec.Dir = t.TempDir()
	}
	res, err := newTestLocal(t).Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("sandbox error: %v", err)
	}
	return res
}

func sh(script string) []string { return []string{"/bin/sh", "-c", script} }

func TestExitCodeAndOutput(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		stdin    string
		wantOut  string
		wantErr  string
		wantCode int
	}{
		{"stdout", sh(`echo hello`), "", "hello\n", "", 0},
		{"stderr", sh(`echo oops >&2`), "", "", "oops\n", 0},
		{"exit code", sh(`exit 3`), "", "", "", 3},
		{"stdin is delivered", sh(`cat`), "ping", "ping", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := run(t, Spec{Argv: tt.argv, Stdin: []byte(tt.stdin)})
			if got := string(res.Stdout); got != tt.wantOut {
				t.Errorf("stdout = %q, want %q", got, tt.wantOut)
			}
			if got := string(res.Stderr); got != tt.wantErr {
				t.Errorf("stderr = %q, want %q", got, tt.wantErr)
			}
			if res.ExitCode != tt.wantCode {
				t.Errorf("exit = %d, want %d", res.ExitCode, tt.wantCode)
			}
			if res.TimedOut {
				t.Error("should not have timed out")
			}
		})
	}
}

func TestWallClockTimeoutKills(t *testing.T) {
	lim := testLimits()
	lim.CPUTime = 300 * time.Millisecond
	lim.CPUExtraTime = 0
	lim.WallTime = 400 * time.Millisecond

	start := time.Now()
	res := run(t, Spec{Argv: sh(`sleep 30`), Limits: lim})
	elapsed := time.Since(start)

	if !res.TimedOut {
		t.Error("expected TimedOut")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v; the deadline did not kill the process", elapsed)
	}
}

func TestOutputBombIsBoundedAndKilled(t *testing.T) {
	lim := testLimits()
	lim.MaxStdout = 1 << 10
	lim.WallTime = 10 * time.Second
	lim.CPUTime = 9 * time.Second

	start := time.Now()
	res := run(t, Spec{Argv: sh(`yes AAAAAAAA`), Limits: lim})
	elapsed := time.Since(start)

	if int64(len(res.Stdout)) > lim.MaxStdout {
		t.Errorf("buffered %d bytes, limit was %d", len(res.Stdout), lim.MaxStdout)
	}
	if !res.StdoutTruncated || !res.OutputExceeded {
		t.Error("expected the output to be reported as truncated")
	}
	if res.TimedOut {
		t.Error("an output bomb is not a timeout; the two must stay distinguishable")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v; hitting the output limit should kill the process promptly", elapsed)
	}
}

// A program that forks and exits must not leave its children behind holding the
// workspace open. This is the one that catches killing the pid instead of the group.
func TestDescendantsDoNotSurvive(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "alive")

	lim := testLimits()
	lim.WallTime = 2 * time.Second
	lim.CPUTime = time.Second

	// The child outlives the parent and would write the marker a second later.
	run(t, Spec{
		Dir:    dir,
		Argv:   sh(`(sleep 3; touch ` + marker + `) & echo started`),
		Limits: lim,
	})

	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("orphaned descendant survived the sandbox and kept running")
	}
}

func TestEnvironmentIsNotInherited(t *testing.T) {
	t.Setenv("JUDGE_SECRET_TOKEN", "do-not-leak")
	res := run(t, Spec{Argv: sh(`env`), Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}})
	out := string(res.Stdout)
	if strings.Contains(out, "do-not-leak") {
		t.Error("the worker's environment leaked into the sandbox")
	}
	if !strings.Contains(out, "HOME=/tmp") {
		t.Errorf("the explicit environment was not passed: %q", out)
	}
}

func TestSignalIsReported(t *testing.T) {
	res := run(t, Spec{Argv: sh(`kill -SEGV $$`)})
	if res.Signal != int(syscall.SIGSEGV) {
		t.Errorf("signal = %d, want %d", res.Signal, syscall.SIGSEGV)
	}
	if !res.Killed() {
		t.Error("Killed() should be true for a signalled process")
	}
}

func TestWorkingDirectoryIsTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("present"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, Spec{Dir: dir, Argv: sh(`cat data.txt`)})
	if string(res.Stdout) != "present" {
		t.Errorf("stdout = %q; the command did not run in the workspace", res.Stdout)
	}
}

func TestMissingBinaryIsASandboxError(t *testing.T) {
	_, err := newTestLocal(t).Run(context.Background(), Spec{
		Dir: t.TempDir(), Argv: []string{"/nonexistent/binary"}, Limits: testLimits(),
	})
	if err == nil {
		t.Fatal("expected a sandbox error, not a result")
	}
}

func TestCPUTimeIsMeasured(t *testing.T) {
	res := run(t, Spec{Argv: sh(`i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done`)})
	if res.CPUTime <= 0 {
		t.Error("cpu time was not measured")
	}
	if res.MemorySource != judge.MemoryFromRusage {
		t.Errorf("memory source = %q, want %q", res.MemorySource, judge.MemoryFromRusage)
	}
}

func TestProcessLimitIsEnforcedWhenPrlimitExists(t *testing.T) {
	if _, err := exec.LookPath("prlimit"); err != nil {
		t.Skip("prlimit not installed")
	}
	lim := testLimits()
	lim.MaxProcesses = 8
	lim.WallTime = 5 * time.Second
	lim.CPUTime = 4 * time.Second

	before := time.Now()
	run(t, Spec{Argv: sh(`:(){ :|:& };:`), Limits: lim})
	if d := time.Since(before); d > 10*time.Second {
		t.Errorf("fork bomb was not bounded: took %v", d)
	}
}
