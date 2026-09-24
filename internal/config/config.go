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

// Config is the whole citron.conf. Durations are stored as seconds and converted by
// its accessor methods.
type Config struct {
	Server    Server    `toml:"server"`
	Sandbox   Sandbox   `toml:"sandbox"`
	Limits    Limits    `toml:"limits"`
	Scheduler Scheduler `toml:"scheduler"`
	Languages Languages `toml:"languages"`
	Log       Log       `toml:"log"`
}

// Server configures the HTTP listener.
type Server struct {
	Address         string  `toml:"address"`
	ReadTimeoutSec  float64 `toml:"read_timeout_seconds"`
	WriteTimeoutSec float64 `toml:"write_timeout_seconds"`
	ShutdownSec     float64 `toml:"shutdown_grace_seconds"`
	// MaxInFlight bounds submissions being read, decoded, queued or run at once, so
	// request buffering cannot outgrow process memory.
	MaxInFlight int `toml:"max_inflight_submissions"`
	// AuthToken, when set, is required in the X-Judge-Token header. Empty disables
	// the check, so the service must then be reachable only on a private network.
	AuthToken string `toml:"auth_token"`
}

// Sandbox configures the execution driver and the filesystem it exposes.
type Sandbox struct {
	// Driver is "nsjail" (production) or "local" (development, NOT isolated).
	Driver           string `toml:"driver"`
	AllowUnsafeLocal bool   `toml:"allow_unsafe_local"`
	NsjailPath       string `toml:"nsjail_path"`
	CgroupRoot       string `toml:"cgroup_root"`
	WorkspaceRoot    string `toml:"workspace_root"`
	CacheRoot        string `toml:"cache_root"`
	CacheEntries     int    `toml:"cache_entries"`
	// CacheMB bounds the bytes the compile cache keeps on the filesystem it shares
	// with workspaces.
	CacheMB int64 `toml:"cache_mb"`

	// ReadOnly and Symlinks ("target:link") build the filesystem a submission sees.
	ReadOnly []string `toml:"readonly_paths"`
	Symlinks []string `toml:"symlinks"`
	TmpfsMB  int64    `toml:"tmpfs_mb"`
	// UserNamespace gives each jail its own user namespace. Disable only where the
	// platform cannot unmask /proc (e.g. Docker Swarm).
	UserNamespace bool `toml:"user_namespace"`
}

// Limits groups the execution, compile and submission limits.
type Limits struct {
	Execution  ExecutionLimits  `toml:"execution"`
	Compile    CompileLimits    `toml:"compile"`
	Submission SubmissionLimits `toml:"submission"`
}

// ExecutionLimits are the default per-testcase limits.
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

// CompileLimits bound the compile step.
type CompileLimits struct {
	WallTimeSec  float64 `toml:"wall_time_seconds"`
	CPUTimeSec   float64 `toml:"cpu_time_seconds"`
	MemoryMB     int64   `toml:"memory_mb"`
	MaxProcesses int     `toml:"max_processes"`
	MaxFileMB    int64   `toml:"max_file_mb"`
	OutputKB     int64   `toml:"output_kb"`
}

// SubmissionLimits bound a whole submission.
type SubmissionLimits struct {
	MaxTestcases     int   `toml:"max_testcases"`
	MaxSourceMB      int64 `toml:"max_source_mb"`
	MaxTotalInputMB  int64 `toml:"max_total_input_mb"`
	MaxTotalOutputMB int64 `toml:"max_total_output_mb"`
	// MaxReturnedOutputMB bounds the program output kept and returned for one
	// submission; output past it is dropped and flagged as truncated.
	MaxReturnedOutputMB  int64   `toml:"max_returned_output_mb"`
	MaxParallelTestcases int     `toml:"max_parallel_testcases"`
	MaxTotalWallTimeSec  float64 `toml:"max_total_wall_time_seconds"`
}

// Scheduler configures admission and concurrency.
type Scheduler struct {
	MaxConcurrentSubmissions int `toml:"max_concurrent_submissions"`
	// MaxQueueWaitSec bounds how long a submission waits for a slot before it is
	// refused with 503.
	MaxQueueWaitSec float64 `toml:"max_queue_wait_seconds"`
	ExecutionSlots  int     `toml:"execution_slots"`
	MemoryBudgetMB  int64   `toml:"memory_budget_mb"`
}

// Languages locates the language packs.
type Languages struct {
	Path string `toml:"path"`
	// RequireToolchains fails startup when a configured language's toolchain is
	// missing.
	RequireToolchains bool `toml:"require_toolchains"`
}

// Log configures logging.
type Log struct {
	Level  string `toml:"level"`
	Format string `toml:"format"` // "json" | "text"
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// ExecutionLimits returns the default per-testcase limits.
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

// CompileLimits returns the compile-step limits. Stack is shared with execution.
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

// SubmissionDeadline returns the wall-clock budget for a whole submission.
func (c Config) SubmissionDeadline() time.Duration {
	return secs(c.Limits.Submission.MaxTotalWallTimeSec)
}

// ShutdownGrace returns how long shutdown waits for in-flight requests.
func (c Config) ShutdownGrace() time.Duration { return secs(c.Server.ShutdownSec) }

// QueueWait returns the maximum time a submission waits for admission.
func (c Config) QueueWait() time.Duration { return secs(c.Scheduler.MaxQueueWaitSec) }

// Default returns a configuration sized for the 2 vCPU / 8 GB baseline.
func Default() Config {
	return Config{
		Server: Server{
			Address:         "127.0.0.1:2358",
			ReadTimeoutSec:  30,
			WriteTimeoutSec: 60,
			ShutdownSec:     20,
			MaxInFlight:     8,
		},
		Sandbox: Sandbox{
			Driver:        "nsjail",
			NsjailPath:    "/usr/local/bin/nsjail",
			CgroupRoot:    "/sys/fs/cgroup/citron",
			WorkspaceRoot: "/box",
			CacheRoot:     "/box/cache",
			CacheEntries:  256,
			CacheMB:       512,
			ReadOnly:      []string{"/usr", "/etc/alternatives"},
			Symlinks:      []string{"/usr/bin:/bin", "/usr/lib:/lib", "/usr/lib64:/lib64", "/usr/sbin:/sbin"},
			TmpfsMB:       64,
			UserNamespace: true,
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
				MaxReturnedOutputMB:  16,
				MaxParallelTestcases: 4,
				// Below the client's 45 s timeout.
				MaxTotalWallTimeSec: 30,
			},
		},
		Scheduler: Scheduler{
			MaxConcurrentSubmissions: 2,
			MaxQueueWaitSec:          15,
			ExecutionSlots:           2,
			MemoryBudgetMB:           1024,
		},
		Languages: Languages{Path: "/usr/local/share/citron/languages", RequireToolchains: true},
		Log:       Log{Level: "info", Format: "json"},
	}
}

// Load reads path over Default and validates the result. A missing file is an
// error so limits are never taken implicitly from defaults.
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

// ErrInvalid is wrapped by configuration errors other than invalid limits.
var ErrInvalid = errors.New("invalid configuration")

// Validate reports the first invalid or inconsistent setting.
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
		{c.Sandbox.CacheMB > 0, "sandbox.cache_mb must be > 0"},
		{c.Limits.Submission.MaxReturnedOutputMB > 0, "limits.submission.max_returned_output_mb must be > 0"},
		// Fewer in-flight requests than submission slots would leave slots idle.
		{c.Server.MaxInFlight >= c.Scheduler.MaxConcurrentSubmissions,
			"server.max_inflight_submissions must be >= scheduler.max_concurrent_submissions"},
		{c.Limits.Submission.MaxTestcases > 0, "limits.submission.max_testcases must be > 0"},
		{c.Limits.Submission.MaxParallelTestcases > 0, "limits.submission.max_parallel_testcases must be > 0"},
		{c.Limits.Submission.MaxTotalWallTimeSec > 0, "limits.submission.max_total_wall_time_seconds must be > 0"},
		{c.Scheduler.MaxConcurrentSubmissions > 0, "scheduler.max_concurrent_submissions must be > 0"},
		{c.Scheduler.ExecutionSlots > 0, "scheduler.execution_slots must be > 0"},
		{c.Scheduler.MaxQueueWaitSec > 0, "scheduler.max_queue_wait_seconds must be > 0"},
		// Queue wait counts against the submission deadline.
		{c.Scheduler.MaxQueueWaitSec < c.Limits.Submission.MaxTotalWallTimeSec,
			"scheduler.max_queue_wait_seconds must be < limits.submission.max_total_wall_time_seconds"},
		{c.Languages.Path != "", "languages.path is required"},
		// A submission that cannot fit in the budget would block forever at admission.
		{c.Scheduler.MemoryBudgetMB >= c.Limits.Execution.MemoryMB,
			"scheduler.memory_budget_mb must be >= limits.execution.memory_mb"},
		// The response must be written before the server's write timeout.
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
