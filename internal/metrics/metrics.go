package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/JustModo/citron/internal/judge"
)

// Metrics holds citron's Prometheus collectors in a private registry.
type Metrics struct {
	registry *prometheus.Registry

	submissions   *prometheus.CounterVec
	testcases     *prometheus.CounterVec
	compiles      *prometheus.CounterVec
	compileTime   *prometheus.HistogramVec
	executionTime *prometheus.HistogramVec
	submissionDur *prometheus.HistogramVec

	activeSubmissions prometheus.Gauge
	queuedSubmissions prometheus.Gauge
	activeExecutions  prometheus.Gauge
	reservedMemory    prometheus.Gauge
	memoryBudget      prometheus.Gauge

	sandboxFailures *prometheus.CounterVec
	outputTruncated prometheus.Counter
}

// New creates and registers all collectors. The judge_* names are kept for
// compatibility with existing dashboards and alerts.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		submissions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_submissions_total",
			Help: "Submissions completed, by language and final status.",
		}, []string{"language", "status"}),
		testcases: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_testcases_total",
			Help: "Testcases executed, by language and status.",
		}, []string{"language", "status"}),
		compiles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_compiles_total",
			Help: "Compilations, by language and outcome. A high cached ratio is the compile cache doing its job.",
		}, []string{"language", "outcome"}),
		compileTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "judge_compile_duration_seconds",
			Help:    "Time spent compiling, excluding cache hits.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 20},
		}, []string{"language"}),
		executionTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "judge_execution_duration_seconds",
			Help:    "Wall time of a single testcase execution.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10},
		}, []string{"language"}),
		submissionDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "judge_submission_duration_seconds",
			Help:    "End-to-end time for a whole submission.",
			Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 20, 30, 60},
		}, []string{"language"}),

		activeSubmissions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_active_submissions", Help: "Submissions currently running.",
		}),
		queuedSubmissions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_queued_submissions", Help: "Submissions waiting for a slot.",
		}),
		activeExecutions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_active_executions", Help: "Sandboxed executions currently running.",
		}),
		reservedMemory: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_reserved_memory_bytes", Help: "Memory reserved by admission control.",
		}),
		memoryBudget: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "judge_memory_budget_bytes", Help: "Total memory admission control may commit.",
		}),

		sandboxFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "judge_sandbox_failures_total",
			Help: "Sandbox failures. These are citron's fault, not a submission's, and should be zero.",
		}, []string{"reason"}),
		outputTruncated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "judge_output_truncated_total", Help: "Executions whose output hit the limit.",
		}),
	}

	reg.MustRegister(
		m.submissions, m.testcases, m.compiles, m.compileTime, m.executionTime,
		m.submissionDur, m.activeSubmissions, m.queuedSubmissions, m.activeExecutions,
		m.reservedMemory, m.memoryBudget, m.sandboxFailures, m.outputTruncated,
	)
	return m
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveSubmission records one finished submission and all of its testcases.
func (m *Metrics) ObserveSubmission(language string, res judge.SubmissionResult) {
	m.submissions.WithLabelValues(language, res.Status.String()).Inc()
	m.submissionDur.WithLabelValues(language).Observe(res.WallTime.Seconds())

	switch {
	case res.Compile.Skipped:
	case res.Compile.Cached:
		m.compiles.WithLabelValues(language, "cached").Inc()
	default:
		outcome := "failure"
		if res.Compile.Success {
			outcome = "success"
		}
		m.compiles.WithLabelValues(language, outcome).Inc()
		m.compileTime.WithLabelValues(language).Observe(res.Compile.Duration.Seconds())
	}

	for _, tc := range res.TestCases {
		m.testcases.WithLabelValues(language, tc.Status.String()).Inc()
		m.executionTime.WithLabelValues(language).Observe(tc.WallTime.Seconds())
		if tc.StdoutTruncated || tc.StderrTruncated {
			m.outputTruncated.Inc()
		}
		if tc.Status == judge.StatusSystemError {
			m.sandboxFailures.WithLabelValues("execution").Inc()
		}
	}
}

// Capacity is the live state reported by the gauges.
type Capacity interface {
	Active() int64
	Queued() int64
	InFlightExecutions() int64
	ReservedMB() int64
	BudgetMB() int64
}

// Watch samples c into the gauges every interval until done is closed. Sampling
// keeps metric calls off the acquire/release path.
func (m *Metrics) Watch(done <-chan struct{}, c Capacity, every time.Duration) {
	m.memoryBudget.Set(float64(c.BudgetMB() << 20))
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			m.activeSubmissions.Set(float64(c.Active()))
			m.queuedSubmissions.Set(float64(c.Queued()))
			m.activeExecutions.Set(float64(c.InFlightExecutions()))
			m.reservedMemory.Set(float64(c.ReservedMB() << 20))
		}
	}
}
