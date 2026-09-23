package judge

// Status is the verdict for a testcase or a whole submission. Wire codes are
// defined by the API layer, not here.
type Status int

// Verdicts.
const (
	StatusAccepted Status = iota
	StatusWrongAnswer
	StatusCompilationError
	StatusTimeLimitExceeded
	StatusMemoryLimitExceeded
	StatusOutputLimitExceeded
	StatusRuntimeErrorSegfault
	StatusRuntimeErrorFileSize
	StatusRuntimeErrorFloatingPoint
	StatusRuntimeErrorAborted
	StatusRuntimeErrorNonZeroExit
	StatusRuntimeErrorOther
	StatusSystemError
)

var statusNames = map[Status]string{
	StatusAccepted:                  "Accepted",
	StatusWrongAnswer:               "Wrong Answer",
	StatusCompilationError:          "Compilation Error",
	StatusTimeLimitExceeded:         "Time Limit Exceeded",
	StatusMemoryLimitExceeded:       "Memory Limit Exceeded",
	StatusOutputLimitExceeded:       "Output Limit Exceeded",
	StatusRuntimeErrorSegfault:      "Runtime Error (SIGSEGV)",
	StatusRuntimeErrorFileSize:      "Runtime Error (SIGXFSZ)",
	StatusRuntimeErrorFloatingPoint: "Runtime Error (SIGFPE)",
	StatusRuntimeErrorAborted:       "Runtime Error (SIGABRT)",
	StatusRuntimeErrorNonZeroExit:   "Runtime Error (NZEC)",
	StatusRuntimeErrorOther:         "Runtime Error (Other)",
	StatusSystemError:               "System Error",
}

// String returns the verdict description, or "Unknown" for an undefined value.
func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return "Unknown"
}

// severity ranks statuses for Aggregate; higher wins. Order: Accepted < WrongAnswer
// < OutputLimit < MemoryLimit < runtime errors < TimeLimit < Compilation < System.
func (s Status) severity() int {
	switch s {
	case StatusAccepted:
		return 0
	case StatusWrongAnswer:
		return 1
	case StatusOutputLimitExceeded:
		return 2
	case StatusMemoryLimitExceeded:
		return 3
	case StatusTimeLimitExceeded:
		return 5
	case StatusCompilationError:
		return 6
	case StatusSystemError:
		return 7
	default: // runtime errors
		return 4
	}
}
