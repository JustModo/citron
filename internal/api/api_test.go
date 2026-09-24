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

	"github.com/JustModo/citron/internal/judge"
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/sched"
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
	registry, err := lang.LoadRegistry(filepath.Join("..", "..", "languages"))
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

// Wire status ids are part of the API contract.
func TestStatusCodes(t *testing.T) {
	tests := []struct {
		status judge.Status
		wantID int
	}{
		{judge.StatusAccepted, 3},
		{judge.StatusWrongAnswer, 4},
		{judge.StatusTimeLimitExceeded, 5},
		{judge.StatusCompilationError, 6},
		{judge.StatusRuntimeErrorSegfault, 7},
		{judge.StatusRuntimeErrorFileSize, 8},
		{judge.StatusRuntimeErrorFloatingPoint, 9},
		{judge.StatusRuntimeErrorAborted, 10},
		{judge.StatusRuntimeErrorNonZeroExit, 11},
		{judge.StatusRuntimeErrorOther, 12},
		{judge.StatusMemoryLimitExceeded, 12},
		{judge.StatusOutputLimitExceeded, 12},
		{judge.StatusSystemError, 13},
		{judge.Status(999), 13},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			got := toStatusDTO(tt.status)
			if got.ID != tt.wantID {
				t.Errorf("id = %d, want %d", got.ID, tt.wantID)
			}
			if got.Description != tt.status.String() {
				t.Errorf("description = %q, want %q", got.Description, tt.status.String())
			}
		})
	}
}

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
		t.Errorf("citron received %d testcases", len(sub.got.TestCases))
	}
	if sub.got.Language != 71 {
		t.Errorf("language resolved to %d, want 71", sub.got.Language)
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
		{"missing testcases", `{"language_id":71,"source_code":"x","stdin":"","expected_output":"5"}`, http.StatusUnprocessableEntity},
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
	registry, err := lang.LoadRegistry(filepath.Join("..", "..", "languages"))
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
	want := map[int]string{
		50: "c", 54: "cpp", 60: "go", 62: "java", 63: "javascript", 71: "python", 73: "rust", 74: "typescript",
	}
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
	registry, _ := lang.LoadRegistry(filepath.Join("..", "..", "languages"))
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
	registry, _ := lang.LoadRegistry(filepath.Join("..", "..", "languages"))
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

	// /health is exempt from auth.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/health with auth enabled = %d, want 200", rec.Code)
	}
}
