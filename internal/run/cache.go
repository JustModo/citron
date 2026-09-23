package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
type CompileCache struct {
	root       string
	maxEntries int

	sf singleflight.Group
	mu sync.Mutex
}

// NewCompileCache creates a cache rooted at root holding at most maxEntries entries
// (128 if maxEntries is not positive).
func NewCompileCache(root string, maxEntries int) (*CompileCache, error) {
	if maxEntries <= 0 {
		maxEntries = 128
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("compile cache: %w", err)
	}
	return &CompileCache{root: root, maxEntries: maxEntries}, nil
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
}

// Build returns the cached compilation for key, running build exactly once for
// concurrent callers that miss. build must place its artifacts in the directory it
// is given; that directory becomes the cache entry only if build returns no error.
func (c *CompileCache) Build(key string, build func(dir string) (judge.CompileResult, error)) (Entry, error) {
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

// evict drops the least recently used entries once the cache exceeds its size.
func (c *CompileCache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	type aged struct {
		path string
		at   time.Time
	}
	var dirs []aged
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "building-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, aged{filepath.Join(c.root, e.Name()), info.ModTime()})
	}
	if len(dirs) <= c.maxEntries {
		return
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].at.Before(dirs[j].at) })
	for _, d := range dirs[:len(dirs)-c.maxEntries] {
		_ = os.RemoveAll(d.path)
	}
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
