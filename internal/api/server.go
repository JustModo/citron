// Package api exposes citron over HTTP.
//
// Handlers stay thin on purpose: parse, validate, map to a domain request, call the
// application, map the result back. Nothing here compiles code, spawns a process or
// touches the filesystem.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/JustModo/citron/internal/judge"
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/sched"
)

// Submitter runs a submission. The scheduler implements it.
type Submitter interface {
	Submit(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error)
}

// Health reports whether citron can currently accept work.
type Health interface {
	Ready() (bool, string)
}

type Server struct {
	submitter Submitter
	registry  *lang.Registry
	health    Health
	limits    Limits
	authToken string
	log       *slog.Logger

	mux *http.ServeMux
}

type Options struct {
	Submitter Submitter
	Registry  *lang.Registry
	Health    Health
	Limits    Limits
	AuthToken string
	Logger    *slog.Logger
	// MetricsHandler is mounted at /metrics when set.
	MetricsHandler http.Handler
}

func NewServer(opts Options) *Server {
	s := &Server{
		submitter: opts.Submitter,
		registry:  opts.Registry,
		health:    opts.Health,
		limits:    opts.Limits,
		authToken: opts.AuthToken,
		log:       opts.Logger,
		mux:       http.NewServeMux(),
	}

	s.mux.HandleFunc("POST /submissions", s.handleSubmission)
	s.mux.HandleFunc("GET /languages", s.handleLanguages)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /ready", s.handleReady)
	if opts.MetricsHandler != nil {
		s.mux.Handle("GET /metrics", opts.MetricsHandler)
	}
	return s
}

func (s *Server) Handler() http.Handler {
	return s.recoverPanic(s.authenticate(s.logRequest(s.mux)))
}

// --- middleware ---

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				writeJSON(w, http.StatusInternalServerError, errorResponse{"internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// authenticate guards everything except liveness, which a container runtime must be
// able to reach without credentials.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Judge-Token") != s.authToken {
			writeJSON(w, http.StatusUnauthorized, errorResponse{"unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/health" || r.URL.Path == "/metrics" {
			return // liveness probes would drown everything else
		}
		s.log.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// --- handlers ---

func (s *Server) handleSubmission(w http.ResponseWriter, r *http.Request) {
	c := codec{base64: r.URL.Query().Get("base64_encoded") == "true"}

	// Bounded read: an oversized body must be refused, not buffered.
	maxBody := s.limits.MaxSourceBytes + s.limits.MaxTotalInput + s.limits.MaxTotalOutput + (1 << 20)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{"request body too large"})
		return
	}

	var req submissionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"malformed JSON: " + err.Error()})
		return
	}

	// A request without a testcases array is the legacy single-testcase form. That
	// is the only difference between the two surfaces; everything below is shared.
	compat := req.Testcases == nil

	sub, err := s.buildSubmission(req, c)
	if err != nil {
		if errors.Is(err, lang.ErrUnknownLanguage) {
			writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
			return
		}
		if errors.Is(err, errValidation) {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{err.Error()})
		return
	}

	result, err := s.submitter.Submit(r.Context(), sub)
	if err != nil {
		s.writeSubmitError(w, err)
		return
	}

	if compat {
		writeJSON(w, http.StatusCreated, toLegacy(result, c))
		return
	}
	writeJSON(w, http.StatusCreated, toNative(result, c))
}

func (s *Server) writeSubmitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sched.ErrDraining):
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{"citron is shutting down"})
	case errors.Is(err, sched.ErrOverloaded):
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{"citron is at capacity"})
	case errors.Is(err, sched.ErrTooLarge):
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{err.Error()})
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		// Citron ran out of its own time budget. Say so rather than leaving the
		// client to guess from a dropped connection.
		writeJSON(w, http.StatusGatewayTimeout, errorResponse{"submission exceeded citron's time budget"})
	default:
		s.log.Error("submission failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{"submission failed"})
	}
}

func (s *Server) buildSubmission(req submissionRequest, c codec) (judge.Submission, error) {
	language, err := s.resolveLanguage(req)
	if err != nil {
		return judge.Submission{}, err
	}

	source, err := c.decode(req.SourceCode)
	if err != nil {
		return judge.Submission{}, invalid("source_code: %s", err)
	}
	if len(source) == 0 {
		return judge.Submission{}, invalid("source_code is required")
	}
	if int64(len(source)) > s.limits.MaxSourceBytes {
		return judge.Submission{}, invalid("source_code exceeds %d bytes", s.limits.MaxSourceBytes)
	}

	raw := req.Testcases
	if raw == nil {
		raw = []testcaseRequest{{Stdin: req.Stdin, ExpectedOutput: req.ExpectedOutput}}
	}
	if len(raw) == 0 {
		return judge.Submission{}, invalid("at least one testcase is required")
	}
	if len(raw) > s.limits.MaxTestcases {
		return judge.Submission{}, invalid("%d testcases exceeds the limit of %d", len(raw), s.limits.MaxTestcases)
	}

	var totalIn, totalOut int64
	cases := make([]judge.TestCase, len(raw))
	for i, tc := range raw {
		stdin, err := c.decode(tc.Stdin)
		if err != nil {
			return judge.Submission{}, invalid("testcase %d stdin: %s", i, err)
		}
		expected, err := c.decode(tc.ExpectedOutput)
		if err != nil {
			return judge.Submission{}, invalid("testcase %d expected_output: %s", i, err)
		}
		totalIn += int64(len(stdin))
		totalOut += int64(len(expected))
		cases[i] = judge.TestCase{Index: judge.TestCaseIndex(i), Stdin: stdin, ExpectedOutput: expected}
	}
	if totalIn > s.limits.MaxTotalInput {
		return judge.Submission{}, invalid("total stdin exceeds %d bytes", s.limits.MaxTotalInput)
	}
	if totalOut > s.limits.MaxTotalOutput {
		return judge.Submission{}, invalid("total expected_output exceeds %d bytes", s.limits.MaxTotalOutput)
	}

	return judge.Submission{
		ID:        judge.SubmissionID(newID()),
		Language:  language.ID(),
		Source:    source,
		TestCases: cases,
		Limits:    s.limits.resolve(req),
	}, nil
}

func (s *Server) resolveLanguage(req submissionRequest) (*lang.Language, error) {
	if req.LanguageID > 0 {
		return s.registry.ByID(judge.LanguageID(req.LanguageID))
	}
	if req.Language != "" {
		return s.registry.ByName(req.Language)
	}
	return nil, invalid("language_id or language is required")
}

func (s *Server) handleLanguages(w http.ResponseWriter, _ *http.Request) {
	type languageDTO struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Name2 string `json:"label"`
	}
	out := make([]languageDTO, 0, len(s.registry.All()))
	for _, l := range s.registry.All() {
		out = append(out, languageDTO{ID: int(l.ID()), Name: l.Name(), Name2: l.Label()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady answers whether work can be accepted right now. A dependency being
// unavailable makes this false; it does not take the process down.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	ready, reason := true, ""
	if s.health != nil {
		ready, reason = s.health.Ready()
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// --- mapping ---

func toNative(res judge.SubmissionResult, c codec) submissionResponse {
	out := submissionResponse{
		ID:         string(res.ID),
		Status:     statusDTO{toLegacyStatus(res.Status).id, res.Status.String()},
		WallTimeMS: res.WallTime.Milliseconds(),
		Compile: compileDTO{
			Skipped:    res.Compile.Skipped,
			Success:    res.Compile.Success,
			Output:     c.encode(res.Compile.Output),
			DurationMS: res.Compile.Duration.Milliseconds(),
			Cached:     res.Compile.Cached,
		},
		Testcases: make([]testcaseResultDTO, len(res.TestCases)),
	}
	for i, tc := range res.TestCases {
		out.Testcases[i] = testcaseResultDTO{
			Index:           int(tc.Index),
			Status:          statusDTO{toLegacyStatus(tc.Status).id, tc.Status.String()},
			Stdout:          c.encode(tc.Stdout),
			Stderr:          c.encode(tc.Stderr),
			ExitCode:        tc.ExitCode,
			Signal:          tc.Signal,
			CPUTimeMS:       tc.CPUTime.Milliseconds(),
			WallTimeMS:      tc.WallTime.Milliseconds(),
			MemoryKB:        int64(tc.Memory) >> 10,
			MemorySource:    string(tc.MemorySource),
			StdoutTruncated: tc.StdoutTruncated,
			StderrTruncated: tc.StderrTruncated,
			Message:         tc.Message,
		}
	}
	return out
}

// toLegacy flattens a submission down to the legacy single-testcase response.
func toLegacy(res judge.SubmissionResult, c codec) legacyResponse {
	status := toLegacyStatus(res.Status)
	out := legacyResponse{
		Token:  string(res.ID),
		Status: statusDTO{status.id, status.desc},
	}
	if len(res.Compile.Output) > 0 {
		out.CompileOutput = nilIfEmpty(c.encode(res.Compile.Output))
	}
	if len(res.TestCases) > 0 {
		tc := res.TestCases[0]
		out.Stdout = nilIfEmpty(c.encode(tc.Stdout))
		out.Stderr = nilIfEmpty(c.encode(tc.Stderr))
		exit := tc.ExitCode
		out.ExitCode = &exit
		t := strconv.FormatFloat(tc.CPUTime.Seconds(), 'f', 3, 64)
		out.Time = &t
		mem := int64(tc.Memory) >> 10
		out.Memory = &mem
		if tc.Message != "" {
			out.Message = nilIfEmpty(tc.Message)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already written; there is nothing useful left to do.
		_ = err
	}
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sub-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
