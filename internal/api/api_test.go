package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JustModo/judge/internal/judge"
	"github.com/JustModo/judge/internal/lang"
	"github.com/JustModo/judge/internal/sched"
)

type fakeSubmitter struct {
	got    judge.Submission
	result judge.SubmissionResult
	err    error
}

func (f *fakeSubmitter) Submit(_ context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	f.got = sub
	if f.err != nil {
		return judge.SubmissionResult{}, f.err
	}
	res := f.result
	res.ID = sub.ID
	if res.TestCases == nil {
		res.TestCases = make([]judge.TestCaseResult, len(sub.TestCases))
		for i := range sub.TestCases {
			res.TestCases[i] = judge.TestCaseResult{
				Index: judge.TestCaseIndex(i), Status: judge.StatusAccepted,
			}
		}
	}
	return res, nil
}

func newTestServer(t *testing.T, sub *fakeSubmitter) http.Handler {
	t.Helper()
	registry, err := lang.LoadRegistry(filepath.Join("..", "..", "configs", "languages.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(Options{
		Submitter: sub,
		Registry:  registry,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Limits: Limits{
			Execution: judge.Limits{
				CPUTime: 2 * time.Second, WallTime: 4 * time.Second,
				Memory: 256 << 20, Stack: 64 << 20, MaxProcesses: 32,
				MaxFileSize: 16 << 20, MaxStdout: 1 << 20, MaxStderr: 1 << 20,
			},
			MaxTestcases:   1000,
			MaxSourceBytes: 1 << 20,
			MaxTotalInput:  32 << 20,
			MaxTotalOutput: 32 << 20,
		},
	}).Handler()
}

func post(t *testing.T, h http.Handler, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The exact request the existing consumer sends today, and the exact fields it reads
// back. If this test fails, pointing that consumer at this judge breaks.
func TestJudge0CompatGoldenRequest(t *testing.T) {
	sub := &fakeSubmitter{result: judge.SubmissionResult{
		Status:  judge.StatusAccepted,
		Compile: judge.CompileResult{Success: true},
		TestCases: []judge.TestCaseResult{{
			Index: 0, Status: judge.StatusAccepted,
			Stdout: []byte("5\n"), ExitCode: 0,
			CPUTime: 12 * time.Millisecond, Memory: 2048 << 10,
		}},
	}}
	h := newTestServer(t, sub)

	// Byte for byte the body shape from submitCon.js.
	body := `{"source_code":"` + b64("print(2+3)") + `","language_id":71,"stdin":"` +
		b64("") + `","expected_output":"` + b64("5") + `"}`
	rec := post(t, h, "/submissions?base64_encoded=true&wait=true", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	// Decode into the shape the consumer actually reads.
	var got struct {
		Status struct {
			ID          int    `json:"id"`
			Description string `json:"description"`
		} `json:"status"`
		Stdout        *string `json:"stdout"`
		Stderr        *string `json:"stderr"`
		CompileOutput *string `json:"compile_output"`
		Token         string  `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not Judge0-shaped: %v\n%s", err, rec.Body)
	}

	if got.Status.ID != 3 {
		t.Errorf("status.id = %d, want 3 — the consumer treats only 3 as passing", got.Status.ID)
	}
	if got.Status.Description != "Accepted" {
		t.Errorf("status.description = %q, want %q", got.Status.Description, "Accepted")
	}
	if got.Stdout == nil {
		t.Fatal("stdout is null; the consumer displays it")
	}
	decoded, err := base64.StdEncoding.DecodeString(*got.Stdout)
	if err != nil {
		t.Fatalf("stdout is not base64: %v", err)
	}
	if string(decoded) != "5\n" {
		t.Errorf("stdout = %q, want %q", decoded, "5\n")
	}
	// Absent values must be null, as Judge0 sends them.
	if got.Stderr != nil {
		t.Errorf("empty stderr should be null, got %q", *got.Stderr)
	}
	if got.CompileOutput != nil {
		t.Errorf("empty compile_output should be null, got %q", *got.CompileOutput)
	}
	if got.Token == "" {
		t.Error("token is empty")
	}

	// The judge must have received exactly one testcase, decoded.
	if len(sub.got.TestCases) != 1 {
		t.Fatalf("built %d testcases from a Judge0 request, want 1", len(sub.got.TestCases))
	}
	if string(sub.got.Source) != "print(2+3)" {
		t.Errorf("source was not decoded: %q", sub.got.Source)
	}
	if string(sub.got.TestCases[0].ExpectedOutput) != "5" {
		t.Errorf("expected_output was not decoded: %q", sub.got.TestCases[0].ExpectedOutput)
	}
}

// The consumer branches on these substrings rather than on ids.
func TestJudge0StatusMapping(t *testing.T) {
	tests := []struct {
		status   judge.Status
		wantID   int
		contains string
	}{
		{judge.StatusAccepted, 3, "Accepted"},
		{judge.StatusWrongAnswer, 4, "Wrong Answer"},
		{judge.StatusTimeLimitExceeded, 5, "Time Limit"},
		{judge.StatusCompilationError, 6, "Compilation"},
		{judge.StatusRuntimeErrorSegfault, 7, "SIGSEGV"},
		{judge.StatusRuntimeErrorFileSize, 8, "SIGXFSZ"},
		{judge.StatusRuntimeErrorFloatingPoint, 9, "SIGFPE"},
		{judge.StatusRuntimeErrorAborted, 10, "SIGABRT"},
		{judge.StatusRuntimeErrorNonZeroExit, 11, "NZEC"},
		{judge.StatusRuntimeErrorOther, 12, "Runtime Error"},
		{judge.StatusMemoryLimitExceeded, 12, "Memory Limit"},
		{judge.StatusOutputLimitExceeded, 12, "Output Limit"},
		{judge.StatusSystemError, 13, "Internal Error"},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			got := toJudge0Status(tt.status)
			if got.id != tt.wantID {
				t.Errorf("id = %d, want %d", got.id, tt.wantID)
			}
			if !strings.Contains(got.desc, tt.contains) {
				t.Errorf("description %q should contain %q", got.desc, tt.contains)
			}
		})
	}

	// These two substrings are load-bearing for the consumer's overall verdict, and
	// must not appear on statuses that do not mean them.
	for status, j := range judge0Statuses {
		if status != judge.StatusTimeLimitExceeded && strings.Contains(j.desc, "Time Limit") {
			t.Errorf("%v description %q contains \"Time Limit\" but is not a timeout", status, j.desc)
		}
		if status != judge.StatusCompilationError && strings.Contains(j.desc, "Compilation") {
			t.Errorf("%v description %q contains \"Compilation\" but is not a compile error", status, j.desc)
		}
	}
}

func TestCompilationErrorCompatResponse(t *testing.T) {
	h := newTestServer(t, &fakeSubmitter{result: judge.SubmissionResult{
		Status:    judge.StatusCompilationError,
		Compile:   judge.CompileResult{Success: false, Output: []byte("main.c:1: error")},
		TestCases: []judge.TestCaseResult{{Index: 0, Status: judge.StatusCompilationError}},
	}})

	rec := post(t, h, "/submissions?base64_encoded=true&wait=true",
		`{"source_code":"`+b64("bad")+`","language_id":50,"stdin":"","expected_output":""}`)

	var got struct {
		Status        statusDTO `json:"status"`
		CompileOutput *string   `json:"compile_output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ID != 6 {
		t.Errorf("status.id = %d, want 6", got.Status.ID)
	}
	if got.CompileOutput == nil {
		t.Fatal("compile_output is null; the student would see no diagnostics")
	}
	decoded, _ := base64.StdEncoding.DecodeString(*got.CompileOutput)
	if !strings.Contains(string(decoded), "error") {
		t.Errorf("compile_output = %q", decoded)
	}
}

// --- native API ---

func TestNativeBatchSubmission(t *testing.T) {
	sub := &fakeSubmitter{}
	h := newTestServer(t, sub)

	rec := post(t, h, "/submissions", `{
		"language": "python",
		"source_code": "print(1)",
		"testcases": [
			{"stdin": "a", "expected_output": "1"},
			{"stdin": "b", "expected_output": "1"},
			{"stdin": "c", "expected_output": "1"}
		]
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got submissionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Testcases) != 3 {
		t.Fatalf("got %d testcase results, want 3", len(got.Testcases))
	}
	for i, tc := range got.Testcases {
		if tc.Index != i {
			t.Errorf("result %d carries index %d", i, tc.Index)
		}
	}
	if len(sub.got.TestCases) != 3 {
		t.Errorf("the judge received %d testcases", len(sub.got.TestCases))
	}
	if sub.got.Language != 71 {
		t.Errorf("language resolved to %d, want 71", sub.got.Language)
	}
}

func TestNativeAndCompatShareOnePipeline(t *testing.T) {
	// The same source through both surfaces must reach the judge identically.
	native := &fakeSubmitter{}
	post(t, newTestServer(t, native), "/submissions",
		`{"language_id":71,"source_code":"print(1)","testcases":[{"stdin":"x","expected_output":"1"}]}`)

	compat := &fakeSubmitter{}
	post(t, newTestServer(t, compat), "/submissions?base64_encoded=true",
		`{"language_id":71,"source_code":"`+b64("print(1)")+`","stdin":"`+b64("x")+`","expected_output":"`+b64("1")+`"}`)

	if string(native.got.Source) != string(compat.got.Source) {
		t.Errorf("source differs: %q vs %q", native.got.Source, compat.got.Source)
	}
	if string(native.got.TestCases[0].Stdin) != string(compat.got.TestCases[0].Stdin) {
		t.Error("stdin differs between the two surfaces")
	}
	if native.got.Limits != compat.got.Limits {
		t.Error("limits differ between the two surfaces")
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"no language", `{"source_code":"x","testcases":[{}]}`, http.StatusUnprocessableEntity},
		{"unknown language id", `{"language_id":9999,"source_code":"x","testcases":[{}]}`, http.StatusBadRequest},
		{"unknown language name", `{"language":"cobol","source_code":"x","testcases":[{}]}`, http.StatusBadRequest},
		{"empty source", `{"language_id":71,"source_code":"","testcases":[{}]}`, http.StatusUnprocessableEntity},
		{"empty testcase array", `{"language_id":71,"source_code":"x","testcases":[]}`, http.StatusUnprocessableEntity},
		{"bad base64", `{"language_id":71,"source_code":"!!!not base64!!!","testcases":[{}]}`, http.StatusUnprocessableEntity},
	}
	h := newTestServer(t, &fakeSubmitter{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/submissions"
			if strings.Contains(tt.name, "base64") {
				url += "?base64_encoded=true"
			}
			rec := post(t, h, url, tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestTooManyTestcasesIsRejected(t *testing.T) {
	registry, err := lang.LoadRegistry(filepath.Join("..", "..", "configs", "languages.toml"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewServer(Options{
		Submitter: &fakeSubmitter{},
		Registry:  registry,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Limits: Limits{
			Execution:      judge.Limits{CPUTime: time.Second, WallTime: time.Second, Memory: 1 << 20},
			MaxTestcases:   2,
			MaxSourceBytes: 1 << 20,
			MaxTotalInput:  1 << 20,
			MaxTotalOutput: 1 << 20,
		},
	}).Handler()

	rec := post(t, h, "/submissions",
		`{"language_id":71,"source_code":"x","testcases":[{},{},{}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

// A client may ask for tighter limits than configured, never looser.
func TestRequestedLimitsAreClamped(t *testing.T) {
	sub := &fakeSubmitter{}
	h := newTestServer(t, sub)

	post(t, h, "/submissions",
		`{"language_id":71,"source_code":"x","testcases":[{}],"cpu_time_limit":999,"memory_limit":99999999}`)
	if sub.got.Limits.CPUTime > 2*time.Second {
		t.Errorf("cpu limit %v exceeds the configured maximum", sub.got.Limits.CPUTime)
	}
	if sub.got.Limits.Memory > 256<<20 {
		t.Errorf("memory limit %d MB exceeds the configured maximum", sub.got.Limits.Memory.MB())
	}

	post(t, h, "/submissions",
		`{"language_id":71,"source_code":"x","testcases":[{}],"cpu_time_limit":0.5}`)
	if sub.got.Limits.CPUTime != 500*time.Millisecond {
		t.Errorf("cpu limit = %v, want the requested 500ms", sub.got.Limits.CPUTime)
	}
}

func TestOverloadAndDrainAreServiceUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{"draining", sched.ErrDraining, http.StatusServiceUnavailable},
		{"overloaded", sched.ErrOverloaded, http.StatusServiceUnavailable},
		{"too large", sched.ErrTooLarge, http.StatusUnprocessableEntity},
		{"timed out", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestServer(t, &fakeSubmitter{err: tt.err})
			rec := post(t, h, "/submissions", `{"language_id":71,"source_code":"x","testcases":[{}]}`)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestLanguagesEndpoint(t *testing.T) {
	h := newTestServer(t, &fakeSubmitter{})
	req := httptest.NewRequest(http.MethodGet, "/languages", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[int]string{50: "c", 54: "cpp", 62: "java", 71: "python"}
	if len(got) != len(want) {
		t.Fatalf("got %d languages, want %d", len(got), len(want))
	}
	for _, l := range got {
		if want[l.ID] != l.Name {
			t.Errorf("id %d is %q, want %q", l.ID, l.Name, want[l.ID])
		}
	}
}

func TestHealthAndReady(t *testing.T) {
	h := newTestServer(t, &fakeSubmitter{})
	for _, path := range []string{"/health", "/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s returned %d", path, rec.Code)
		}
	}
}

type notReady struct{}

func (notReady) Ready() (bool, string) { return false, "queue unreachable" }

func TestReadyDegradesWithoutKillingLiveness(t *testing.T) {
	registry, _ := lang.LoadRegistry(filepath.Join("..", "..", "configs", "languages.toml"))
	h := NewServer(Options{
		Submitter: &fakeSubmitter{}, Registry: registry, Health: notReady{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/ready = %d, want 503", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/health = %d; a degraded dependency must not fail liveness", rec.Code)
	}
}

func TestAuthToken(t *testing.T) {
	registry, _ := lang.LoadRegistry(filepath.Join("..", "..", "configs", "languages.toml"))
	h := NewServer(Options{
		Submitter: &fakeSubmitter{}, Registry: registry, AuthToken: "s3cret",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Limits: Limits{
			Execution:      judge.Limits{CPUTime: time.Second, WallTime: time.Second, Memory: 1 << 20},
			MaxTestcases:   10,
			MaxSourceBytes: 1 << 20, MaxTotalInput: 1 << 20, MaxTotalOutput: 1 << 20,
		},
	}).Handler()

	body := `{"language_id":71,"source_code":"x","testcases":[{}]}`
	if rec := post(t, h, "/submissions", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated submission = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/submissions", strings.NewReader(body))
	req.Header.Set("X-Judge-Token", "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Error("a correct token was rejected")
	}

	// Liveness must stay reachable without credentials.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/health with auth enabled = %d, want 200", rec.Code)
	}
}
