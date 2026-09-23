package judge

import (
	"errors"
	"fmt"
	"time"
)

// Identifier and unit types.
type (
	SubmissionID  string
	LanguageID    int
	TestCaseIndex int
	MemoryBytes   int64
)

// MB returns m in whole mebibytes, rounded down.
func (m MemoryBytes) MB() int64 { return int64(m) / (1 << 20) }

// Limits bounds a single execution. A zero limit is rejected by Validate rather
// than treated as unbounded.
type Limits struct {
	CPUTime      time.Duration
	CPUExtraTime time.Duration // grace after WallTime for the sandbox to kill an overrun
	WallTime     time.Duration
	Memory       MemoryBytes
	Stack        MemoryBytes
	MaxProcesses int
	MaxFileSize  int64 // bytes
	MaxStdout    int64 // bytes
	MaxStderr    int64 // bytes
}

// ErrInvalidLimits is wrapped by every Limits validation error.
var ErrInvalidLimits = errors.New("invalid limits")

// Validate reports the first limit that is unset or inconsistent.
func (l Limits) Validate() error {
	for _, c := range []struct {
		ok   bool
		what string
	}{
		{l.CPUTime > 0, "cpu_time"},
		{l.WallTime > 0, "wall_time"},
		{l.WallTime >= l.CPUTime, "wall_time must be >= cpu_time"},
		{l.Memory > 0, "memory"},
		{l.Stack > 0, "stack"},
		{l.MaxProcesses > 0, "max_processes"},
		{l.MaxFileSize > 0, "max_file_size"},
		{l.MaxStdout > 0, "max_stdout"},
		{l.MaxStderr > 0, "max_stderr"},
	} {
		if !c.ok {
			return fmt.Errorf("%w: %s", ErrInvalidLimits, c.what)
		}
	}
	return nil
}

// Deadline returns the hard wall-clock ceiling for one execution, including the
// grace the sandbox needs to kill an overrunning process tree.
func (l Limits) Deadline() time.Duration { return l.WallTime + l.CPUExtraTime }

// TestCase is one input and its expected output.
type TestCase struct {
	Index          TestCaseIndex
	Stdin          []byte
	ExpectedOutput []byte
}

// Submission is one source file run against its testcases. It is compiled once;
// each testcase runs in a fresh workspace.
type Submission struct {
	ID        SubmissionID
	Language  LanguageID
	Source    []byte
	TestCases []TestCase
	Limits    Limits
}

// Validate reports whether the submission is complete and its limits are valid.
func (s Submission) Validate() error {
	switch {
	case s.ID == "":
		return errors.New("submission: missing id")
	case len(s.Source) == 0:
		return errors.New("submission: empty source")
	case len(s.TestCases) == 0:
		return errors.New("submission: no testcases")
	}
	return s.Limits.Validate()
}
