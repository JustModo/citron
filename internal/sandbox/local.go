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

	"github.com/JustModo/citron/internal/judge"
)

// Local runs commands on the host with rlimits, a private process group and bounded
// output. It provides no isolation (no namespaces, filesystem or network
// restriction) and is intended for development and unprivileged tests only.
type Local struct {
	log *slog.Logger
	// prlimit is the path to prlimit(1), or "" if absent. Go cannot set rlimits on
	// a child directly, so limits are applied by exec'ing through it.
	prlimit string
}

// NewLocal returns a Local driver, warning if prlimit is unavailable.
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

// Name returns "local".
func (*Local) Name() string { return "local" }

// Run executes spec on the host and reports what happened.
func (l *Local) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, errors.New("sandbox: empty argv")
	}
	// Resolve before wrapping in prlimit, or a missing binary becomes the wrapper's
	// exit 127 and looks like a runtime error in the submission.
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

	// A private process group lets the kill reach grandchildren.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	// Descendants that inherited the pipes keep them open after SIGKILL; without a
	// WaitDelay, Wait blocks on the copier forever.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)

	// A normal exit can still leave descendants running.
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
		// A non-zero exit is a result, not a sandbox failure.
	case errors.Is(err, exec.ErrWaitDelay):
		// A killed descendant still held the pipes; expected for fork bombs.
	default:
		return Result{}, fmt.Errorf("sandbox: starting %q: %w", argv[0], err)
	}

	res.ExitCode, res.Signal = exitStatus(cmd.ProcessState)
	if st := cmd.ProcessState; st != nil {
		if ru, ok := st.SysUsage().(*syscall.Rusage); ok {
			res.CPUTime = time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
			res.Memory = judge.MemoryBytes(ru.Maxrss) << 10 // Linux reports KiB
		}
	}

	// An output-limit kill also cancels ctx, so it must not count as a timeout.
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
		// Omitted: --nproc, because RLIMIT_NPROC counts every process of the uid
		// machine-wide and breaks unrelated forks; --as, because the JVM reserves
		// ~1 GB of address space at startup. nsjail uses the cgroup for both.
		"--",
	}
	return append(out, argv...)
}

// resolveCommand checks that name is executable. A path is relative to dir, the
// command's working directory; a bare name is looked up on PATH.
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

// killGroup SIGKILLs the process group led by pid.
func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
