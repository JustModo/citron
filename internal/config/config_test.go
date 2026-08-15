package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shipped config must load and validate. This catches a config/struct drift
// that no unit test on Default() would notice.
func TestShippedConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "citron.conf"))
	if err != nil {
		t.Fatalf("configs/citron.conf does not load: %v", err)
	}
	if cfg.Sandbox.Driver != "nsjail" {
		t.Errorf("shipped config must default to the isolating driver, got %q", cfg.Sandbox.Driver)
	}
	if cfg.Server.Address == "0.0.0.0:2358" {
		t.Error("shipped config must not bind to a public interface")
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "citron.conf")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOverlaysDefaults(t *testing.T) {
	cfg, err := Load(write(t, `
[server]
address = "0.0.0.0:9999"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Address != "0.0.0.0:9999" {
		t.Errorf("override not applied: %q", cfg.Server.Address)
	}
	if cfg.Limits.Execution.MemoryMB != 256 {
		t.Errorf("default not preserved: got %d", cfg.Limits.Execution.MemoryMB)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	_, err := Load(write(t, "[server]\nadress = \"typo\"\n"))
	if err == nil {
		t.Fatal("expected an error for a misspelled key")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.conf")); err == nil {
		t.Fatal("a missing config must fail loudly, not fall back to defaults")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"local driver needs opt-in", func(c *Config) {
			c.Sandbox.Driver = "local"
		}, "allow_unsafe_local"},
		{"local driver with opt-in is allowed", func(c *Config) {
			c.Sandbox.Driver = "local"
			c.Sandbox.AllowUnsafeLocal = true
		}, ""},
		{"unknown sandbox driver", func(c *Config) { c.Sandbox.Driver = "docker" }, "sandbox.driver"},
		{"redis driver needs a url", func(c *Config) { c.Queue.Driver = "redis" }, "redis_url"},
		{"memory budget below one execution deadlocks admission", func(c *Config) {
			c.Scheduler.MemoryBudgetMB = 128
			c.Limits.Execution.MemoryMB = 256
		}, "memory_budget_mb"},
		{"submission deadline above write timeout", func(c *Config) {
			c.Limits.Submission.MaxTotalWallTimeSec = 120
			c.Server.WriteTimeoutSec = 60
		}, "max_total_wall_time_seconds"},
		{"zero cpu limit", func(c *Config) { c.Limits.Execution.CPUTimeSec = 0 }, "limits.execution"},
		{"zero attempts", func(c *Config) { c.Jobs.MaxAttempts = 0 }, "max_attempts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mut(&cfg)
			err := cfg.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("expected valid, got %v", err)
			case tt.want == "":
				return
			case err == nil:
				t.Fatalf("expected an error mentioning %q, got nil", tt.want)
			case !strings.Contains(err.Error(), tt.want):
				t.Errorf("error %q should mention %q", err, tt.want)
			}
		})
	}
}

func TestLimitConversion(t *testing.T) {
	cfg := Default()
	l := cfg.ExecutionLimits()
	if l.CPUTime != 2*time.Second {
		t.Errorf("cpu time = %v, want 2s", l.CPUTime)
	}
	if l.CPUExtraTime != 500*time.Millisecond {
		t.Errorf("fractional seconds lost: %v", l.CPUExtraTime)
	}
	if l.Memory.MB() != 256 {
		t.Errorf("memory = %d MB, want 256", l.Memory.MB())
	}
	if l.MaxStdout != 1<<20 {
		t.Errorf("stdout = %d, want 1 MiB", l.MaxStdout)
	}
	if err := l.Validate(); err != nil {
		t.Errorf("default execution limits invalid: %v", err)
	}
	if err := cfg.CompileLimits().Validate(); err != nil {
		t.Errorf("default compile limits invalid: %v", err)
	}
}
