package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

type managerV2 struct{ root string }

func (*managerV2) Version() string { return "v2" }

func (m *managerV2) New(name string, mem judge.MemoryBytes, maxPIDs int) (cgroup, error) {
	dir := filepath.Join(m.root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cgroup: %w", err)
	}
	c := &cgroupV2{dir: dir}

	if mem > 0 {
		if err := c.write("memory.max", strconv.FormatInt(int64(mem), 10)); err != nil {
			c.remove()
			return nil, err
		}
		// Without this a memory bomb swaps instead of being OOM-killed.
		if err := c.write("memory.swap.max", "0"); err != nil && !os.IsNotExist(err) {
			c.remove()
			return nil, err
		}
	}
	if maxPIDs > 0 {
		if err := c.write("pids.max", strconv.Itoa(maxPIDs)); err != nil {
			c.remove()
			return nil, err
		}
	}

	fd, err := os.Open(dir)
	if err != nil {
		c.remove()
		return nil, fmt.Errorf("cgroup: %w", err)
	}
	c.fd = fd
	return c, nil
}

type cgroupV2 struct {
	dir string
	fd  *os.File
}

func (c *cgroupV2) write(file, value string) error { return writeCgroupFile(c.dir, file, value) }

func (c *cgroupV2) read(file string) string {
	s, _ := readCgroupFile(c.dir, file)
	return s
}

// Prepare uses CLONE_INTO_CGROUP so the child is accounted from clone onwards.
func (c *cgroupV2) Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(c.fd.Fd())}
}

func (*cgroupV2) Place(int) error { return nil }

// PeakMemory returns the cgroup's memory high-water mark.
func (c *cgroupV2) PeakMemory() judge.MemoryBytes {
	if v, ok := cgroupNumber(c.read("memory.peak")); ok {
		return judge.MemoryBytes(v)
	}
	// Pre-5.19 kernels lack memory.peak.
	if v, ok := cgroupNumber(c.read("memory.current")); ok {
		return judge.MemoryBytes(v)
	}
	return 0
}

// OOMKilled reports whether the kernel OOM-killed any member of the cgroup.
func (c *cgroupV2) OOMKilled() bool {
	events := c.read("memory.events")
	for _, key := range []string{"oom_kill", "oom_group_kill"} {
		if n, ok := cgroupStat(events, key); ok && n > 0 {
			return true
		}
	}
	return false
}

// CPUTime returns CPU time consumed by all members of the cgroup.
func (c *cgroupV2) CPUTime() time.Duration {
	if usec, ok := cgroupStat(c.read("cpu.stat"), "usage_usec"); ok {
		return time.Duration(usec) * time.Microsecond
	}
	return 0
}

// Kill SIGKILLs the whole cgroup atomically, so concurrent forks cannot escape.
func (c *cgroupV2) Kill() error {
	return c.write("cgroup.kill", "1")
}

func (c *cgroupV2) Close() error {
	if c.fd != nil {
		_ = c.fd.Close()
	}
	return c.remove()
}

func (c *cgroupV2) remove() error { return removeCgroupDir(c.dir, c.Kill) }
