// Package run turns a submission into results: compile once, execute every testcase,
// compare, aggregate.
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

	"github.com/JustModo/judge/internal/compare"
	"github.com/JustModo/judge/internal/judge"
	"github.com/JustModo/judge/internal/lang"
	"github.com/JustModo/judge/internal/sandbox"
	"github.com/JustModo/judge/internal/workspace"
)

// Options are the knobs the runner needs, already resolved from configuration.
type Options struct {
	CompileLimits   judge.Limits
	MaxParallel     int
	SubmissionLimit time.Duration
}

// Admitter reserves machine capacity for one execution. The runner asks before every
// compile and every testcase, so the judge never starts work the machine cannot hold.
// A nil Admitter means no admission control, which is only appropriate in tests.
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

// sandboxEnv is the entire environment a submission sees. Nothing is inherited: the
// worker's variables can hold credentials and internal addresses.
var sandboxEnv = []string{
	"PATH=/usr/local/bin:/usr/bin:/bin",
	"HOME=/tmp",
	"LANG=C.UTF-8",
	"LC_ALL=C.UTF-8",
}

func (r *Runner) Run(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	if err := sub.Validate(); err != nil {
		return judge.SubmissionResult{}, err
	}
	// The judge must answer before its client gives up waiting, whatever else is
	// happening on the machine.
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
		// Compilation failed: every testcase shares the verdict, and none are run.
		result.Status = judge.StatusCompilationError
		result.TestCases = make([]judge.TestCaseResult, len(sub.TestCases))
		for i, tc := range sub.TestCases {
			result.TestCases[i] = judge.TestCaseResult{
				Index: tc.Index, Status: judge.StatusCompilationError,
			}
		}
		result.WallTime = time.Since(start)
		return result, nil
	}

	results, err := r.runTestCases(ctx, language, sub, compiled, source, binary, limits)
	if err != nil {
		return judge.SubmissionResult{}, err
	}
	result.TestCases = results
	result.Status = judge.Aggregate(result.Compile, results)
	result.WallTime = time.Since(start)
	return result, nil
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
			// No compile step: the source itself is the artifact. It still goes
			// through the cache so artifact lifetime has exactly one owner.
			return judge.CompileResult{Skipped: true, Success: true}, nil
		}
		// Compilers are the most memory-hungry thing the judge runs; they reserve
		// capacity like any other execution.
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
				// A sandbox failure is the judge's fault, not the submission's. It
				// fails this testcase without abandoning the others.
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

	// The artifact is copied in above, so it is NOT also bind-mounted: mounting it
	// would expose the cache's directory layout inside the workspace for no gain.
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

// verdict turns what the sandbox observed into a status. Limit violations outrank a
// wrong answer: a program killed at its memory ceiling has not answered anything.
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
			// The kernel kills on CPU overrun; the sandbox kills on wall clock.
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
