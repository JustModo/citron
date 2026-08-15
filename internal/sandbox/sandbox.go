// Package sandbox runs one command under resource limits and returns what happened.
//
// A Sandbox reports facts, not verdicts: exit codes, signals, timings and whether a
// limit was hit. Turning those into Accepted or Runtime Error is the caller's job, so
// the verdict rules stay in one place and every driver behaves identically.
package sandbox

import (
	"context"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// Spec is one execution.
type Spec struct {
	// Dir is the writable workspace and the working directory of the command.
	Dir string
	// ReadOnly are extra paths the command may read, such as a shared compiled
	// artifact. Everything else must be unreachable.
	ReadOnly []string

	Argv  []string
	Stdin []byte
	// Env is passed verbatim. It is never derived from the judge's own environment:
	// the worker's variables may hold credentials the submission must not see.
	Env []string

	Limits judge.Limits
}

// Result is what the sandbox observed. A non-zero ExitCode is a normal result, not
// an error; error is reserved for the sandbox itself failing.
type Result struct {
	ExitCode int
	Signal   int

	CPUTime  time.Duration
	WallTime time.Duration

	Memory       judge.MemoryBytes
	MemorySource judge.MemorySource

	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool

	TimedOut       bool
	OOMKilled      bool
	OutputExceeded bool
}

// Killed reports whether the process died from a signal rather than exiting.
func (r Result) Killed() bool { return r.Signal != 0 }

type Sandbox interface {
	Run(ctx context.Context, spec Spec) (Result, error)
	// Name identifies the driver in logs and results.
	Name() string
}
