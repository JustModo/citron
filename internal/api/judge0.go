package api

import "github.com/JustModo/judge/internal/judge"

// Judge0 compatibility.
//
// The existing consumer sends one request per testcase to
// `POST /submissions?base64_encoded=true&wait=true` and reads only status.id,
// status.description, stdout, stderr and compile_output. Reproducing that exactly
// means it can move to this judge by changing a URL, and the compile cache means it
// gets the compile-once benefit without changing anything at all.

type judge0Status struct {
	id   int
	desc string
}

// Judge0's status ids. Two properties are load-bearing beyond the numbers: id 3 means
// accepted, and consumers branch on the descriptions containing "Compilation" and
// "Time Limit". Both are pinned by tests.
var judge0Statuses = map[judge.Status]judge0Status{
	judge.StatusAccepted:                  {3, "Accepted"},
	judge.StatusWrongAnswer:               {4, "Wrong Answer"},
	judge.StatusTimeLimitExceeded:         {5, "Time Limit Exceeded"},
	judge.StatusCompilationError:          {6, "Compilation Error"},
	judge.StatusRuntimeErrorSegfault:      {7, "Runtime Error (SIGSEGV)"},
	judge.StatusRuntimeErrorFileSize:      {8, "Runtime Error (SIGXFSZ)"},
	judge.StatusRuntimeErrorFloatingPoint: {9, "Runtime Error (SIGFPE)"},
	judge.StatusRuntimeErrorAborted:       {10, "Runtime Error (SIGABRT)"},
	judge.StatusRuntimeErrorNonZeroExit:   {11, "Runtime Error (NZEC)"},
	judge.StatusRuntimeErrorOther:         {12, "Runtime Error (Other)"},
	// Judge0 has no status for these. They map onto the generic runtime error so the
	// id stays meaningful, while the description still says what actually happened.
	judge.StatusMemoryLimitExceeded: {12, "Runtime Error (Memory Limit Exceeded)"},
	judge.StatusOutputLimitExceeded: {12, "Runtime Error (Output Limit Exceeded)"},
	judge.StatusSystemError:         {13, "Internal Error"},
}

func toJudge0Status(s judge.Status) judge0Status {
	if j, ok := judge0Statuses[s]; ok {
		return j
	}
	return judge0Status{13, "Internal Error"}
}

// judge0Response matches Judge0's submission body. String fields are pointers so an
// absent value serialises as null rather than "", which is what Judge0 does.
type judge0Response struct {
	Token         string       `json:"token"`
	Status        judge0Status `json:"-"`
	StatusOut     statusDTO    `json:"status"`
	Stdout        *string      `json:"stdout"`
	Stderr        *string      `json:"stderr"`
	CompileOutput *string      `json:"compile_output"`
	Message       *string      `json:"message"`
	ExitCode      *int         `json:"exit_code"`
	Time          *string      `json:"time"`
	Memory        *int64       `json:"memory"`
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
