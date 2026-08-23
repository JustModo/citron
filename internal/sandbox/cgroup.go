package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// cgroup is one execution's resource container: its limits, its accounting, and
// the kill that takes the whole process tree with it.
type cgroup interface {
	Prepare(cmd *exec.Cmd)
	Place(pid int) error

	CPUTime() time.Duration
	PeakMemory() judge.MemoryBytes
	OOMKilled() bool

	Kill() error
	Close() error
}

// cgroupManager creates one cgroup per execution. Resolved once at startup.
type cgroupManager interface {
	New(name string, mem judge.MemoryBytes, maxPIDs int) (cgroup, error)
	Version() string
}

// v1Hierarchies are the controllers a v1 host must delegate, each with a file that
// proves the controller is attached to the hierarchy it was found on.
var v1Hierarchies = []struct{ controller, probeFile string }{
	{"memory", "memory.limit_in_bytes"},
	{"pids", "pids.max"},
	{"cpuacct", "cpuacct.usage"},
}

// newCgroupManager picks a backend for this host, preferring v2. It runs at startup
// so a misconfigured deployment fails loudly rather than leaving limits unenforced.
func newCgroupManager(root string) (cgroupManager, error) {
	mounts, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, fmt.Errorf("cgroup: %w", err)
	}
	defer mounts.Close()
	unified, v1, err := parseCgroupMounts(mounts)
	if err != nil {
		return nil, fmt.Errorf("cgroup: reading mounts: %w", err)
	}

	var v2Err error
	if unified != "" && within(unified, root) {
		if v2Err = probeCgroup(root, "memory.max", "pids.max", "cpu.stat"); v2Err == nil {
			return &managerV2{root: root}, nil
		}
	}

	name := filepath.Base(root)
	dirs := make(map[string]string, len(v1Hierarchies))
	for _, h := range v1Hierarchies {
		mount, ok := v1[h.controller]
		if !ok {
			return nil, cgroupUnavailable(root, v2Err,
				fmt.Errorf("no cgroup v1 %s mount", h.controller))
		}
		dir := filepath.Join(mount, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, cgroupUnavailable(root, v2Err, err)
		}
		if err := probeCgroup(dir, h.probeFile); err != nil {
			return nil, cgroupUnavailable(root, v2Err, err)
		}
		dirs[h.controller] = dir
	}
	return &managerV1{dirs: dirs}, nil
}

func cgroupUnavailable(root string, v2Err, v1Err error) error {
	if v2Err == nil {
		v2Err = fmt.Errorf("%s is not under a cgroup2 mount", root)
	}
	return fmt.Errorf("cgroup: no usable hierarchy: v2: %w; v1: %w", v2Err, v1Err)
}

// probeCgroup creates a real child group: a delegated parent is indistinguishable
// from an undelegated one until you look inside one of its children.
func probeCgroup(dir string, files ...string) error {
	probe := filepath.Join(dir, "citron-probe")
	if err := os.Mkdir(probe, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("cgroup root %s is not writable: %w", dir, err)
	}
	defer os.Remove(probe)

	for _, f := range files {
		if _, err := os.Stat(filepath.Join(probe, f)); err != nil {
			return fmt.Errorf("cgroup root %s has no %s; controllers are not delegated", dir, f)
		}
	}
	return nil
}

// parseCgroupMounts reports the cgroup2 mountpoint, if any, and where each v1
// controller is mounted. One v1 mount can carry several ("cpu,cpuacct").
func parseCgroupMounts(r io.Reader) (unified string, v1 map[string]string, err error) {
	v1 = map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		point := unescapeMount(f[1])
		switch f[2] {
		case "cgroup2":
			if unified == "" {
				unified = point
			}
		case "cgroup":
			for opt := range strings.SplitSeq(f[3], ",") {
				if _, seen := v1[opt]; !seen {
					v1[opt] = point
				}
			}
		}
	}
	return unified, v1, sc.Err()
}

func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readCgroupFile surfaces read errors: a backend pointed at the wrong filenames
// must not look like a submission that used no memory.
func readCgroupFile(dir, file string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// cgroupNumber parses a control file holding a single integer.
func cgroupNumber(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// cgroupStat pulls one "key value" field out of a multi-line control file.
func cgroupStat(content, key string) (int64, bool) {
	for line := range strings.SplitSeq(content, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || k != key {
			continue
		}
		return cgroupNumber(v)
	}
	return 0, false
}

func writeCgroupFile(dir, file, value string) error {
	if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0o644); err != nil {
		return fmt.Errorf("cgroup: writing %s: %w", file, err)
	}
	return nil
}

// removeCgroupDir retries: the kernel refuses removal until the last process in the
// cgroup is fully reaped, which can lag the parent's wait.
func removeCgroupDir(dir string, kill func() error) error {
	var err error
	for range 50 {
		if err = os.Remove(dir); err == nil || os.IsNotExist(err) {
			return nil
		}
		_ = kill()
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("cgroup: removing %s: %w", dir, err)
}
