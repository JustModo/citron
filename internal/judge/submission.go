// Package judge holds the domain model. It imports nothing outside the standard
// library and knows nothing about HTTP, Redis, Docker, nsjail or the filesystem.
package judge

import (
	"errors"
	"fmt"
	"time"
)

type (
	SubmissionID  string
	LanguageID    int
	TestCaseIndex int
	MemoryBytes   int64
)

func (m MemoryBytes) MB() int64 { return int64(m) / (1 << 20) }

// Limits bounds a single execution. Every field is enforced; a zero value means
// "not configured" and is rejected by Validate rather than silently unbounded.
type Limits struct {
	CPUTime      time.Duration
	CPUExtraTime time.Duration
	WallTime     time.Duration
	Memory       MemoryBytes
	Stack        MemoryBytes
	MaxProcesses int
	MaxFileSize  int64
	MaxStdout    int64
	MaxStderr    int64
}

var ErrInvalidLimits = errors.New("invalid limits")

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

// Deadline is the hard wall-clock ceiling for one execution, including the grace
// the sandbox needs to notice a CPU overrun and kill the process tree.
func (l Limits) Deadline() time.Duration { return l.WallTime + l.CPUExtraTime }

type TestCase struct {
	Index          TestCaseIndex
	Stdin          []byte
	ExpectedOutput []byte
}

// Submission is one source file run against N testcases. Compilation happens once
// for the whole submission; every testcase gets a fresh writable workspace.
type Submission struct {
	ID        SubmissionID
	Language  LanguageID
	Source    []byte
	TestCases []TestCase
	Limits    Limits
}

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
