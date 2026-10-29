// Package cache implements the disk-backed response store that replaces the
// original project's Cloudflare Cache API edge cache. Entries are keyed by
// the canonical upstream GET URL, stored as a versioned body file plus an
// atomically-replaced metadata sidecar, expired per the cache-policy TTL
// classes and evicted LRU-style when the total size budget is exceeded.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package cache

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FormatRange renders a Content-Range value "bytes a-b/total".
func FormatRange(a, b, total int64) string {
	return "bytes " + strconv.FormatInt(a, 10) + "-" + strconv.FormatInt(b, 10) +
		"/" + strconv.FormatInt(total, 10)
}

// Store is a disk-backed cache.
type Store struct {
	dir      string
	maxBytes int64
	maxFile  int64

	mu      sync.Mutex // guards total
	total   int64
	evictMu sync.Mutex // serializes Commit/evict/removal size accounting

	disabled bool
}

// Entry is a cached response ready to be served.
type Entry struct {
	Status int
	Header http.Header
	Body   string // file path
	Size   int64
	// Legacy marks entries written by older versions that stored a JSON
	// metadata preamble line at the start of the body file. New entries keep
	// the body raw and write the metadata to a separate sidecar.
	Legacy bool
}

type meta struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header"`
	Expiry int64               `json:"expiry"` // unix seconds
	Size   int64               `json:"size"`
	// Body is the body file name inside the shard directory. Empty for legacy
	// entries, which used <key>.body with an inline metadata preamble.
	Body string `json:"body,omitempty"`
}

// Key derives the cache key from the canonical upstream URL.
func Key(canonicalURL string) string {
	sum := sha256.Sum256([]byte(canonicalURL))
	return hex.EncodeToString(sum[:])
}

// Open prepares (and size-audits) the cache directory. An empty dir or an
// explicit "off"/"none" disables the store; use Enabled() to check.
func Open(dir string, maxTotalMB, maxFileMB int) *Store {
	if dir == "" || strings.EqualFold(dir, "off") || strings.EqualFold(dir, "none") {
		return &Store{disabled: true}
	}
	s := &Store{
		dir:      dir,
		maxBytes: int64(maxTotalMB) << 20,
		maxFile:  int64(maxFileMB) << 20,
	}
	if s.maxBytes <= 0 {
		s.maxBytes = 1 << 30
	}
	if s.maxFile <= 0 {
		s.maxFile = 512 << 20
	}
	_ = os.MkdirAll(dir, 0o755)
	s.total = s.audit()
	// Trim a pre-existing over-budget cache at startup; Commit only evicts
	// when new entries arrive.
	s.evict("")
	return s
}

// Enabled reports whether the store accepts entries. A zero-value Store has
// no directory and is treated as disabled so it can never write into the
// process working directory.
func (s *Store) Enabled() bool { return s != nil && !s.disabled && s.dir != "" }

func (s *Store) shardDir(key string) string {
	return filepath.Join(s.dir, key[:2])
}

func (s *Store) metaPath(key string) string {
	return filepath.Join(s.shardDir(key), key+".meta")
}

func (s *Store) legacyBodyPath(key string) string {
	return filepath.Join(s.shardDir(key), key+".body")
}

// bodyPath resolves the body file for an entry. Legacy entries have no Body
// field and use the fixed <key>.body name. The Body value must be a plain file
// name; a tampered sidecar cannot point outside the shard directory.
func (s *Store) bodyPath(key string, m meta) string {
	if m.Body != "" && m.Body != "." && m.Body != ".." && filepath.Base(m.Body) == m.Body {
		return filepath.Join(s.shardDir(key), m.Body)
	}
	return s.legacyBodyPath(key)
}

// audit recomputes the total size, drops corrupt/partial entries and removes
// orphan body files (e.g. left behind by a crash between body create and the
// metadata commit).
func (s *Store) audit() int64 {
	referenced := make(map[string]bool)
	var total int64
	_ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			_ = os.Remove(path)
			return nil
		}
		if !strings.HasSuffix(path, ".meta") {
			return nil
		}
		key := strings.TrimSuffix(filepath.Base(path), ".meta")
		raw, rerr := os.ReadFile(path)
		var m meta
		if rerr != nil || json.Unmarshal(raw, &m) != nil || m.Size < 0 {
			_ = os.Remove(path)
			_ = os.Remove(s.legacyBodyPath(key))
			return nil
		}
		bp := s.bodyPath(key, m)
		if _, statErr := os.Stat(bp); statErr != nil {
			_ = os.Remove(path)
			_ = os.Remove(bp)
			return nil
		}
		referenced[bp] = true
		total += m.Size
		return nil
	})
	_ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".body") && !referenced[path] {
			_ = os.Remove(path)
		}
		return nil
	})
	return total
}

// Get returns a fresh entry or nil (miss/expired/corrupt).
func (s *Store) Get(key string) *Entry {
	if !s.Enabled() {
		return nil
	}
	metaPath := s.metaPath(key)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var m meta
	if json.Unmarshal(raw, &m) != nil || m.Size < 0 {
		// Corrupt sidecar: drop it and the legacy body (a versioned body, if
		// any, is cleaned up by the next audit's orphan pass).
		s.drop(metaPath, s.legacyBodyPath(key), 0)
		return nil
	}
	bodyPath := s.bodyPath(key, m)
	if time.Now().Unix() >= m.Expiry {
		s.drop(metaPath, bodyPath, m.Size)
		return nil
	}
	if _, err := os.Stat(bodyPath); err != nil {
		s.drop(metaPath, bodyPath, m.Size)
		return nil
	}
	// LRU clock lives on the sidecar mtime; touching it never rewrites the
	// JSON, so a concurrent read can never observe a partially written sidecar.
	now := time.Now()
	_ = os.Chtimes(metaPath, now, now)
	h := http.Header{}
	for k, vv := range m.Header {
		for _, v := range vv {
			h.Add(k, v)
		}
	}
	return &Entry{Status: m.Status, Header: h, Body: bodyPath, Size: m.Size, Legacy: m.Body == ""}
}

// drop removes an entry's files and subtracts its size from the running total.
func (s *Store) drop(metaPath, bodyPath string, size int64) {
	s.evictMu.Lock()
	defer s.evictMu.Unlock()
	_ = os.Remove(metaPath)
	if bodyPath != "" {
		_ = os.Remove(bodyPath)
	}
	if size > 0 {
		s.mu.Lock()
		s.total -= size
		if s.total < 0 {
			s.total = 0
		}
		s.mu.Unlock()
	}
}

// Writer streams a response body into the cache while the same bytes go to
// the client (tee). A nil *Writer is a safe no-op.
type Writer struct {
	s        *Store
	key      string
	bodyName string
	bodyPath string
	f        *os.File
	size     int64
	over     bool
	done     bool

	status int
	header http.Header
	expiry int64
}

// Begin starts a cache write for key with the given status/headers/TTL.
// Returns nil when the store is disabled — the nil Writer is usable.
func (s *Store) Begin(key string, status int, header http.Header, ttlSeconds int) *Writer {
	if !s.Enabled() {
		return nil
	}
	dir := s.shardDir(key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	// Versioned body name: a refresh writes a new file, so readers holding the
	// previous metadata keep a consistent body until the sidecar is replaced.
	bodyName := key + "." + randSuffix() + ".body"
	bodyPath := filepath.Join(dir, bodyName)
	f, err := os.Create(bodyPath)
	if err != nil {
		return nil
	}
	hdr := http.Header{}
	for k, vv := range header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			hdr.Add(k, v)
		}
	}
	return &Writer{
		s: s, key: key, bodyName: bodyName, bodyPath: bodyPath, f: f,
		status: status, header: hdr, expiry: time.Now().Unix() + int64(ttlSeconds),
	}
}

var hopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-connection": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

func randSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Write tees one chunk. Cache write failures never break the client stream:
// once over the per-file cap (or on I/O error) the writer silently discards.
func (w *Writer) Write(p []byte) (int, error) {
	if w == nil || w.done {
		return len(p), nil
	}
	if !w.over {
		if w.size+int64(len(p)) > w.s.maxFile {
			w.disable()
			return len(p), nil
		}
		n, err := w.f.Write(p)
		w.size += int64(n)
		if err != nil {
			w.disable()
			return len(p), nil // swallow: the cache must not break the response
		}
	}
	return len(p), nil
}

// disable tears the writer down and marks it ineligible for Commit.
func (w *Writer) disable() {
	w.over = true
	w.teardown()
}

// Commit finalizes the entry. No-op for nil/over/done writers.
func (w *Writer) Commit() {
	if w == nil {
		return
	}
	if w.done || w.over || w.f == nil {
		w.done = true
		return
	}
	w.done = true
	if err := w.f.Sync(); err != nil {
		w.teardown()
		return
	}
	if err := w.f.Close(); err != nil {
		w.f = nil
		w.teardown()
		return
	}
	w.f = nil

	w.s.evictMu.Lock()
	defer w.s.evictMu.Unlock()

	metaPath := w.s.metaPath(w.key)
	// Read the previous sidecar (if any) so the refresh replaces its size and
	// removes the superseded body file instead of leaving an orphan behind.
	oldBodyPath := ""
	oldSize := int64(0)
	if rawOld, err := os.ReadFile(metaPath); err == nil {
		var om meta
		if json.Unmarshal(rawOld, &om) == nil {
			oldSize = om.Size
			oldBodyPath = w.s.bodyPath(w.key, om)
		}
	}

	m := meta{Status: w.status, Header: w.header, Expiry: w.expiry, Size: w.size, Body: w.bodyName}
	raw, err := json.Marshal(m)
	if err != nil {
		_ = os.Remove(w.bodyPath)
		return
	}
	if err := writeFileAtomic(metaPath, raw); err != nil {
		_ = os.Remove(w.bodyPath)
		return
	}

	if oldBodyPath != "" && oldBodyPath != w.bodyPath {
		_ = os.Remove(oldBodyPath)
	}
	// Update the running total incrementally (Commits are serialized by
	// evictMu) and only pay for the directory walk when the budget is
	// actually exceeded.
	w.s.mu.Lock()
	w.s.total += w.size - oldSize
	if w.s.total < 0 {
		w.s.total = 0
	}
	over := w.s.total > w.s.maxBytes
	w.s.mu.Unlock()
	if over {
		w.s.evictLocked(w.key)
	}
}

// Abort discards the partial entry.
func (w *Writer) Abort() {
	if w == nil {
		return
	}
	w.done = true
	w.teardown()
}

func (w *Writer) teardown() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if w.bodyPath != "" {
		_ = os.Remove(w.bodyPath)
	}
}

// writeFileAtomic writes data to path via a temporary file + rename so a
// concurrent reader never observes a partially written sidecar.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + "." + randSuffix() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// evict drops least-recently-used entries until the budget is met.
func (s *Store) evict(protect string) {
	s.evictMu.Lock()
	defer s.evictMu.Unlock()
	s.evictLocked(protect)
}

// evictLocked assumes evictMu is held. It recomputes the total from disk so
// drift from overwrites, expiries or partial failures cannot accumulate.
func (s *Store) evictLocked(protect string) {
	type item struct {
		key    string
		metaP  string
		bodyP  string
		size   int64
		access int64
	}
	var items []item
	var total int64
	_ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".meta") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		var m meta
		if json.Unmarshal(raw, &m) != nil {
			return nil
		}
		key := strings.TrimSuffix(filepath.Base(path), ".meta")
		items = append(items, item{
			key:    key,
			metaP:  path,
			bodyP:  s.bodyPath(key, m),
			size:   m.Size,
			access: info.ModTime().Unix(),
		})
		total += m.Size
		return nil
	})
	s.mu.Lock()
	s.total = total
	s.mu.Unlock()
	if total <= s.maxBytes {
		return
	}

	sort.Slice(items, func(i, j int) bool { return items[i].access < items[j].access })

	for _, it := range items {
		if total <= s.maxBytes {
			break
		}
		if it.key == protect {
			continue
		}
		if os.Remove(it.metaP) == nil {
			_ = os.Remove(it.bodyP)
			total -= it.size
		}
	}
	s.mu.Lock()
	s.total = total
	s.mu.Unlock()
}

// OpenBody opens the cached body file ready to serve exactly e.Size bytes.
// The second return value is the absolute file offset of the body start;
// callers must add it when seeking for Range slices. Legacy entries carry a
// JSON metadata preamble line that must be skipped; new entries start at 0.
func (e *Entry) OpenBody() (*os.File, int64, error) {
	f, err := os.Open(e.Body)
	if err != nil {
		return nil, 0, err
	}
	if !e.Legacy {
		return f, 0, nil
	}
	line, err := readFirstLine(f)
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	start := int64(len(line))
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, start, nil
}

// readFirstLine reads up to and including the first '\n' using a small
// buffer, then rewinds the file so a plain Seek can position it precisely.
func readFirstLine(f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 8192)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return line, nil
}
