package judge

import "time"

// MemorySource records how a memory figure was measured: cgroup peak (nsjail) or
// per-process rusage high-water mark (local driver). The two are not comparable.
type MemorySource string

// Memory sources.
const (
	MemoryFromCgroup MemorySource = "cgroup"
	MemoryFromRusage MemorySource = "rusage"
)

// TestCaseResult is the outcome of running one testcase.
type TestCaseResult struct {
	Index  TestCaseIndex
	Status Status

	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool

	ExitCode int
	Signal   int

	CPUTime      time.Duration
	WallTime     time.Duration
	Memory       MemoryBytes
	MemorySource MemorySource

	// Message describes a citron-level failure; never program output.
	Message string
}

// CompileResult is the outcome of compiling a submission.
type CompileResult struct {
	// Skipped is true when the language has no compile step.
	Skipped  bool
	Success  bool
	Output   []byte
	Duration time.Duration
	Cached   bool // artifact reused from the compile cache
}

// SubmissionResult is the outcome of judging a whole submission.
type SubmissionResult struct {
	ID        SubmissionID
	Status    Status
	Compile   CompileResult
	TestCases []TestCaseResult
	WallTime  time.Duration
}

// Aggregate derives the submission verdict: CompilationError if compilation failed,
// SystemError if there are no results, otherwise the most severe testcase verdict.
func Aggregate(compile CompileResult, results []TestCaseResult) Status {
	if !compile.Skipped && !compile.Success {
		return StatusCompilationError
	}
	if len(results) == 0 {
		return StatusSystemError
	}
	worst := StatusAccepted
	for _, r := range results {
		if r.Status.severity() > worst.severity() {
			worst = r.Status
		}
	}
	return worst
}
