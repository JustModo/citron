package judge

import "time"

// MemorySource records how a memory figure was obtained. The nsjail driver reads a
// cgroup's peak (touched pages); the local dev driver reads rusage (per-process high
// water mark). The two disagree, so results say which one produced the number.
type MemorySource string

const (
	MemoryFromCgroup MemorySource = "cgroup"
	MemoryFromRusage MemorySource = "rusage"
)

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

	// Message explains a citron-level failure (sandbox error, limit hit). It never
	// carries user program output.
	Message string
}

type CompileResult struct {
	// Skipped is true for interpreted languages, which have no compile step.
	Skipped  bool
	Success  bool
	Output   []byte
	Duration time.Duration
	Cached   bool
}

type SubmissionResult struct {
	ID        SubmissionID
	Status    Status
	Compile   CompileResult
	TestCases []TestCaseResult
	WallTime  time.Duration
}

// Aggregate derives the submission verdict from the testcase verdicts. A failed
// compile short-circuits; otherwise the worst testcase wins. Every testcase runs
// regardless (§29), so this only decides the headline.
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
