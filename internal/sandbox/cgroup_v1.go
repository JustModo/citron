package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// managerV1 creates cgroups across separate v1 controller hierarchies.
type managerV1 struct {
	dirs map[string]string
}

func (*managerV1) Version() string { return "v1" }

func (m *managerV1) New(name string, mem judge.MemoryBytes, maxPIDs int) (cgroup, error) {
	c := &cgroupV1{dirs: make(map[string]string, len(m.dirs))}
	made := make(map[string]bool, len(m.dirs))
	for controller, parent := range m.dirs {
		dir := filepath.Join(parent, name)
		// Co-mounted controllers (cpu,cpuacct) share one directory.
		if !made[dir] {
			if err := os.Mkdir(dir, 0o755); err != nil {
				c.remove()
				return nil, fmt.Errorf("cgroup: %w", err)
			}
			made[dir] = true
		}
		c.dirs[controller] = dir
	}

	if mem > 0 {
		limit := strconv.FormatInt(int64(mem), 10)
		if err := c.write("memory", "memory.limit_in_bytes", limit); err != nil {
			c.remove()
			return nil, err
		}
		// Keeps swap from bypassing the limit; hosts without swap accounting fail here.
		if err := c.write("memory", "memory.memsw.limit_in_bytes", limit); err != nil {
			c.remove()
			return nil, err
		}
	}
	if maxPIDs > 0 {
		if err := c.write("pids", "pids.max", strconv.Itoa(maxPIDs)); err != nil {
			c.remove()
			return nil, err
		}
	}
	period := strconv.Itoa(cpuPeriodUS)
	if err := c.write("cpu", "cpu.cfs_period_us", period); err != nil {
		c.remove()
		return nil, err
	}
	if err := c.write("cpu", "cpu.cfs_quota_us", period); err != nil {
		c.remove()
		return nil, err
	}
	return c, nil
}

type cgroupV1 struct {
	dirs map[string]string
}

func (c *cgroupV1) write(controller, file, value string) error {
	return writeCgroupFile(c.dirs[controller], file, value)
}

func (c *cgroupV1) read(controller, file string) string {
	s, _ := readCgroupFile(c.dirs[controller], file)
	return s
}

func (*cgroupV1) Prepare(*exec.Cmd) {}

// Place moves pid into every controller; later forks inherit membership.
// v1 has no CLONE_INTO_CGROUP, so this runs after Start; the unaccounted gap
// covers only nsjail's setup, before the submission is exec'd.
func (c *cgroupV1) Place(pid int) error {
	for controller := range c.dirs {
		if err := c.write(controller, "cgroup.procs", strconv.Itoa(pid)); err != nil {
			if errors.Is(err, syscall.ESRCH) { // already exited
				return nil
			}
			return err
		}
	}
	return nil
}

func (c *cgroupV1) PeakMemory() judge.MemoryBytes {
	if v, ok := cgroupNumber(c.read("memory", "memory.max_usage_in_bytes")); ok {
		return judge.MemoryBytes(v)
	}
	if v, ok := cgroupNumber(c.read("memory", "memory.usage_in_bytes")); ok {
		return judge.MemoryBytes(v)
	}
	return 0
}

func (c *cgroupV1) OOMKilled() bool {
	n, ok := cgroupStat(c.read("memory", "memory.oom_control"), "oom_kill")
	return ok && n > 0
}

// cpuacct reports nanoseconds, where v2's cpu.stat reports microseconds.
func (c *cgroupV1) CPUTime() time.Duration {
	if ns, ok := cgroupNumber(c.read("cpuacct", "cpuacct.usage")); ok {
		return time.Duration(ns) * time.Nanosecond
	}
	return 0
}

// Kill SIGKILLs every member. v1 has no cgroup.kill, so it repeats until the
// cgroup is empty; pids.max bounds the tree and a mid-round fork is caught next round.
func (c *cgroupV1) Kill() error {
	dir, ok := c.dirs["pids"]
	if !ok {
		return nil
	}
	var err error
	for range 20 {
		var procs string
		if procs, err = readCgroupFile(dir, "cgroup.procs"); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("cgroup: reading cgroup.procs: %w", err)
		}
		if procs == "" {
			return nil
		}
		for field := range strings.FieldsSeq(procs) {
			if pid, convErr := strconv.Atoi(field); convErr == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("cgroup: processes still running in %s after kill", dir)
}

func (c *cgroupV1) Close() error { return c.remove() }

func (c *cgroupV1) remove() error {
	var firstErr error
	for controller, dir := range c.dirs {
		if err := removeCgroupDir(dir, c.Kill); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("cgroup: %s: %w", controller, err)
		}
	}
	return firstErr
}
