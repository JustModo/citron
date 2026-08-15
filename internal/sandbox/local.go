package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// Local runs commands directly on the host with rlimits, a private process group and
// bounded output.
//
// It does NOT isolate anything: no namespaces, no filesystem restriction, no network
// restriction. It exists so the judge runs on a developer machine without nsjail, and
// so the Sandbox contract is exercised by tests that need no privileges. The
// composition root refuses to select it unless the operator has explicitly opted in.
type Local struct {
	log *slog.Logger
	// prlimit is the path to util-linux prlimit, or "" when unavailable. Go cannot
	// set rlimits on a child directly, so the limits are applied by exec'ing through
	// prlimit when it is present.
	prlimit string
}

func NewLocal(log *slog.Logger) *Local {
	l := &Local{log: log}
	if p, err := exec.LookPath("prlimit"); err == nil {
		l.prlimit = p
	} else {
		log.Warn("prlimit not found; local sandbox will not enforce cpu, file size or process limits")
	}
	log.Warn("local sandbox driver selected: submitted code is NOT isolated from this machine")
	return l
}

func (*Local) Name() string { return "local" }

func (l *Local) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, errors.New("sandbox: empty argv")
	}
	// Resolve before wrapping in prlimit. Otherwise a missing toolchain surfaces as
	// the wrapper's exit 127, which reads as a runtime error in the submitted code.
	if err := resolveCommand(spec.Dir, spec.Argv[0]); err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := time.AfterFunc(spec.Limits.Deadline(), cancel)
	defer deadline.Stop()

	argv := l.withLimits(spec.Limits, spec.Argv)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env // never os.Environ(): the worker's env may hold credentials
	cmd.Stdin = bytes.NewReader(spec.Stdin)

	stdout := newLimitWriter(spec.Limits.MaxStdout, cancel)
	stderr := newLimitWriter(spec.Limits.MaxStderr, cancel)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// A private process group is what makes the kill below reach grandchildren; a
	// program that forks and exits would otherwise leave its children running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	// Even after SIGKILL, a child that inherited the pipes keeps them open. Without
	// this, Wait blocks on the copier forever.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)

	// Belt and braces: kill the group again in case the process exited normally but
	// left descendants holding the workspace open.
	if cmd.Process != nil {
		_ = killGroup(cmd.Process.Pid)
	}

	res := Result{
		WallTime:        wall,
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		OutputExceeded:  stdout.Truncated() || stderr.Truncated(),
		MemorySource:    judge.MemoryFromRusage,
	}

	switch {
	case err == nil:
	case errors.As(err, new(*exec.ExitError)):
		// A non-zero exit is a result, not a failure of the sandbox.
	case errors.Is(err, exec.ErrWaitDelay):
		// The process is gone but a descendant still held the pipes open. Expected
		// whenever a fork bomb or a backgrounding program is killed.
	default:
		return Result{}, fmt.Errorf("sandbox: starting %q: %w", argv[0], err)
	}

	if st := cmd.ProcessState; st != nil {
		res.ExitCode = st.ExitCode()
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = int(ws.Signal())
			res.ExitCode = 128 + res.Signal
		}
		if ru, ok := st.SysUsage().(*syscall.Rusage); ok {
			res.CPUTime = time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
			res.Memory = judge.MemoryBytes(ru.Maxrss) << 10 // Linux reports KiB
		}
	}

	// Cause tells a wall-clock kill apart from an output-bomb kill; both arrive as a
	// cancelled context and a SIGKILLed process.
	if ctx.Err() != nil && !res.OutputExceeded {
		res.TimedOut = true
	}
	if res.CPUTime >= spec.Limits.CPUTime {
		res.TimedOut = true
	}

	return res, nil
}

// withLimits wraps argv in prlimit so the child gets rlimits Go cannot set directly.
func (l *Local) withLimits(lim judge.Limits, argv []string) []string {
	if l.prlimit == "" {
		return argv
	}
	cpu := int64(lim.CPUTime.Seconds()) + int64(lim.CPUExtraTime.Seconds()) + 1
	out := []string{
		l.prlimit,
		"--cpu=" + strconv.FormatInt(cpu, 10),
		"--fsize=" + strconv.FormatInt(lim.MaxFileSize, 10),
		"--stack=" + strconv.FormatInt(int64(lim.Stack), 10),
		// Deliberately absent:
		//   --nproc: RLIMIT_NPROC counts every process owned by the uid on the whole
		//     machine, not the ones in this execution. On a shared uid it makes
		//     unrelated forks fail, including the compiler's. Process count is
		//     bounded by the cgroup's pids.max in the real sandbox.
		//   --as: the JVM reserves ~1 GB of address space regardless of heap size,
		//     so an address-space cap kills it at startup. Memory is bounded by the
		//     cgroup's memory.max in the real sandbox.
		"--",
	}
	return append(out, argv...)
}

// resolveCommand reports whether argv[0] can actually be executed. A path is taken
// relative to the workspace, matching the command's working directory; a bare name is
// looked up on PATH.
func resolveCommand(dir, name string) error {
	if !strings.ContainsRune(name, '/') {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		return nil
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("sandbox: %s is not executable", name)
	}
	return nil
}

func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
