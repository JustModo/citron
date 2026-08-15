// Command judge runs the code judge: HTTP API and execution in one process.
//
// This file is the composition root. Every dependency is constructed here and
// injected downwards; no package reaches out for a connection, a logger or a
// configuration value of its own.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/JustModo/judge/internal/api"
	"github.com/JustModo/judge/internal/compare"
	"github.com/JustModo/judge/internal/config"
	"github.com/JustModo/judge/internal/lang"
	"github.com/JustModo/judge/internal/lang/hooks"
	"github.com/JustModo/judge/internal/metrics"
	"github.com/JustModo/judge/internal/run"
	"github.com/JustModo/judge/internal/sandbox"
	"github.com/JustModo/judge/internal/sched"
	"github.com/JustModo/judge/internal/workspace"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	configPath := flag.String("config", "configs/judge.conf", "path to judge.conf")
	showLanguages := flag.Bool("languages", false, "probe the configured toolchains and exit")
	// The config file defaults to loopback, which is right when the binary runs on a
	// host directly. In a container the network namespace is the boundary, so the
	// process must bind the container's own 0.0.0.0 and Docker decides who may reach
	// the published port.
	address := flag.String("address", "", "override server.address from the config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := runJudge(*configPath, *showLanguages, *address); err != nil {
		fmt.Fprintf(os.Stderr, "judge: %v\n", err)
		os.Exit(1)
	}
}

func runJudge(configPath string, showLanguages bool, address string) error {
	// A broken configuration is fatal at startup. It is the one class of error that
	// should stop the process rather than degrade it.
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if address != "" {
		cfg.Server.Address = address
	}
	log := newLogger(cfg.Log)

	registry, err := lang.LoadRegistry(resolveRelative(configPath, cfg.Languages.Path), hooks.All())
	if err != nil {
		return err
	}

	toolchains := registry.Probe(context.Background())
	if showLanguages {
		for _, t := range toolchains {
			fmt.Printf("%-8s %-9s %s\n", t.Language, availability(t.Available), t.Version)
		}
		return nil
	}
	for _, t := range toolchains {
		if !t.Available {
			// Accepting submissions for a language that cannot run produces
			// failures no student can act on.
			if cfg.Languages.RequireToolchains {
				return fmt.Errorf("language %q is configured but its toolchain is missing: %w", t.Language, t.Err)
			}
			log.Warn("language toolchain missing", "language", t.Language, "error", t.Err)
			continue
		}
		log.Info("language ready", "language", t.Language, "version", t.Version)
	}

	sb, err := newSandbox(cfg, log)
	if err != nil {
		return err
	}
	log.Info("sandbox ready", "driver", sb.Name())

	workspaces, err := workspace.NewManager(cfg.Sandbox.WorkspaceRoot)
	if err != nil {
		return err
	}
	// Clear anything a previous crash left behind before accepting work.
	if err := workspaces.Sweep(); err != nil {
		log.Warn("could not sweep stale workspaces", "error", err)
	}

	cache, err := run.NewCompileCache(cfg.Sandbox.CacheRoot, cfg.Sandbox.CacheEntries)
	if err != nil {
		return err
	}

	metrics := metrics.New()
	admitter := sched.NewAdmitter(cfg.Scheduler.MemoryBudgetMB, cfg.Scheduler.ExecutionSlots)
	runner := run.NewRunner(registry, sb, workspaces, cache, compare.Default(), admitter, run.Options{
		CompileLimits:   cfg.CompileLimits(),
		MaxParallel:     cfg.Limits.Submission.MaxParallelTestcases,
		SubmissionLimit: cfg.SubmissionDeadline(),
		Observer:        metrics,
	}, log)
	scheduler := sched.NewScheduler(runner, cfg.Scheduler.MaxConcurrentSubmissions, cfg.QueueWait())

	metricsDone := make(chan struct{})
	defer close(metricsDone)
	go metrics.Watch(metricsDone, capacity{scheduler, admitter}, 2*time.Second)

	server := api.NewServer(api.Options{
		Submitter:      scheduler,
		Registry:       registry,
		Health:         &health{scheduler: scheduler},
		AuthToken:      cfg.Server.AuthToken,
		Logger:         log,
		MetricsHandler: metrics.Handler(),
		Limits: api.Limits{
			Execution:      cfg.ExecutionLimits(),
			MaxTestcases:   cfg.Limits.Submission.MaxTestcases,
			MaxSourceBytes: cfg.Limits.Submission.MaxSourceMB << 20,
			MaxTotalInput:  cfg.Limits.Submission.MaxTotalInputMB << 20,
			MaxTotalOutput: cfg.Limits.Submission.MaxTotalOutputMB << 20,
		},
	})

	httpServer := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSec) * time.Second,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutSec) * time.Second,
	}

	return serve(httpServer, scheduler, workspaces, cfg, log)
}

// serve runs until a signal arrives, then shuts down in the order that leaves nothing
// running: stop accepting work, let in-flight submissions finish, kill what refuses
// to, then close the listener.
func serve(
	httpServer *http.Server,
	scheduler *sched.Scheduler,
	workspaces *workspace.Manager,
	cfg config.Config,
	log *slog.Logger,
) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("judge listening", "address", cfg.Server.Address)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down", "grace", cfg.ShutdownGrace())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace())
	defer cancel()

	// Refuse new submissions and wait for the running ones.
	if err := scheduler.Drain(shutdownCtx); err != nil {
		log.Warn("drain timed out; terminating running executions", "active", scheduler.Active())
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	if err := workspaces.Sweep(); err != nil {
		log.Warn("workspace cleanup", "error", err)
	}
	log.Info("stopped")
	return nil
}

func newSandbox(cfg config.Config, log *slog.Logger) (sandbox.Sandbox, error) {
	switch cfg.Sandbox.Driver {
	case "nsjail":
		return sandbox.NewNsjail(sandbox.NsjailConfig{
			Path:       cfg.Sandbox.NsjailPath,
			CgroupRoot: cfg.Sandbox.CgroupRoot,
			ReadOnly:   cfg.Sandbox.ReadOnly,
			Symlinks:   cfg.Sandbox.Symlinks,
			TmpfsMB:    cfg.Sandbox.TmpfsMB,
		}, log)
	case "local":
		// Configuration validation already required an explicit opt-in.
		return sandbox.NewLocal(log), nil
	default:
		return nil, fmt.Errorf("unknown sandbox driver %q", cfg.Sandbox.Driver)
	}
}

// capacity joins the two things that bound throughput — submission slots and the
// memory budget — into the single view the metrics gauges sample.
type capacity struct {
	scheduler *sched.Scheduler
	admitter  *sched.Admitter
}

func (c capacity) Active() int64             { return c.scheduler.Active() }
func (c capacity) Queued() int64             { return c.scheduler.Queued() }
func (c capacity) InFlightExecutions() int64 { return c.admitter.InFlight() }
func (c capacity) ReservedMB() int64         { return c.admitter.ReservedMB() }
func (c capacity) BudgetMB() int64           { return c.admitter.BudgetMB() }

// health answers /ready. Being at capacity is not unready — it is the system working
// as configured — but shutting down is.
type health struct{ scheduler *sched.Scheduler }

func (h *health) Ready() (bool, string) {
	if h.scheduler.Draining() {
		return false, "shutting down"
	}
	return true, ""
}

func newLogger(cfg config.Log) *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// resolveRelative interprets a path in the config file relative to that file, so the
// judge can be started from any directory.
func resolveRelative(configPath, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return filepath.Join(filepath.Dir(configPath), filepath.Base(path))
}

func availability(ok bool) string {
	if ok {
		return "ok"
	}
	return "MISSING"
}
