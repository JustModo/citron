package judge

import (
	"strings"
	"testing"
	"time"
)

func TestAggregate(t *testing.T) {
	ok := CompileResult{Success: true}
	tests := []struct {
		name    string
		compile CompileResult
		results []Status
		want    Status
	}{
		{"all accepted", ok, []Status{StatusAccepted, StatusAccepted}, StatusAccepted},
		{"compile failure wins", CompileResult{Success: false}, nil, StatusCompilationError},
		{"skipped compile is not a failure", CompileResult{Skipped: true}, []Status{StatusAccepted}, StatusAccepted},
		{"worst testcase wins", ok, []Status{StatusAccepted, StatusWrongAnswer, StatusTimeLimitExceeded}, StatusTimeLimitExceeded},
		{"runtime beats wrong answer", ok, []Status{StatusWrongAnswer, StatusRuntimeErrorSegfault}, StatusRuntimeErrorSegfault},
		{"tle beats runtime", ok, []Status{StatusRuntimeErrorSegfault, StatusTimeLimitExceeded}, StatusTimeLimitExceeded},
		{"system error beats everything", ok, []Status{StatusTimeLimitExceeded, StatusSystemError}, StatusSystemError},
		{"no testcases is a system error", ok, nil, StatusSystemError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := make([]TestCaseResult, len(tt.results))
			for i, s := range tt.results {
				rs[i] = TestCaseResult{Index: TestCaseIndex(i), Status: s}
			}
			if got := Aggregate(tt.compile, rs); got != tt.want {
				t.Errorf("Aggregate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The consumer branches on substrings of the status description, not on ids alone.
// Renaming these breaks it silently, so pin them here.
func TestStatusDescriptionsConsumersDependOn(t *testing.T) {
	if !strings.Contains(StatusCompilationError.String(), "Compilation") {
		t.Errorf("compilation status must contain %q, got %q", "Compilation", StatusCompilationError)
	}
	if !strings.Contains(StatusTimeLimitExceeded.String(), "Time Limit") {
		t.Errorf("tle status must contain %q, got %q", "Time Limit", StatusTimeLimitExceeded)
	}
}

func TestStatusStringIsTotal(t *testing.T) {
	for s := StatusAccepted; s <= StatusSystemError; s++ {
		if s.String() == "Unknown" {
			t.Errorf("status %d has no name", s)
		}
	}
	if Status(999).String() != "Unknown" {
		t.Error("out-of-range status should be Unknown")
	}
}

func TestLimitsValidate(t *testing.T) {
	valid := Limits{
		CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
		Memory: 256 << 20, Stack: 64 << 20,
		MaxProcesses: 32, MaxFileSize: 16 << 20,
		MaxStdout: 1 << 20, MaxStderr: 1 << 20,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}

	tests := []struct {
		name  string
		mutID func(*Limits)
	}{
		{"zero cpu time", func(l *Limits) { l.CPUTime = 0 }},
		{"zero wall time", func(l *Limits) { l.WallTime = 0 }},
		{"wall below cpu", func(l *Limits) { l.WallTime = time.Second }},
		{"zero memory", func(l *Limits) { l.Memory = 0 }},
		{"zero processes", func(l *Limits) { l.MaxProcesses = 0 }},
		{"zero stdout", func(l *Limits) { l.MaxStdout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := valid
			tt.mutID(&l)
			if err := l.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestSubmissionValidate(t *testing.T) {
	valid := Submission{
		ID: "s1", Source: []byte("int main(){}"),
		TestCases: []TestCase{{Index: 0}},
		Limits: Limits{
			CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
			Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
			MaxFileSize: 1 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}
	for _, tt := range []struct {
		name string
		mut  func(*Submission)
	}{
		{"no id", func(s *Submission) { s.ID = "" }},
		{"no source", func(s *Submission) { s.Source = nil }},
		{"no testcases", func(s *Submission) { s.TestCases = nil }},
		{"bad limits", func(s *Submission) { s.Limits.Memory = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mut(&s)
			if err := s.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}
