package sandbox

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// Spec describes one execution.
type Spec struct {
	// Dir is the writable workspace and the working directory of the command.
	Dir string
	// ReadOnly are extra paths bind-mounted read-only, added to the driver's own.
	ReadOnly []string

	Argv  []string
	Stdin []byte
	// Env is passed verbatim, never merged with the worker's environment, which
	// may hold credentials.
	Env []string

	Limits judge.Limits
}

// Result is what the sandbox observed. A non-zero ExitCode is a normal result;
// Run returns an error only when the sandbox itself fails.
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

// exitStatus reports how a finished process ended; death by signal yields
// code 128+signal, following the shell convention.
func exitStatus(st *os.ProcessState) (code, signal int) {
	if st == nil {
		return 0, 0
	}
	code = st.ExitCode()
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		signal = int(ws.Signal())
		code = 128 + signal
	}
	return code, signal
}

// Sandbox is an execution driver.
type Sandbox interface {
	// Run executes spec; the error is non-nil only if the sandbox itself failed.
	Run(ctx context.Context, spec Spec) (Result, error)
	// Name identifies the driver in logs and results.
	Name() string
}
