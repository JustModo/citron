package sandbox

import (
	"strings"
	"testing"
)

// Picking the wrong hierarchy fails silently: limits nothing enforces, zeros read
// back. This parser is where that decision is made.
func TestParseCgroupMounts(t *testing.T) {
	const v2Only = `
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec,relatime,nsdelegate 0 0
`
	const v1Only = `
tmpfs /sys/fs/cgroup tmpfs ro,nosuid,nodev,noexec,mode=755 0 0
cgroup /sys/fs/cgroup/memory cgroup rw,nosuid,nodev,noexec,relatime,memory 0 0
cgroup /sys/fs/cgroup/cpu,cpuacct cgroup rw,nosuid,nodev,noexec,relatime,cpu,cpuacct 0 0
cgroup /sys/fs/cgroup/pids cgroup rw,nosuid,nodev,noexec,relatime,pids 0 0
`
	// Hybrid: a controllerless cgroup2 mount off to one side, controllers on v1.
	const hybrid = v1Only + `
cgroup2 /sys/fs/cgroup/unified cgroup2 rw,nosuid,nodev,noexec,relatime 0 0
`
	// Mountpoints with spaces are octal-escaped by the kernel.
	const escaped = `
cgroup /sys/fs/my\040cgroups/memory cgroup rw,relatime,memory 0 0
`

	tests := []struct {
		name        string
		mounts      string
		wantUnified string
		wantV1      map[string]string
	}{
		{"v2 only", v2Only, "/sys/fs/cgroup", nil},
		{"v1 only", v1Only, "", map[string]string{
			"memory":  "/sys/fs/cgroup/memory",
			"pids":    "/sys/fs/cgroup/pids",
			"cpu":     "/sys/fs/cgroup/cpu,cpuacct",
			"cpuacct": "/sys/fs/cgroup/cpu,cpuacct",
		}},
		{"hybrid", hybrid, "/sys/fs/cgroup/unified", map[string]string{
			"memory":  "/sys/fs/cgroup/memory",
			"pids":    "/sys/fs/cgroup/pids",
			"cpuacct": "/sys/fs/cgroup/cpu,cpuacct",
		}},
		{"escaped mountpoint", escaped, "", map[string]string{
			"memory": "/sys/fs/my cgroups/memory",
		}},
		{"none", "proc /proc proc rw 0 0\n", "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unified, v1, err := parseCgroupMounts(strings.NewReader(tc.mounts))
			if err != nil {
				t.Fatalf("parseCgroupMounts: %v", err)
			}
			if unified != tc.wantUnified {
				t.Errorf("unified = %q, want %q", unified, tc.wantUnified)
			}
			for controller, want := range tc.wantV1 {
				if got := v1[controller]; got != want {
					t.Errorf("v1[%q] = %q, want %q", controller, got, want)
				}
			}
			if tc.wantV1 == nil && len(v1) > 0 {
				// Ordinary mount options land in the map too; only ours matter.
				for _, h := range v1Hierarchies {
					if got, ok := v1[h.controller]; ok {
						t.Errorf("v1[%q] = %q, want no v1 hierarchy", h.controller, got)
					}
				}
			}
		})
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		root, path string
		want       bool
	}{
		{"/sys/fs/cgroup", "/sys/fs/cgroup/citron", true},
		{"/sys/fs/cgroup", "/sys/fs/cgroup", true},
		{"/sys/fs/cgroup/unified", "/sys/fs/cgroup/citron", false},
		{"/sys/fs/cgroup", "/sys/fs/cgroup-other/citron", false},
		{"/sys/fs/cgroup", "/tmp/citron", false},
	}
	for _, tc := range tests {
		if got := within(tc.root, tc.path); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}

func TestCgroupStat(t *testing.T) {
	const events = "low 0\nhigh 0\nmax 3\noom 1\noom_kill 2\n"
	const cpu = "usage_usec 12345\nuser_usec 9000\nsystem_usec 3345\n"
	const oomControl = "oom_kill_disable 0\nunder_oom 0\noom_kill 4\n"

	tests := []struct {
		name, content, key string
		want               int64
		wantOK             bool
	}{
		{"v2 oom_kill", events, "oom_kill", 2, true},
		{"v2 oom is not oom_kill", events, "oom_group_kill", 0, false},
		{"v2 cpu usage", cpu, "usage_usec", 12345, true},
		{"v1 oom_kill", oomControl, "oom_kill", 4, true},
		{"v1 oom_kill_disable is not a prefix match", oomControl, "oom_kil", 0, false},
		{"missing key", cpu, "nope", 0, false},
		{"empty file", "", "usage_usec", 0, false},
	}
	for _, tc := range tests {
		got, ok := cgroupStat(tc.content, tc.key)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: cgroupStat(_, %q) = %d, %v; want %d, %v",
				tc.name, tc.key, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCgroupNumber(t *testing.T) {
	tests := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{"1048576", 1048576, true},
		{"0", 0, true},
		{"max", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		got, ok := cgroupNumber(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("cgroupNumber(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
