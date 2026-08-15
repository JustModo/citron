package judge

// Status is the verdict for a testcase or a whole submission. It is the judge's own
// vocabulary; the integer codes used on the wire live in the API layer so the domain
// never depends on a transport format.
type Status int

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

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return "Unknown"
}

func (s Status) IsRuntimeError() bool {
	return s >= StatusRuntimeErrorSegfault && s <= StatusRuntimeErrorOther
}

// severity orders statuses for aggregation: the worst testcase decides the
// submission verdict. Higher wins.
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
