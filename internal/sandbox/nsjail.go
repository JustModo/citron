package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// NsjailConfig describes the jail. The mount lists are configuration rather than
// constants so that adding a language whose runtime needs another path is a config
// change, matching how languages themselves are added.
type NsjailConfig struct {
	Path       string
	CgroupRoot string

	// ReadOnly paths are bind-mounted read-only into the jail.
	ReadOnly []string
	// Symlinks are "target:link" pairs, needed on usrmerge distributions where
	// /bin and /lib are symlinks that a bind mount would not reproduce.
	Symlinks []string
	// TmpfsMB sizes the writable /tmp. Bounding it matters: nothing else stops a
	// submission writing files until the host's disk is full.
	TmpfsMB int64
}

// Nsjail runs each execution in its own namespaces and its own cgroup.
type Nsjail struct {
	cfg NsjailConfig
	log *slog.Logger
	seq atomic.Uint64
}

func NewNsjail(cfg NsjailConfig, log *slog.Logger) (*Nsjail, error) {
	if cfg.Path == "" {
		cfg.Path = "nsjail"
	}
	if _, err := exec.LookPath(cfg.Path); err != nil {
		return nil, fmt.Errorf("nsjail: %w", err)
	}
	if err := CgroupAvailable(cfg.CgroupRoot); err != nil {
		return nil, err
	}
	if cfg.TmpfsMB <= 0 {
		cfg.TmpfsMB = 64
	}
	n := &Nsjail{cfg: cfg, log: log}
	if err := n.selfTest(); err != nil {
		return nil, err
	}
	return n, nil
}

// selfTest runs a trivial command through a real jail at startup.
//
// Without it, a mistyped flag or a missing mount path shows up as every submission
// mysteriously failing to compile. Configuration problems belong at boot, where they
// are one loud error instead of thousands of confusing verdicts.
func (n *Nsjail) selfTest() error {
	dir, err := os.MkdirTemp("", "citron-selftest-")
	if err != nil {
		return fmt.Errorf("nsjail self-test: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o777); err != nil {
		return fmt.Errorf("nsjail self-test: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := n.Run(ctx, Spec{
		Dir: dir,
		// A bare name on purpose: this is how language manifests spell their
		// commands, so the self-test must exercise the same resolution they do.
		Argv: []string{"echo", "citron-selftest"},
		Env:  []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
		Limits: judge.Limits{
			CPUTime: 5 * time.Second, WallTime: 10 * time.Second,
			Memory: 128 << 20, Stack: 8 << 20, MaxProcesses: 32,
			MaxFileSize: 1 << 20, MaxStdout: 4 << 10, MaxStderr: 8 << 10,
		},
	})
	if err != nil {
		return fmt.Errorf("nsjail self-test: %w", err)
	}
	if res.ExitCode != 0 || !bytes.Contains(res.Stdout, []byte("citron-selftest")) {
		return fmt.Errorf("nsjail self-test failed (exit %d): %s",
			res.ExitCode, bytes.TrimSpace(res.Stderr))
	}
	return nil
}

func (*Nsjail) Name() string { return "nsjail" }

const jailWorkspace = "/box"

func (n *Nsjail) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, errors.New("sandbox: empty argv")
	}

	name := fmt.Sprintf("exec-%d-%d", os.Getpid(), n.seq.Add(1))
	cg, err := newCgroup(n.cfg.CgroupRoot, name, spec.Limits.Memory, spec.Limits.MaxProcesses)
	if err != nil {
		return Result{}, err
	}
	defer cg.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := time.AfterFunc(spec.Limits.Deadline(), cancel)
	defer deadline.Stop()

	args, err := n.args(spec)
	if err != nil {
		return Result{}, err
	}

	// nsjail logs to stderr by default, which would mix jail diagnostics into the
	// submitted program's own stderr. Give it a private fd instead.
	logRead, logWrite, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: %w", err)
	}
	defer logRead.Close()

	cmd := exec.CommandContext(ctx, n.cfg.Path, args...)
	cmd.Stdin = bytes.NewReader(spec.Stdin)
	cmd.ExtraFiles = []*os.File{logWrite} // becomes fd 3 in the child

	stdout := newLimitWriter(spec.Limits.MaxStdout, cancel)
	stderr := newLimitWriter(spec.Limits.MaxStderr, cancel)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	jailLog := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(logRead, 16<<10))
		jailLog <- b
	}()

	// The process is placed into the cgroup by the kernel at clone time, so there is
	// no window in which it runs unaccounted or unbounded.
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.fd.Fd())}
	// Killing the cgroup rather than the process takes every descendant with it.
	cmd.Cancel = cg.Kill
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	wall := time.Since(start)

	// Closing the write end lets the log reader see EOF.
	_ = logWrite.Close()
	jailDiagnostics := <-jailLog

	// Whatever happened, nothing from this execution may still be running.
	_ = cg.Kill()

	res := Result{
		WallTime:        wall,
		CPUTime:         cg.CPUTime(),
		Memory:          cg.PeakMemory(),
		MemorySource:    judge.MemoryFromCgroup,
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		OutputExceeded:  stdout.Truncated() || stderr.Truncated(),
		OOMKilled:       cg.OOMKilled(),
	}

	switch {
	case runErr == nil:
	case errors.As(runErr, new(*exec.ExitError)):
	case errors.Is(runErr, exec.ErrWaitDelay):
		// Killed while a descendant still held the pipes. Expected for fork bombs.
	default:
		return Result{}, fmt.Errorf("sandbox: launching nsjail: %w: %s",
			runErr, bytes.TrimSpace(jailDiagnostics))
	}

	// A jail that never started the program is citron's failure, not the
	// submission's. Reporting it as a compile or runtime error would send a student
	// chasing a bug in their own code.
	if bytes.Contains(jailDiagnostics, []byte("Launching child process failed")) ||
		bytes.Contains(jailDiagnostics, []byte("Couldn't launch the child process")) {
		return Result{}, fmt.Errorf("sandbox: jail failed to start the process: %s",
			bytes.TrimSpace(jailDiagnostics))
	}
	// nsjail warns on every run about running unprivileged and about no_pivotroot.
	// Both are expected and documented, so this is debug detail rather than a
	// warning that would drown the log at one line per testcase.
	if len(jailDiagnostics) > 0 {
		n.log.Debug("nsjail diagnostics", "output", string(bytes.TrimSpace(jailDiagnostics)))
	}

	if st := cmd.ProcessState; st != nil {
		res.ExitCode = st.ExitCode()
		if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = int(ws.Signal())
			res.ExitCode = 128 + res.Signal
		}
	}
	// nsjail reports the jailed process's death by signal as 128+signal rather than
	// dying itself, so recover the signal from the exit code.
	if res.Signal == 0 && res.ExitCode > 128 && res.ExitCode < 165 {
		res.Signal = res.ExitCode - 128
	}

	if ctx.Err() != nil && !res.OutputExceeded {
		res.TimedOut = true
	}
	if spec.Limits.CPUTime > 0 && res.CPUTime >= spec.Limits.CPUTime {
		res.TimedOut = true
	}
	// An OOM kill arrives as SIGKILL; without the cgroup's own record it would be
	// indistinguishable from a timeout.
	if res.OOMKilled {
		res.TimedOut = false
	}

	return res, nil
}

func (n *Nsjail) args(spec Spec) ([]string, error) {
	// nsjail execve's argv[0] as given; it performs no PATH lookup. A bare command
	// name has to be resolved here. The jail mounts the same /usr the worker has, so
	// a path resolved out here is valid in there.
	argv := append([]string(nil), spec.Argv...)
	if !strings.ContainsRune(argv[0], '/') {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return nil, fmt.Errorf("sandbox: %w", err)
		}
		argv[0] = path
	}

	lim := spec.Limits
	args := []string{
		"--mode", "o",
		"--quiet",
		// Jail diagnostics go to their own descriptor so they never contaminate the
		// submitted program's stderr.
		"--log_fd", "3",
		// An unprivileged uid inside the jail's own user namespace.
		"--user", "65534",
		"--group", "65534",
		// No network of any kind: no internet, no DNS, no localhost, no metadata
		// service, no reaching citron's own API or queue.
		"--iface_no_lo",
		// The cgroup is created and owned by citron, not by nsjail.
		"--disable_clone_newcgroup",
		// pivot_root is not permitted inside a container; nsjail falls back to
		// MS_MOVE and chroot. See docs/sandbox.md.
		"--no_pivotroot",
		// Deliberately not set: RLIMIT_AS. The JVM reserves roughly a gigabyte of
		// address space whatever its heap size, so capping address space kills it
		// at startup. Memory is bounded by the cgroup, which counts touched pages.
		"--rlimit_as", "max",
		"--rlimit_fsize", strconv.FormatInt(max(lim.MaxFileSize>>20, 1), 10),
		"--rlimit_stack", strconv.FormatInt(max(int64(lim.Stack)>>20, 1), 10),
		"--rlimit_nofile", "256",
		"--time_limit", strconv.Itoa(int(lim.Deadline().Seconds()) + 1),
		"--cwd", jailWorkspace,
		"--bindmount", spec.Dir + ":" + jailWorkspace,
		// A sized tmpfs, not a plain --tmpfsmount: without a size cap a submission
		// can write files until the host's disk is full, which takes down far more
		// than the submission.
		"--mount", "none:/tmp:tmpfs:size=" + strconv.FormatInt(n.cfg.TmpfsMB<<20, 10),
	}

	for _, p := range n.cfg.ReadOnly {
		args = append(args, "--bindmount_ro", p)
	}
	for _, p := range spec.ReadOnly {
		args = append(args, "--bindmount_ro", p)
	}
	for _, s := range n.cfg.Symlinks {
		args = append(args, "--symlink", s)
	}
	for _, e := range spec.Env {
		args = append(args, "--env", e)
	}

	args = append(args, "--")
	return append(args, argv...), nil
}
