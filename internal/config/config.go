// Package config loads citron.conf into an immutable, validated struct. Nothing
// reads configuration after startup; everything is injected from the composition root.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/JustModo/citron/internal/judge"
)

// Durations are seconds in the file (matching DESIGN.md) and converted here, so the
// config surface stays plain data and unit conversion happens exactly once.
type Config struct {
	Server    Server    `toml:"server"`
	Sandbox   Sandbox   `toml:"sandbox"`
	Limits    Limits    `toml:"limits"`
	Scheduler Scheduler `toml:"scheduler"`
	Jobs      Jobs      `toml:"jobs"`
	Queue     Queue     `toml:"queue"`
	Languages Languages `toml:"languages"`
	Log       Log       `toml:"log"`
}

type Server struct {
	Address         string  `toml:"address"`
	ReadTimeoutSec  float64 `toml:"read_timeout_seconds"`
	WriteTimeoutSec float64 `toml:"write_timeout_seconds"`
	ShutdownSec     float64 `toml:"shutdown_grace_seconds"`
	// AuthToken, when set, is required in the X-Judge-Token header. Empty disables
	// the check; the service must then be reachable only on a private network.
	AuthToken string `toml:"auth_token"`
}

type Sandbox struct {
	// Driver is "nsjail" (production) or "local" (development, NOT isolated).
	Driver           string `toml:"driver"`
	AllowUnsafeLocal bool   `toml:"allow_unsafe_local"`
	NsjailPath       string `toml:"nsjail_path"`
	CgroupRoot       string `toml:"cgroup_root"`
	WorkspaceRoot    string `toml:"workspace_root"`
	CacheRoot        string `toml:"cache_root"`
	CacheEntries     int    `toml:"cache_entries"`

	// ReadOnly and Symlinks build the filesystem a submission sees. They are
	// configuration so that a language needing another path is a config change
	// rather than a rebuild.
	ReadOnly []string `toml:"readonly_paths"`
	Symlinks []string `toml:"symlinks"`
	TmpfsMB  int64    `toml:"tmpfs_mb"`
}

type Limits struct {
	Execution  ExecutionLimits  `toml:"execution"`
	Compile    CompileLimits    `toml:"compile"`
	Submission SubmissionLimits `toml:"submission"`
}

type ExecutionLimits struct {
	CPUTimeSec      float64 `toml:"cpu_time_seconds"`
	CPUExtraTimeSec float64 `toml:"cpu_extra_time_seconds"`
	WallTimeSec     float64 `toml:"wall_time_seconds"`
	MemoryMB        int64   `toml:"memory_mb"`
	StackMB         int64   `toml:"stack_mb"`
	MaxProcesses    int     `toml:"max_processes"`
	MaxFileMB       int64   `toml:"max_file_mb"`
	StdoutMB        int64   `toml:"stdout_mb"`
	StderrMB        int64   `toml:"stderr_mb"`
}

type CompileLimits struct {
	WallTimeSec  float64 `toml:"wall_time_seconds"`
	CPUTimeSec   float64 `toml:"cpu_time_seconds"`
	MemoryMB     int64   `toml:"memory_mb"`
	MaxProcesses int     `toml:"max_processes"`
	MaxFileMB    int64   `toml:"max_file_mb"`
	OutputKB     int64   `toml:"output_kb"`
}

type SubmissionLimits struct {
	MaxTestcases         int     `toml:"max_testcases"`
	MaxSourceMB          int64   `toml:"max_source_mb"`
	MaxTotalInputMB      int64   `toml:"max_total_input_mb"`
	MaxTotalOutputMB     int64   `toml:"max_total_output_mb"`
	MaxParallelTestcases int     `toml:"max_parallel_testcases"`
	MaxTotalWallTimeSec  float64 `toml:"max_total_wall_time_seconds"`
}

type Scheduler struct {
	MaxConcurrentSubmissions int `toml:"max_concurrent_submissions"`
	// MaxQueueWaitSec bounds how long a submission waits for a slot. Past it the
	// Citron refuses with 503 instead of holding the connection until the client
	// times out having been told nothing.
	MaxQueueWaitSec float64 `toml:"max_queue_wait_seconds"`
	ExecutionSlots  int     `toml:"execution_slots"`
	MemoryBudgetMB  int64   `toml:"memory_budget_mb"`
}

type Jobs struct {
	MaxAttempts int `toml:"max_attempts"`
}

type Queue struct {
	Driver   string `toml:"driver"` // "inproc" | "redis"
	RedisURL string `toml:"redis_url"`
	Stream   string `toml:"stream"`
	Group    string `toml:"group"`
}

type Languages struct {
	Path string `toml:"path"`
	// RequireToolchains fails startup when a configured language's compiler or
	// runtime is missing, rather than accepting submissions that cannot run.
	RequireToolchains bool `toml:"require_toolchains"`
}

type Log struct {
	Level  string `toml:"level"`
	Format string `toml:"format"` // "json" | "text"
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// ExecutionLimits converts the configured defaults into domain limits.
func (c Config) ExecutionLimits() judge.Limits {
	e := c.Limits.Execution
	return judge.Limits{
		CPUTime:      secs(e.CPUTimeSec),
		CPUExtraTime: secs(e.CPUExtraTimeSec),
		WallTime:     secs(e.WallTimeSec),
		Memory:       judge.MemoryBytes(e.MemoryMB << 20),
		Stack:        judge.MemoryBytes(e.StackMB << 20),
		MaxProcesses: e.MaxProcesses,
		MaxFileSize:  e.MaxFileMB << 20,
		MaxStdout:    e.StdoutMB << 20,
		MaxStderr:    e.StderrMB << 20,
	}
}

func (c Config) CompileLimits() judge.Limits {
	k := c.Limits.Compile
	return judge.Limits{
		CPUTime:      secs(k.CPUTimeSec),
		WallTime:     secs(k.WallTimeSec),
		Memory:       judge.MemoryBytes(k.MemoryMB << 20),
		Stack:        judge.MemoryBytes(c.Limits.Execution.StackMB << 20),
		MaxProcesses: k.MaxProcesses,
		MaxFileSize:  k.MaxFileMB << 20,
		MaxStdout:    k.OutputKB << 10,
		MaxStderr:    k.OutputKB << 10,
	}
}

func (c Config) SubmissionDeadline() time.Duration {
	return secs(c.Limits.Submission.MaxTotalWallTimeSec)
}

func (c Config) ShutdownGrace() time.Duration { return secs(c.Server.ShutdownSec) }

func (c Config) QueueWait() time.Duration { return secs(c.Scheduler.MaxQueueWaitSec) }

// Default returns a configuration sized for the 2 vCPU / 8 GB baseline.
func Default() Config {
	return Config{
		Server: Server{
			Address:         "127.0.0.1:2358",
			ReadTimeoutSec:  30,
			WriteTimeoutSec: 60,
			ShutdownSec:     20,
		},
		Sandbox: Sandbox{
			Driver:        "nsjail",
			NsjailPath:    "/usr/local/bin/nsjail",
			CgroupRoot:    "/sys/fs/cgroup/citron",
			WorkspaceRoot: "/box",
			CacheRoot:     "/box/cache",
			CacheEntries:  256,
			ReadOnly:      []string{"/usr", "/etc/alternatives", "/etc/java-21-openjdk"},
			Symlinks:      []string{"/usr/bin:/bin", "/usr/lib:/lib", "/usr/lib64:/lib64", "/usr/sbin:/sbin"},
			TmpfsMB:       64,
		},
		Limits: Limits{
			Execution: ExecutionLimits{
				CPUTimeSec: 2, CPUExtraTimeSec: 0.5, WallTimeSec: 4,
				MemoryMB: 256, StackMB: 64, MaxProcesses: 32,
				MaxFileMB: 16, StdoutMB: 1, StderrMB: 1,
			},
			Compile: CompileLimits{
				WallTimeSec: 15, CPUTimeSec: 12, MemoryMB: 512,
				MaxProcesses: 64, MaxFileMB: 64, OutputKB: 64,
			},
			Submission: SubmissionLimits{
				MaxTestcases: 1000, MaxSourceMB: 1,
				MaxTotalInputMB: 32, MaxTotalOutputMB: 32,
				MaxParallelTestcases: 4,
				// Below the 45s client abort so an overloaded citron still answers.
				MaxTotalWallTimeSec: 30,
			},
		},
		Scheduler: Scheduler{
			MaxConcurrentSubmissions: 2,
			MaxQueueWaitSec:          15,
			ExecutionSlots:           2,
			MemoryBudgetMB:           1024,
		},
		Jobs:      Jobs{MaxAttempts: 2},
		Queue:     Queue{Driver: "inproc", Stream: "citron:submissions", Group: "citron"},
		Languages: Languages{Path: "configs/languages.toml", RequireToolchains: true},
		Log:       Log{Level: "info", Format: "json"},
	}
}

// Load reads path over the defaults. A missing file is an error: running on implicit
// defaults in production is how limits silently stop being enforced.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		if sErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return Config{}, fmt.Errorf("config %s: unknown key(s):\n%s", path, sErr.String())
		}
		if dErr, ok := errors.AsType[*toml.DecodeError](err); ok {
			return Config{}, fmt.Errorf("config %s:\n%s", path, dErr.String())
		}
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

var ErrInvalid = errors.New("invalid configuration")

func (c Config) Validate() error {
	if err := c.ExecutionLimits().Validate(); err != nil {
		return fmt.Errorf("limits.execution: %w", err)
	}
	if err := c.CompileLimits().Validate(); err != nil {
		return fmt.Errorf("limits.compile: %w", err)
	}

	checks := []struct {
		ok   bool
		what string
	}{
		{c.Server.Address != "", "server.address is required"},
		{c.Sandbox.Driver == "nsjail" || c.Sandbox.Driver == "local",
			`sandbox.driver must be "nsjail" or "local"`},
		{c.Sandbox.Driver != "local" || c.Sandbox.AllowUnsafeLocal,
			`sandbox.driver = "local" does not isolate untrusted code; set sandbox.allow_unsafe_local = true to accept that`},
		{c.Sandbox.WorkspaceRoot != "", "sandbox.workspace_root is required"},
		{c.Queue.Driver == "inproc" || c.Queue.Driver == "redis",
			`queue.driver must be "inproc" or "redis"`},
		{c.Queue.Driver != "redis" || c.Queue.RedisURL != "",
			`queue.redis_url is required when queue.driver = "redis"`},
		{c.Limits.Submission.MaxTestcases > 0, "limits.submission.max_testcases must be > 0"},
		{c.Limits.Submission.MaxParallelTestcases > 0, "limits.submission.max_parallel_testcases must be > 0"},
		{c.Limits.Submission.MaxTotalWallTimeSec > 0, "limits.submission.max_total_wall_time_seconds must be > 0"},
		{c.Scheduler.MaxConcurrentSubmissions > 0, "scheduler.max_concurrent_submissions must be > 0"},
		{c.Scheduler.ExecutionSlots > 0, "scheduler.execution_slots must be > 0"},
		{c.Scheduler.MaxQueueWaitSec > 0, "scheduler.max_queue_wait_seconds must be > 0"},
		// Queue wait plus execution must still fit inside the client's patience.
		{c.Scheduler.MaxQueueWaitSec < c.Limits.Submission.MaxTotalWallTimeSec,
			"scheduler.max_queue_wait_seconds must be < limits.submission.max_total_wall_time_seconds"},
		{c.Jobs.MaxAttempts > 0, "jobs.max_attempts must be > 0"},
		{c.Languages.Path != "", "languages.path is required"},
		// A submission that cannot fit in the budget would block forever at admission.
		{c.Scheduler.MemoryBudgetMB >= c.Limits.Execution.MemoryMB,
			"scheduler.memory_budget_mb must be >= limits.execution.memory_mb"},
		// Citron must answer before the client gives up.
		{c.Limits.Submission.MaxTotalWallTimeSec <= c.Server.WriteTimeoutSec,
			"limits.submission.max_total_wall_time_seconds must be <= server.write_timeout_seconds"},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("%w: %s", ErrInvalid, c.what)
		}
	}
	return nil
}
