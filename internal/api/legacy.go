package api

import "github.com/JustModo/judge/internal/judge"

// The legacy submission API.
//
// This is the single-testcase wire format that existing clients speak: one HTTP
// request per testcase, base64 payloads, and an integer status code. It is kept so
// clients can move to this service without being rewritten first, and it runs the
// same pipeline as the native batch API — the compile cache means such a client
// still compiles a submission only once.
//
// New clients should use the native batch API instead: it carries every testcase in
// one request, which removes the per-testcase round trip.

type legacyStatus struct {
	id   int
	desc string
}

// legacyStatuses maps internal verdicts onto the integer codes the wire format uses.
//
// Three properties are load-bearing for existing clients and are pinned by tests:
// code 3 means accepted, and the descriptions for a compile failure and a timeout
// contain the literal substrings "Compilation" and "Time Limit", which clients match
// on to categorise a submission.
var legacyStatuses = map[judge.Status]legacyStatus{
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
	// The wire format has no code for these two. They map onto the generic runtime
	// error so the code stays meaningful to a client that only understands the
	// original set, while the description still says what actually happened.
	judge.StatusMemoryLimitExceeded: {12, "Runtime Error (Memory Limit Exceeded)"},
	judge.StatusOutputLimitExceeded: {12, "Runtime Error (Output Limit Exceeded)"},
	judge.StatusSystemError:         {13, "Internal Error"},
}

func toLegacyStatus(s judge.Status) legacyStatus {
	if l, ok := legacyStatuses[s]; ok {
		return l
	}
	return legacyStatus{13, "Internal Error"}
}

// legacyResponse is the single-testcase response body. String fields are pointers so
// an absent value serialises as null rather than an empty string, which is what
// existing clients expect.
type legacyResponse struct {
	Token         string    `json:"token"`
	Status        statusDTO `json:"status"`
	Stdout        *string   `json:"stdout"`
	Stderr        *string   `json:"stderr"`
	CompileOutput *string   `json:"compile_output"`
	Message       *string   `json:"message"`
	ExitCode      *int      `json:"exit_code"`
	Time          *string   `json:"time"`
	Memory        *int64    `json:"memory"`
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
