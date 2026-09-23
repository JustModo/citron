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
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// NsjailConfig configures the jail. Mount lists are configurable so a language
// whose runtime needs another path requires no code change.
type NsjailConfig struct {
	// Path is the nsjail binary; defaults to "nsjail" on PATH.
	Path string
	// CgroupRoot is the delegated cgroup directory executions are created under.
	CgroupRoot string

	// ReadOnly paths are bind-mounted read-only into the jail.
	ReadOnly []string
	// Symlinks are "target:link" pairs, needed on usrmerge distributions where
	// /bin and /lib are symlinks that a bind mount would not reproduce.
	Symlinks []string
	// TmpfsMB sizes the writable /tmp (default 64); it is the only bound on files
	// a submission can write there.
	TmpfsMB int64
	// NoUserNamespace keeps the jail in the container's user namespace. The kernel
	// refuses a fresh /proc in a new user namespace while the container's /proc has
	// masked paths, which some platforms (e.g. Docker Swarm) cannot unmask. The
	// submission still runs as an unprivileged uid without capabilities, and
	// no_new_privs makes setuid binaries inert.
	NoUserNamespace bool
}

// Nsjail runs each execution in its own namespaces and its own cgroup.
type Nsjail struct {
	cfg     NsjailConfig
	cgroups cgroupManager
	log     *slog.Logger
	seq     atomic.Uint64
}

// NewNsjail locates nsjail, selects a cgroup backend and runs a self-test, so
// misconfiguration fails at startup.
func NewNsjail(cfg NsjailConfig, log *slog.Logger) (*Nsjail, error) {
	if cfg.Path == "" {
		cfg.Path = "nsjail"
	}
	if _, err := exec.LookPath(cfg.Path); err != nil {
		return nil, fmt.Errorf("nsjail: %w", err)
	}
	cgroups, err := newCgroupManager(cfg.CgroupRoot)
	if err != nil {
		return nil, err
	}
	log.Info("cgroup backend", "version", cgroups.Version(), "root", cfg.CgroupRoot)
	if cfg.TmpfsMB <= 0 {
		cfg.TmpfsMB = 64
	}
	n := &Nsjail{cfg: cfg, cgroups: cgroups, log: log}
	if err := n.selfTest(); err != nil {
		return nil, err
	}
	return n, nil
}

// selfTest runs a trivial command through a real jail, so a bad flag or missing
// mount fails at startup instead of as failed submissions.
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
		// A bare name, as language manifests use, to exercise PATH resolution.
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

// Name returns "nsjail".
func (*Nsjail) Name() string { return "nsjail" }

// jailWorkspace is where spec.Dir is mounted inside the jail.
const jailWorkspace = "/box"

// Run executes spec inside a fresh jail and cgroup and reports what happened.
func (n *Nsjail) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, errors.New("sandbox: empty argv")
	}

	name := fmt.Sprintf("exec-%d-%d", os.Getpid(), n.seq.Add(1))
	cg, err := n.cgroups.New(name, spec.Limits.Memory, spec.Limits.MaxProcesses)
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

	// Give nsjail a private log fd so its diagnostics stay out of the program's stderr.
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

	// On v2 the child is placed at clone time; on v1 runJail places it after Start.
	cg.Prepare(cmd)
	// Killing the cgroup rather than the process takes every descendant with it.
	cmd.Cancel = cg.Kill
	// Bounds Wait when descendants still hold the inherited output pipes.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	runErr := runJail(cmd, cg)
	wall := time.Since(start)

	// Closing the write end lets the log reader see EOF.
	_ = logWrite.Close()
	jailDiagnostics := <-jailLog

	// Ensure nothing from this execution survives, however it ended.
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

	// A jail that never launched the program is an infrastructure error, not a
	// compile or runtime error in the submission.
	if bytes.Contains(jailDiagnostics, []byte("Launching child process failed")) ||
		bytes.Contains(jailDiagnostics, []byte("Couldn't launch the child process")) {
		return Result{}, fmt.Errorf("sandbox: jail failed to start the process: %s",
			bytes.TrimSpace(jailDiagnostics))
	}
	// nsjail warns on every run about running unprivileged and no_pivotroot; both
	// are expected, so log at debug rather than once per testcase at warn.
	if len(jailDiagnostics) > 0 {
		n.log.Debug("nsjail diagnostics", "output", string(bytes.TrimSpace(jailDiagnostics)))
	}

	res.ExitCode, res.Signal = exitStatus(cmd.ProcessState)
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

// runJail starts the jail, places it in the cgroup and waits for it.
func runJail(cmd *exec.Cmd, cg cgroup) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	// An unplaced process would run without limits.
	if err := cg.Place(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}

// args builds the nsjail command line for spec.
func (n *Nsjail) args(spec Spec) ([]string, error) {
	// nsjail does no PATH lookup. The jail mounts the host's /usr, so a path
	// resolved here is valid inside.
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
		"--log_fd", "3", // the private log pipe from Run
		// nobody:nogroup.
		"--user", "65534",
		"--group", "65534",
		// No network interfaces at all, not even loopback.
		"--iface_no_lo",
		// citron creates and owns the cgroup.
		"--disable_clone_newcgroup",
		// pivot_root is not permitted inside a container; nsjail falls back to
		// MS_MOVE and chroot.
		"--no_pivotroot",
		// No RLIMIT_AS: the JVM reserves ~1 GB of address space regardless of heap
		// size. Memory is bounded by the cgroup, which counts touched pages.
		"--rlimit_as", "max",
		"--rlimit_fsize", strconv.FormatInt(max(lim.MaxFileSize>>20, 1), 10),
		"--rlimit_stack", strconv.FormatInt(max(int64(lim.Stack)>>20, 1), 10),
		"--rlimit_nofile", "256",
		"--time_limit", strconv.Itoa(int(lim.Deadline().Seconds()) + 1),
		"--cwd", jailWorkspace,
		"--bindmount", spec.Dir + ":" + jailWorkspace,
		// A sized tmpfs rather than --tmpfsmount, so /tmp writes stay bounded.
		"--mount", "none:/tmp:tmpfs:size=" + strconv.FormatInt(n.cfg.TmpfsMB<<20, 10),
	}

	if n.cfg.NoUserNamespace {
		args = append(args, "--disable_clone_newuser")
	}
	for _, p := range n.cfg.ReadOnly {
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
