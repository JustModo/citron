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

	"github.com/JustModo/citron/internal/api"
	"github.com/JustModo/citron/internal/compare"
	"github.com/JustModo/citron/internal/config"
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/metrics"
	"github.com/JustModo/citron/internal/run"
	"github.com/JustModo/citron/internal/sandbox"
	"github.com/JustModo/citron/internal/sched"
	"github.com/JustModo/citron/internal/workspace"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	configPath := flag.String("config", "configs/citron.conf", "path to citron.conf")
	showLanguages := flag.Bool("languages", false, "probe the configured toolchains and exit")
	// Lets containers bind 0.0.0.0 while the config file defaults to loopback.
	address := flag.String("address", "", "override server.address from the config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := runCitron(*configPath, *showLanguages, *address); err != nil {
		fmt.Fprintf(os.Stderr, "citron: %v\n", err)
		os.Exit(1)
	}
}

func runCitron(configPath string, showLanguages bool, address string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if address != "" {
		cfg.Server.Address = address
	}
	log := newLogger(cfg.Log)

	registry, err := lang.LoadRegistry(resolveRelative(configPath, cfg.Languages.Path))
	if err != nil {
		return err
	}

	toolchains := registry.Probe(context.Background())
	if showLanguages {
		for _, t := range toolchains {
			fmt.Printf("%-12s %-9s %s\n", t.Language, availability(t.Available), t.Version)
		}
		return nil
	}
	for _, t := range toolchains {
		if !t.Available {
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
	// Remove workspaces left by a previous crash.
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

// serve runs until SIGINT or SIGTERM, then drains the scheduler before shutting down
// the HTTP server so in-flight submissions can still respond.
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
		log.Info("citron listening", "address", cfg.Server.Address)
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
			// Inverted so the zero value of NsjailConfig is the stricter jail.
			NoUserNamespace: !cfg.Sandbox.UserNamespace,
		}, log)
	case "local":
		// Config validation requires an explicit opt-in for this unsandboxed driver.
		return sandbox.NewLocal(log), nil
	default:
		return nil, fmt.Errorf("unknown sandbox driver %q", cfg.Sandbox.Driver)
	}
}

// capacity adapts the scheduler and admitter to metrics.Capacity.
type capacity struct {
	scheduler *sched.Scheduler
	admitter  *sched.Admitter
}

func (c capacity) Active() int64             { return c.scheduler.Active() }
func (c capacity) Queued() int64             { return c.scheduler.Queued() }
func (c capacity) InFlightExecutions() int64 { return c.admitter.InFlight() }
func (c capacity) ReservedMB() int64         { return c.admitter.ReservedMB() }
func (c capacity) BudgetMB() int64           { return c.admitter.BudgetMB() }

// health reports unready only while draining; being at capacity is still ready.
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

// resolveRelative returns path if it is absolute or exists; otherwise it looks for
// the same file name next to the config file.
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
