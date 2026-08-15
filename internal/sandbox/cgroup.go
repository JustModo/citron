package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// cgroup is one execution's resource container.
//
// The judge creates it, places the process into it at clone time, and reads the
// accounting back afterwards. Doing this here rather than delegating to nsjail buys
// three things: memory.peak counts exactly the pages this execution touched,
// memory.events says whether the kernel OOM-killed it, and cgroup.kill terminates
// every descendant at once — a process tree cannot outrun it the way it can outrun
// signalling a process group.
type cgroup struct {
	dir string
	fd  *os.File
}

func newCgroup(root, name string, mem judge.MemoryBytes, maxPIDs int) (*cgroup, error) {
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cgroup: %w", err)
	}
	c := &cgroup{dir: dir}

	if mem > 0 {
		if err := c.write("memory.max", strconv.FormatInt(int64(mem), 10)); err != nil {
			c.remove()
			return nil, err
		}
		// Without this a submission over its limit is swapped rather than killed,
		// which turns a memory bomb into a machine-wide slowdown.
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

func (c *cgroup) write(file, value string) error {
	if err := os.WriteFile(filepath.Join(c.dir, file), []byte(value), 0o644); err != nil {
		return fmt.Errorf("cgroup: writing %s: %w", file, err)
	}
	return nil
}

func (c *cgroup) read(file string) string {
	b, err := os.ReadFile(filepath.Join(c.dir, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// PeakMemory is the high-water mark of memory actually touched by this execution.
func (c *cgroup) PeakMemory() judge.MemoryBytes {
	if v, err := strconv.ParseInt(c.read("memory.peak"), 10, 64); err == nil {
		return judge.MemoryBytes(v)
	}
	// Pre-5.19 kernels have no memory.peak. current is a poor substitute after the
	// process has exited, but it is better than reporting zero.
	if v, err := strconv.ParseInt(c.read("memory.current"), 10, 64); err == nil {
		return judge.MemoryBytes(v)
	}
	return 0
}

// OOMKilled reports whether the kernel killed something for exceeding memory.max.
// This is what separates "ran out of memory" from an ordinary crash.
func (c *cgroup) OOMKilled() bool {
	for line := range strings.SplitSeq(c.read("memory.events"), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || (key != "oom_kill" && key != "oom_group_kill") {
			continue
		}
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// CPUTime is the total CPU consumed by every process in the cgroup, which is what a
// multi-threaded or forking submission actually costs.
func (c *cgroup) CPUTime() time.Duration {
	for line := range strings.SplitSeq(c.read("cpu.stat"), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || key != "usage_usec" {
			continue
		}
		if usec, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.Duration(usec) * time.Microsecond
		}
	}
	return 0
}

// Kill terminates every process in the cgroup atomically. A fork bomb cannot escape
// it: there is no window in which a new child lands outside the set being killed.
func (c *cgroup) Kill() error {
	return c.write("cgroup.kill", "1")
}

func (c *cgroup) Close() error {
	if c.fd != nil {
		_ = c.fd.Close()
	}
	return c.remove()
}

// remove retries briefly: the kernel refuses to remove a cgroup until the last
// process in it is fully reaped, which can lag the parent's wait by a moment.
func (c *cgroup) remove() error {
	var err error
	for range 50 {
		if err = os.Remove(c.dir); err == nil || os.IsNotExist(err) {
			return nil
		}
		_ = c.Kill()
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("cgroup: removing %s: %w", c.dir, err)
}

// CgroupAvailable reports whether root is a usable delegated cgroup v2 directory.
// The composition root calls this at startup so a misconfigured deployment fails
// loudly instead of running submissions with unenforced memory limits.
func CgroupAvailable(root string) error {
	probe := filepath.Join(root, "judge-probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		return fmt.Errorf("cgroup root %s is not writable: %w", root, err)
	}
	defer os.Remove(probe)

	for _, f := range []string{"memory.max", "pids.max"} {
		if _, err := os.Stat(filepath.Join(probe, f)); err != nil {
			return fmt.Errorf("cgroup root %s has no %s; controllers are not delegated", root, f)
		}
	}
	return nil
}
