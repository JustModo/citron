//go:build security

// Package security checks that a hostile submission cannot reach past the sandbox.
//
// These run against a live citron, because the thing under test is the interaction
// between nsjail, the cgroup and the kernel — none of which a unit test can stand in
// for. Start one with:
//
//	make security
//
// Each case asserts a specific contained outcome. "Did not crash citron" is not a
// pass: a submission that reads /etc/shadow and exits 0 would satisfy that.
package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var citronURL = envOr("CITRON_URL", "http://127.0.0.1:2358")

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type testcaseResult struct {
	Status struct {
		ID          int    `json:"id"`
		Description string `json:"description"`
	} `json:"status"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	MemoryKB int64  `json:"memory_kb"`
}

type submissionResult struct {
	Status struct {
		Description string `json:"description"`
	} `json:"status"`
	Compile struct {
		Success bool   `json:"success"`
		Output  string `json:"output"`
	} `json:"compile"`
	Testcases []testcaseResult `json:"testcases"`
}

// submit runs one program and returns its single testcase result.
func submit(t *testing.T, languageID int, source string) (submissionResult, testcaseResult) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"language_id": languageID,
		"source_code": source,
		"testcases":   []map[string]string{{"stdin": "", "expected_output": "\x00never matches"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Post(citronURL+"/submissions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("citron unreachable at %s: %v", citronURL, err)
	}
	defer resp.Body.Close()

	var out submissionResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(out.Testcases) == 0 {
		return out, testcaseResult{}
	}
	return out, out.Testcases[0]
}

const (
	langC      = 50
	langPython = 71
)

// --- resource containment ---

func TestForkBombIsContained(t *testing.T) {
	start := time.Now()
	_, tc := submit(t, langPython, `
import os
while True:
    try:
        os.fork()
    except OSError:
        pass
`)
	elapsed := time.Since(start)

	if elapsed > 60*time.Second {
		t.Errorf("fork bomb ran for %v; pids.max did not contain it", elapsed)
	}
	if tc.Status.Description == "Accepted" {
		t.Error("fork bomb was accepted")
	}
	t.Logf("fork bomb contained in %v as %q", elapsed, tc.Status.Description)

	// Citron must still be serving afterwards.
	assertCitronAlive(t)
}

func TestMemoryBombIsKilledAtTheLimit(t *testing.T) {
	_, tc := submit(t, langPython, `
chunks = []
while True:
    chunks.append(bytearray(8 * 1024 * 1024))
`)
	if tc.Status.Description == "Accepted" {
		t.Fatal("memory bomb was accepted")
	}
	// 256 MB configured limit; allow generous slack for accounting granularity but
	// catch a limit that is not being applied at all.
	if tc.MemoryKB > 512*1024 {
		t.Errorf("peak memory %d KB far exceeds the 256 MB limit", tc.MemoryKB)
	}
	t.Logf("memory bomb: %q at %d KB", tc.Status.Description, tc.MemoryKB)
	assertCitronAlive(t)
}

func TestCPULoopHitsTheTimeLimit(t *testing.T) {
	start := time.Now()
	_, tc := submit(t, langPython, `
while True:
    pass
`)
	elapsed := time.Since(start)

	if !strings.Contains(tc.Status.Description, "Time Limit") {
		t.Errorf("status = %q, want a time limit verdict", tc.Status.Description)
	}
	if elapsed > 30*time.Second {
		t.Errorf("CPU loop ran for %v before being killed", elapsed)
	}
}

func TestOutputBombIsBounded(t *testing.T) {
	_, tc := submit(t, langPython, `
import sys
while True:
    sys.stdout.write("A" * 65536)
`)
	if len(tc.Stdout) > 4<<20 {
		t.Errorf("captured %d bytes of output; the limit is 1 MB", len(tc.Stdout))
	}
	if tc.Status.Description == "Accepted" {
		t.Error("output bomb was accepted")
	}
	t.Logf("output bomb: %q, %d bytes captured", tc.Status.Description, len(tc.Stdout))
	assertCitronAlive(t)
}

// Nothing bounds a file-writing loop except the tmpfs size and RLIMIT_FSIZE. Without
// them a submission fills the host disk, which takes down far more than citron.
func TestFileBombCannotFillTheDisk(t *testing.T) {
	_, tc := submit(t, langPython, `
with open("/tmp/fill", "wb") as f:
    while True:
        f.write(b"A" * (1024 * 1024))
`)
	if tc.Status.Description == "Accepted" {
		t.Error("file bomb was accepted")
	}
	t.Logf("file bomb: %q", tc.Status.Description)
	assertCitronAlive(t)
}

// --- isolation ---

func TestNoNetworkAccess(t *testing.T) {
	tests := []struct {
		name, target string
	}{
		{"internet", "1.1.1.1"},
		{"dns", "8.8.8.8"},
		{"aws metadata", "169.254.169.254"},
		{"loopback", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, tc := submit(t, langPython, fmt.Sprintf(`
import socket
s = socket.socket()
s.settimeout(3)
s.connect(("%s", 80))
print("CONNECTED")
`, tt.target))
			if strings.Contains(tc.Stdout, "CONNECTED") {
				t.Errorf("the sandbox reached %s", tt.target)
			}
		})
	}
}

// Citron's own API must be unreachable from inside a submission.
func TestCannotReachCitronItself(t *testing.T) {
	_, tc := submit(t, langPython, `
import urllib.request
print(urllib.request.urlopen("http://127.0.0.1:2358/languages", timeout=3).read()[:50])
`)
	if strings.Contains(tc.Stdout, "python") || strings.Contains(tc.Stdout, "id") {
		t.Errorf("a submission reached citron's own API: %q", tc.Stdout)
	}
}

// /proc/self/environ is deliberately absent: a process may always read its own
// environment, and the jail's PID namespace makes /proc/1/environ the submission
// itself. What matters is that the environment holds nothing worth reading, which
// TestEnvironmentCarriesNoSecrets covers.
func TestCannotReadSensitiveFiles(t *testing.T) {
	for _, path := range []string{
		"/etc/shadow",
		"/sys/fs/cgroup/cgroup.procs",
		"/opt/citron/configs/citron.conf",
		"/box/cache",
	} {
		t.Run(path, func(t *testing.T) {
			_, tc := submit(t, langPython, fmt.Sprintf(`
try:
    with open(%q, "rb") as f:
        data = f.read()
    print("READ", len(data), data[:80])
except Exception as e:
    print("DENIED", type(e).__name__)
`, path))
			if strings.Contains(tc.Stdout, "READ") {
				t.Errorf("submission read %s: %q", path, tc.Stdout)
			}
		})
	}
}

// Citron's configuration carries the auth token and internal paths. A submission
// must not be able to walk out of its workspace to find it.
func TestPathTraversalCannotEscapeTheWorkspace(t *testing.T) {
	_, tc := submit(t, langPython, `
import os
found = []
for depth in range(1, 8):
    p = "../" * depth + "opt/citron/configs/citron.conf"
    if os.path.exists(p):
        found.append(p)
print("FOUND" if found else "NONE", found)
`)
	if strings.Contains(tc.Stdout, "FOUND") {
		t.Errorf("path traversal reached citron's configuration: %q", tc.Stdout)
	}
}

func TestCannotSeeOtherWorkspaces(t *testing.T) {
	_, tc := submit(t, langPython, `
import os
try:
    entries = os.listdir("/box")
    print("LISTED", entries[:20])
except Exception as e:
    print("DENIED", type(e).__name__)
`)
	// /box inside the jail is this execution's own workspace. Seeing sibling
	// workspaces or the shared compile cache would mean the mount is wrong.
	for _, leak := range []string{"cache", "tc-", "building-"} {
		if strings.Contains(tc.Stdout, leak) {
			t.Errorf("a submission can see other executions' state (%q): %q", leak, tc.Stdout)
		}
	}
}

func TestCannotWriteToTheImage(t *testing.T) {
	_, tc := submit(t, langPython, `
for path in ["/usr/bin/pwned", "/etc/pwned", "/usr/lib/pwned"]:
    try:
        open(path, "w").write("x")
        print("WROTE", path)
    except Exception as e:
        print("DENIED", path, type(e).__name__)
`)
	if strings.Contains(tc.Stdout, "WROTE") {
		t.Errorf("a submission modified the image: %q", tc.Stdout)
	}
}

func TestRunsUnprivileged(t *testing.T) {
	_, tc := submit(t, langPython, `
import os
print("uid", os.getuid(), "gid", os.getgid())
`)
	if strings.Contains(tc.Stdout, "uid 0 ") {
		t.Errorf("submission runs as root: %q", tc.Stdout)
	}
}

func TestCannotMount(t *testing.T) {
	_, tc := submit(t, langC, `
#include <stdio.h>
#include <sys/mount.h>
int main(void) {
    if (mount("none", "/tmp/x", "tmpfs", 0, NULL) == 0) { printf("MOUNTED\n"); return 0; }
    printf("DENIED\n");
    return 0;
}
`)
	if strings.Contains(tc.Stdout, "MOUNTED") {
		t.Error("a submission created a mount")
	}
}

// A process that outlives its parent would hold the workspace open and keep burning
// CPU after the verdict is returned.
func TestDescendantsDoNotSurvive(t *testing.T) {
	_, tc := submit(t, langPython, `
import os, sys
pid = os.fork()
if pid == 0:
    os.setsid()
    while True:
        pass
print("parent exiting", flush=True)
sys.exit(0)
`)
	t.Logf("orphan test: %q", tc.Status.Description)

	// If the child survived it would still be consuming a CPU. A later submission
	// finishing promptly is the observable proof that it did not.
	start := time.Now()
	_, quick := submit(t, langPython, `print("still responsive")`)
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("a later submission took %v; an orphan is probably still running", d)
	}
	if quick.Status.Description != "Wrong Answer" && quick.Status.Description != "Accepted" {
		t.Errorf("citron unhealthy after the orphan test: %q", quick.Status.Description)
	}
}

func TestEnvironmentCarriesNoSecrets(t *testing.T) {
	_, tc := submit(t, langPython, `
import os
for k, v in os.environ.items():
    print(k, "=", v)
`)
	for _, leak := range []string{"TOKEN", "SECRET", "PASSWORD", "AWS_", "REDIS", "CITRON_"} {
		if strings.Contains(strings.ToUpper(tc.Stdout), leak) {
			t.Errorf("environment leaked %q: %q", leak, tc.Stdout)
		}
	}
}

func assertCitronAlive(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(citronURL + "/health")
	if err != nil {
		t.Fatalf("citron is not responding after the attack: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health returned %d after the attack", resp.StatusCode)
	}
}
