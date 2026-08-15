package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// submissionRequest is the native API: one source, many testcases, compiled once.
//
// Fields mirror the Judge0 names a client is likely to already be sending, so the
// two surfaces do not need two vocabularies.
type submissionRequest struct {
	LanguageID int    `json:"language_id"`
	Language   string `json:"language"`
	SourceCode string `json:"source_code"`

	// Testcases present means the native API; absent means the Judge0-compatible
	// single-testcase form below.
	Testcases []testcaseRequest `json:"testcases"`

	Stdin          string `json:"stdin"`
	ExpectedOutput string `json:"expected_output"`

	// Optional limits. A client may ask for less than the configured limit, never
	// more, so one caller cannot degrade the machine for everyone else.
	CPUTimeLimit  float64 `json:"cpu_time_limit"`
	WallTimeLimit float64 `json:"wall_time_limit"`
	MemoryLimit   int64   `json:"memory_limit"` // KB, as in Judge0
}

type testcaseRequest struct {
	Stdin          string `json:"stdin"`
	ExpectedOutput string `json:"expected_output"`
}

type statusDTO struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}

type compileDTO struct {
	Skipped    bool   `json:"skipped"`
	Success    bool   `json:"success"`
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Cached     bool   `json:"cached"`
}

type testcaseResultDTO struct {
	Index    int       `json:"index"`
	Status   statusDTO `json:"status"`
	Stdout   string    `json:"stdout,omitempty"`
	Stderr   string    `json:"stderr,omitempty"`
	ExitCode int       `json:"exit_code"`
	Signal   int       `json:"signal,omitempty"`

	CPUTimeMS  int64 `json:"cpu_time_ms"`
	WallTimeMS int64 `json:"wall_time_ms"`
	MemoryKB   int64 `json:"memory_kb"`
	// MemorySource says how MemoryKB was measured. The sandboxes count different
	// things, so a number without its source invites false comparisons.
	MemorySource string `json:"memory_source,omitempty"`

	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	Message         string `json:"message,omitempty"`
}

type submissionResponse struct {
	ID         string              `json:"id"`
	Status     statusDTO           `json:"status"`
	Compile    compileDTO          `json:"compile"`
	WallTimeMS int64               `json:"wall_time_ms"`
	Testcases  []testcaseResultDTO `json:"testcases"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// codec decodes request payloads. Base64 is handled once, here at the boundary;
// nothing downstream re-encodes.
type codec struct{ base64 bool }

func (c codec) decode(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if !c.base64 {
		return []byte(s), nil
	}
	// Clients differ on padding; accept both rather than reject a valid submission.
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	return b, nil
}

func (c codec) encode(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if !c.base64 {
		return string(b)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Limits are the bounds the API enforces on a request.
type Limits struct {
	Execution        judge.Limits
	MaxTestcases     int
	MaxSourceBytes   int64
	MaxTotalInput    int64
	MaxTotalOutput   int64
	SubmissionWindow time.Duration
}

var errValidation = errors.New("invalid submission")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errValidation, fmt.Sprintf(format, args...))
}

// resolveLimits applies a client's requested limits, clamped to the configured ones.
func (l Limits) resolve(req submissionRequest) judge.Limits {
	out := l.Execution
	if req.CPUTimeLimit > 0 {
		if d := time.Duration(req.CPUTimeLimit * float64(time.Second)); d < out.CPUTime {
			out.CPUTime = d
		}
	}
	if req.WallTimeLimit > 0 {
		if d := time.Duration(req.WallTimeLimit * float64(time.Second)); d < out.WallTime {
			out.WallTime = d
		}
	}
	if req.MemoryLimit > 0 {
		if m := judge.MemoryBytes(req.MemoryLimit << 10); m < out.Memory {
			out.Memory = m
		}
	}
	// A wall clock below the CPU limit would report a timeout before the CPU limit
	// could ever be reached.
	if out.WallTime < out.CPUTime {
		out.WallTime = out.CPUTime
	}
	return out
}
