package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// submissionRequest is one source and its testcases, compiled once.
type submissionRequest struct {
	LanguageID int               `json:"language_id"`
	Language   string            `json:"language"`
	SourceCode string            `json:"source_code"`
	Testcases  []testcaseRequest `json:"testcases"`

	// Optional limits, clamped to the configured maximums.
	CPUTimeLimit  float64 `json:"cpu_time_limit"`
	WallTimeLimit float64 `json:"wall_time_limit"`
	MemoryLimit   int64   `json:"memory_limit"` // KB
}

type testcaseRequest struct {
	Stdin          string `json:"stdin"`
	ExpectedOutput string `json:"expected_output"`
}

type statusDTO struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
}

// statusCodes are the wire status ids. They are part of the API contract and must not
// be renumbered. Memory and output limits share 12 with the generic runtime error and
// are distinguished by description.
var statusCodes = map[judge.Status]int{
	judge.StatusAccepted:                  3,
	judge.StatusWrongAnswer:               4,
	judge.StatusTimeLimitExceeded:         5,
	judge.StatusCompilationError:          6,
	judge.StatusRuntimeErrorSegfault:      7,
	judge.StatusRuntimeErrorFileSize:      8,
	judge.StatusRuntimeErrorFloatingPoint: 9,
	judge.StatusRuntimeErrorAborted:       10,
	judge.StatusRuntimeErrorNonZeroExit:   11,
	judge.StatusRuntimeErrorOther:         12,
	judge.StatusMemoryLimitExceeded:       12,
	judge.StatusOutputLimitExceeded:       12,
	judge.StatusSystemError:               13,
}

func toStatusDTO(s judge.Status) statusDTO {
	id, ok := statusCodes[s]
	if !ok {
		id = statusCodes[judge.StatusSystemError]
	}
	return statusDTO{id, s.String()}
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
	// MemorySource names how MemoryKB was measured; sandboxes measure differently.
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

// codec converts payloads between the wire form (optionally base64) and raw bytes.
type codec struct{ base64 bool }

func (c codec) decode(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if !c.base64 {
		return []byte(s), nil
	}
	// Accept both padded and unpadded base64.
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
	Execution      judge.Limits
	MaxTestcases   int
	MaxSourceBytes int64
	MaxTotalInput  int64
	MaxTotalOutput int64
}

var errValidation = errors.New("invalid submission")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errValidation, fmt.Sprintf(format, args...))
}

// resolve applies a client's requested limits, clamped to the configured ones.
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
	// Wall time below CPU time would make the CPU limit unreachable.
	if out.WallTime < out.CPUTime {
		out.WallTime = out.CPUTime
	}
	return out
}
