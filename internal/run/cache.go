package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/JustModo/citron/internal/judge"
)

// CompileCache stores compiled artifacts on disk, keyed by language, source and
// compile argv. Failed compilations are cached too, so a resubmitted source with a
// compile error is not recompiled for every request.
//
// The cache is bounded by entry count and by total bytes, since it shares its
// filesystem with testcase workspaces. Entries in use by a running submission are
// pinned and never evicted.
type CompileCache struct {
	root       string
	maxEntries int
	maxBytes   int64

	sf     singleflight.Group
	mu     sync.Mutex
	pinned map[string]int
}

// NewCompileCache creates a cache rooted at root holding at most maxEntries entries
// and maxBytes bytes of artifacts.
func NewCompileCache(root string, maxEntries int, maxBytes int64) (*CompileCache, error) {
	if maxEntries <= 0 || maxBytes <= 0 {
		return nil, fmt.Errorf("compile cache: entry and byte limits must be positive")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("compile cache: %w", err)
	}
	return &CompileCache{root: root, maxEntries: maxEntries, maxBytes: maxBytes, pinned: map[string]int{}}, nil
}

// Key returns the cache key for a compilation. Every input that affects the output
// must be hashed here.
func Key(language judge.LanguageID, source []byte, argv []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1\x00%d\x00", language)
	h.Write(source)
	io.WriteString(h, "\x00")
	io.WriteString(h, strings.Join(argv, "\x1f"))
	return hex.EncodeToString(h.Sum(nil))
}

type cachedMeta struct {
	Success  bool          `json:"success"`
	Output   []byte        `json:"output"`
	Duration time.Duration `json:"duration"`
}

// Entry is a cached compilation: a directory of artifact files plus its result.
type Entry struct {
	Dir    string
	Result judge.CompileResult
	key    string
}

// Build returns the cached compilation for key, running build exactly once for
// concurrent callers that miss. build must place its artifacts in the directory it
// is given; that directory becomes the cache entry only if build returns no error.
// The returned entry is pinned against eviction until passed to Release.
func (c *CompileCache) Build(key string, build func(dir string) (judge.CompileResult, error)) (Entry, error) {
	c.mu.Lock()
	c.pinned[key]++
	c.mu.Unlock()
	e, err := c.get(key, build)
	if err != nil {
		c.Release(Entry{key: key})
		return Entry{}, err
	}
	e.key = key
	return e, nil
}

// Release unpins an entry returned by Build.
func (c *CompileCache) Release(e Entry) {
	if e.key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pinned[e.key]--; c.pinned[e.key] <= 0 {
		delete(c.pinned, e.key)
	}
}

func (c *CompileCache) get(key string, build func(dir string) (judge.CompileResult, error)) (Entry, error) {
	if e, ok := c.lookup(key); ok {
		return e, nil
	}
	// Only the singleflight leader's closure runs, so compiled stays false for waiters.
	compiled := false
	v, err, _ := c.sf.Do(key, func() (any, error) {
		// Re-check: a previous flight may have finished after the first lookup.
		if e, ok := c.lookup(key); ok {
			return e, nil
		}
		compiled = true
		return c.build(key, build)
	})
	if err != nil {
		return Entry{}, err
	}
	e := v.(Entry)
	// Cached means this caller did not compile: a disk hit or a shared in-flight build.
	e.Result.Cached = !compiled
	return e, nil
}

func (c *CompileCache) lookup(key string) (Entry, bool) {
	dir := filepath.Join(c.root, key)
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return Entry{}, false
	}
	var m cachedMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return Entry{}, false
	}
	_ = os.Chtimes(dir, time.Now(), time.Now()) // mtime drives LRU eviction
	return Entry{
		Dir: dir,
		Result: judge.CompileResult{
			Success:  m.Success,
			Output:   m.Output,
			Duration: m.Duration,
			Cached:   true,
		},
	}, true
}

const metaFile = ".citron-meta.json"

// build compiles into a staging directory and renames it into place as the entry.
func (c *CompileCache) build(key string, build func(dir string) (judge.CompileResult, error)) (Entry, error) {
	staging, err := os.MkdirTemp(c.root, "building-")
	if err != nil {
		return Entry{}, fmt.Errorf("compile cache: %w", err)
	}
	// The compiler runs as the jail's unprivileged uid and must write here; the
	// finished entry is made read-only to others.
	if err := os.Chmod(staging, 0o777); err != nil {
		_ = os.RemoveAll(staging)
		return Entry{}, fmt.Errorf("compile cache: %w", err)
	}

	result, err := build(staging)
	if err == nil {
		err = c.capEntrySize(staging, &result)
	}
	if err == nil {
		err = os.Chmod(staging, 0o755)
	}
	if err != nil {
		_ = os.RemoveAll(staging)
		return Entry{}, err
	}

	meta, err := json.Marshal(cachedMeta{
		Success: result.Success, Output: result.Output, Duration: result.Duration,
	})
	if err != nil {
		_ = os.RemoveAll(staging)
		return Entry{}, fmt.Errorf("compile cache: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, metaFile), meta, 0o644); err != nil {
		_ = os.RemoveAll(staging)
		return Entry{}, fmt.Errorf("compile cache: %w", err)
	}

	final := filepath.Join(c.root, key)
	// Rename is atomic, so readers never see a partial entry. If another process won
	// the race, its entry has identical content.
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		if e, ok := c.lookup(key); ok {
			return e, nil
		}
		return Entry{}, fmt.Errorf("compile cache: %w", err)
	}

	c.evict()
	return Entry{Dir: final, Result: result}, nil
}

// capEntrySize turns a compilation whose output exceeds a quarter of the byte budget
// into a failed one and discards its artifacts, so no single entry can crowd out the
// rest of the cache. The result is deterministic for the source, so it is cached.
func (c *CompileCache) capEntrySize(dir string, result *judge.CompileResult) error {
	limit := c.maxBytes / 4
	if dirSize(dir) <= limit {
		return nil
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("compile cache: %w", err)
	}
	for _, f := range files {
		if err := os.RemoveAll(filepath.Join(dir, f.Name())); err != nil {
			return fmt.Errorf("compile cache: %w", err)
		}
	}
	*result = judge.CompileResult{
		Output:   fmt.Appendf(nil, "compiled output exceeds %d MiB", limit>>20),
		Duration: result.Duration,
	}
	return nil
}

// evict drops unpinned entries, least recently used first, until the cache is
// within both its entry count and its byte budget.
func (c *CompileCache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	type aged struct {
		key  string
		at   time.Time
		size int64
	}
	var dirs []aged
	var total int64
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "building-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := dirSize(filepath.Join(c.root, e.Name()))
		total += size
		dirs = append(dirs, aged{e.Name(), info.ModTime(), size})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].at.Before(dirs[j].at) })
	count := len(dirs)
	for _, d := range dirs {
		if count <= c.maxEntries && total <= c.maxBytes {
			return
		}
		if c.pinned[d.key] > 0 {
			continue
		}
		if os.RemoveAll(filepath.Join(c.root, d.key)) == nil {
			count--
			total -= d.size
		}
	}
}

// dirSize sums the sizes of the regular files under dir without following links.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// CopyInto copies an entry's artifacts into a testcase workspace. Copying rather
// than sharing keeps each testcase's writable state private.
func CopyInto(entry Entry, dir string) error {
	files, err := os.ReadDir(entry.Dir)
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	for _, f := range files {
		if f.IsDir() || f.Name() == metaFile {
			continue
		}
		if err := copyFile(filepath.Join(entry.Dir, f.Name()), filepath.Join(dir, f.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("artifact: %w", err)
	}
	return out.Close()
}
