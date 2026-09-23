package run

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/JustModo/citron/internal/compare"
	"github.com/JustModo/citron/internal/judge"
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/sandbox"
	"github.com/JustModo/citron/internal/workspace"
)

// Options configures a Runner.
type Options struct {
	CompileLimits   judge.Limits
	MaxParallel     int
	SubmissionLimit time.Duration
	// Observer may be nil to disable metrics.
	Observer Observer
}

// Observer records finished submissions.
type Observer interface {
	ObserveSubmission(language string, res judge.SubmissionResult)
}

// Admitter reserves machine capacity for one execution. The runner acquires before
// every compile and testcase. A nil Admitter disables admission control (tests only).
type Admitter interface {
	Acquire(ctx context.Context, mem judge.MemoryBytes) (release func(), err error)
}

// admit reserves capacity, returning a no-op release when there is no admitter.
func (r *Runner) admit(ctx context.Context, mem judge.MemoryBytes) (func(), error) {
	if r.admitter == nil {
		return func() {}, nil
	}
	return r.admitter.Acquire(ctx, mem)
}

// Runner compiles and judges submissions.
type Runner struct {
	registry   *lang.Registry
	sandbox    sandbox.Sandbox
	workspaces *workspace.Manager
	cache      *CompileCache
	comparator compare.Comparator
	admitter   Admitter
	opts       Options
	log        *slog.Logger
}

// NewRunner returns a Runner. A non-positive opts.MaxParallel is treated as 1.
func NewRunner(
	registry *lang.Registry,
	sb sandbox.Sandbox,
	workspaces *workspace.Manager,
	cache *CompileCache,
	comparator compare.Comparator,
	admitter Admitter,
	opts Options,
	log *slog.Logger,
) *Runner {
	if opts.MaxParallel <= 0 {
		opts.MaxParallel = 1
	}
	return &Runner{
		registry: registry, sandbox: sb, workspaces: workspaces,
		cache: cache, comparator: comparator, admitter: admitter,
		opts: opts, log: log,
	}
}

// sandboxEnv is the entire environment a submission sees. Nothing is inherited
// because the worker's environment may hold credentials.
var sandboxEnv = []string{
	"PATH=/usr/local/bin:/usr/bin:/bin",
	"HOME=/tmp",
	"LANG=C.UTF-8",
	"LC_ALL=C.UTF-8",
}

// Run judges sub, bounded by Options.SubmissionLimit. Sandbox failures on a single
// testcase are reported as system errors rather than returned.
func (r *Runner) Run(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	if err := sub.Validate(); err != nil {
		return judge.SubmissionResult{}, err
	}
	if r.opts.SubmissionLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.SubmissionLimit)
		defer cancel()
	}

	start := time.Now()
	language, err := r.registry.ByID(sub.Language)
	if err != nil {
		return judge.SubmissionResult{}, err
	}

	limits := language.Limits(sub.Limits)
	source, binary := language.Files(sub.Source)

	compiled, err := r.compile(ctx, language, sub, source, binary, limits)
	if err != nil {
		return judge.SubmissionResult{}, err
	}

	result := judge.SubmissionResult{ID: sub.ID, Compile: compiled.Result}
	if !compiled.Result.Skipped && !compiled.Result.Success {
		// Testcases are not run; each inherits the compilation error.
		result.Status = judge.StatusCompilationError
		result.TestCases = make([]judge.TestCaseResult, len(sub.TestCases))
		for i, tc := range sub.TestCases {
			result.TestCases[i] = judge.TestCaseResult{
				Index: tc.Index, Status: judge.StatusCompilationError,
			}
		}
		result.WallTime = time.Since(start)
		r.observe(language.Name(), result)
		return result, nil
	}

	results, err := r.runTestCases(ctx, language, sub, compiled, source, binary, limits)
	if err != nil {
		return judge.SubmissionResult{}, err
	}
	result.TestCases = results
	result.Status = judge.Aggregate(result.Compile, results)
	result.WallTime = time.Since(start)
	r.observe(language.Name(), result)
	return result, nil
}

func (r *Runner) observe(language string, res judge.SubmissionResult) {
	if r.opts.Observer != nil {
		r.opts.Observer.ObserveSubmission(language, res)
	}
}

func (r *Runner) compile(
	ctx context.Context,
	language *lang.Language,
	sub judge.Submission,
	source, binary string,
	limits judge.Limits,
) (Entry, error) {
	argv, err := language.CompileArgv(lang.Context{
		Source: source, Binary: binary, Dir: r.workspaces.Root(),
		Limits: r.opts.CompileLimits, BaseMem: sub.Limits.Memory,
	})
	if err != nil {
		return Entry{}, err
	}
	key := Key(sub.Language, sub.Source, argv)
	return r.cache.Build(key, func(dir string) (judge.CompileResult, error) {
		if err := os.WriteFile(filepath.Join(dir, source), sub.Source, 0o644); err != nil {
			return judge.CompileResult{}, fmt.Errorf("compile: %w", err)
		}
		if len(argv) == 0 {
			// No compile step: the source is the artifact. It still goes through the
			// cache so the cache alone owns artifact lifetime.
			return judge.CompileResult{Skipped: true, Success: true}, nil
		}
		release, err := r.admit(ctx, r.opts.CompileLimits.Memory)
		if err != nil {
			return judge.CompileResult{}, fmt.Errorf("compile: %w", err)
		}
		defer release()

		started := time.Now()
		res, err := r.sandbox.Run(ctx, sandbox.Spec{
			Dir: dir, Argv: argv, Env: sandboxEnv, Limits: r.opts.CompileLimits,
		})
		if err != nil {
			return judge.CompileResult{}, fmt.Errorf("compile: %w", err)
		}
		out := res.Stderr
		if len(res.Stdout) > 0 {
			out = append(append([]byte{}, res.Stdout...), res.Stderr...)
		}
		if res.TimedOut {
			out = append(out, "\ncompilation timed out"...)
		}
		return judge.CompileResult{
			Success:  res.ExitCode == 0 && !res.TimedOut,
			Output:   out,
			Duration: time.Since(started),
		}, nil
	})
}

func (r *Runner) runTestCases(
	ctx context.Context,
	language *lang.Language,
	sub judge.Submission,
	compiled Entry,
	source, binary string,
	limits judge.Limits,
) ([]judge.TestCaseResult, error) {
	argv, err := language.RunArgv(lang.Context{
		Source: source, Binary: binary, Dir: r.workspaces.Root(),
		Limits: limits, BaseMem: sub.Limits.Memory,
	})
	if err != nil {
		return nil, err
	}

	results := make([]judge.TestCaseResult, len(sub.TestCases))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.opts.MaxParallel)

	for i, tc := range sub.TestCases {
		g.Go(func() error {
			res, err := r.runOne(gctx, compiled, argv, limits, tc)
			if err != nil {
				// A sandbox failure is a system error for this testcase only.
				r.log.Error("testcase execution failed",
					"submission", sub.ID, "testcase", tc.Index, "error", err)
				res = judge.TestCaseResult{
					Index: tc.Index, Status: judge.StatusSystemError,
					Message: "sandbox failure",
				}
			}
			results[i] = res
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *Runner) runOne(
	ctx context.Context,
	compiled Entry,
	argv []string,
	limits judge.Limits,
	tc judge.TestCase,
) (judge.TestCaseResult, error) {
	release, err := r.admit(ctx, limits.Memory)
	if err != nil {
		return judge.TestCaseResult{}, err
	}
	defer release()

	ws, err := r.workspaces.New("tc")
	if err != nil {
		return judge.TestCaseResult{}, err
	}
	defer ws.Close()

	if err := CopyInto(compiled, ws.Dir); err != nil {
		return judge.TestCaseResult{}, err
	}

	// The artifact is copied, not bind-mounted, to keep the cache layout out of the jail.
	res, err := r.sandbox.Run(ctx, sandbox.Spec{
		Dir: ws.Dir, Argv: argv, Stdin: tc.Stdin, Env: sandboxEnv, Limits: limits,
	})
	if err != nil {
		return judge.TestCaseResult{}, err
	}

	out := judge.TestCaseResult{
		Index:           tc.Index,
		Stdout:          res.Stdout,
		Stderr:          res.Stderr,
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		ExitCode:        res.ExitCode,
		Signal:          res.Signal,
		CPUTime:         res.CPUTime,
		WallTime:        res.WallTime,
		Memory:          res.Memory,
		MemorySource:    res.MemorySource,
	}
	out.Status = r.verdict(res, tc)
	return out, nil
}

// verdict maps a sandbox result to a status. Limit violations take precedence over
// output comparison.
func (r *Runner) verdict(res sandbox.Result, tc judge.TestCase) judge.Status {
	switch {
	case res.TimedOut:
		return judge.StatusTimeLimitExceeded
	case res.OOMKilled:
		return judge.StatusMemoryLimitExceeded
	case res.OutputExceeded:
		return judge.StatusOutputLimitExceeded
	}
	if res.Killed() {
		switch syscall.Signal(res.Signal) {
		case syscall.SIGSEGV:
			return judge.StatusRuntimeErrorSegfault
		case syscall.SIGXFSZ:
			return judge.StatusRuntimeErrorFileSize
		case syscall.SIGFPE:
			return judge.StatusRuntimeErrorFloatingPoint
		case syscall.SIGABRT:
			return judge.StatusRuntimeErrorAborted
		case syscall.SIGXCPU, syscall.SIGKILL:
			// SIGXCPU: kernel CPU limit. SIGKILL: sandbox wall-clock limit.
			return judge.StatusTimeLimitExceeded
		default:
			return judge.StatusRuntimeErrorOther
		}
	}
	if res.ExitCode != 0 {
		return judge.StatusRuntimeErrorNonZeroExit
	}
	if r.comparator.Equal(tc.ExpectedOutput, res.Stdout) {
		return judge.StatusAccepted
	}
	return judge.StatusWrongAnswer
}
