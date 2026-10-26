// Package cache implements the disk-backed response store that replaces the
// original project's Cloudflare Cache API edge cache. Entries are keyed by
// the canonical upstream GET URL, stored as body + sidecar metadata files,
// expired per the cache-policy TTL classes and evicted LRU-style when the
// total size budget is exceeded.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package cache

import (
        "crypto/rand"
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
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

        mu       sync.Mutex
        total    int64
        disabled bool
}

// Entry is a cached response ready to be served.
type Entry struct {
        Status int
        Header http.Header
        Body   string // file path
        Size   int64
}

type meta struct {
        Status int                 `json:"status"`
        Header map[string][]string `json:"header"`
        Expiry int64               `json:"expiry"` // unix seconds
        Size   int64               `json:"size"`
        Access int64               `json:"access"` // unix seconds, LRU clock
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
        return s
}

// Enabled reports whether the store accepts entries.
func (s *Store) Enabled() bool { return s != nil && !s.disabled }

// audit recomputes the total size and drops corrupt/partial files.
func (s *Store) audit() int64 {
        var total int64
        _ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
                if err != nil || d.IsDir() {
                        return nil
                }
                if strings.HasSuffix(path, ".tmp") {
                        _ = os.Remove(path)
                        return nil
                }
                if strings.HasSuffix(path, ".meta") {
                        var m meta
                        if raw, err := os.ReadFile(path); err == nil &&
                                json.Unmarshal(raw, &m) == nil && m.Size >= 0 {
                                total += m.Size
                                return nil
                        }
                        _ = os.Remove(path)
                        _ = os.Remove(strings.TrimSuffix(path, ".meta") + ".body")
                }
                return nil
        })
        return total
}

func (s *Store) paths(key string) (string, string) {
        sub := filepath.Join(s.dir, key[:2])
        return filepath.Join(sub, key+".body"), filepath.Join(sub, key+".meta")
}

// Get returns a fresh entry or nil (miss/expired/corrupt).
func (s *Store) Get(key string) *Entry {
        if !s.Enabled() {
                return nil
        }
        bodyPath, metaPath := s.paths(key)
        raw, err := os.ReadFile(metaPath)
        if err != nil {
                return nil
        }
        var m meta
        if json.Unmarshal(raw, &m) != nil || m.Size < 0 {
                _ = os.Remove(metaPath)
                _ = os.Remove(bodyPath)
                return nil
        }
        if time.Now().Unix() >= m.Expiry {
                _ = os.Remove(metaPath)
                _ = os.Remove(bodyPath)
                return nil
        }
        if _, err := os.Stat(bodyPath); err != nil {
                _ = os.Remove(metaPath)
                return nil
        }
        // LRU touch: rewrite the small sidecar with a new clock (best-effort).
        m.Access = time.Now().Unix()
        if updated, err := json.Marshal(m); err == nil {
                _ = os.WriteFile(metaPath, updated, 0o644)
        }
        h := http.Header{}
        for k, vv := range m.Header {
                for _, v := range vv {
                        h.Add(k, v)
                }
        }
        return &Entry{Status: m.Status, Header: h, Body: bodyPath, Size: m.Size}
}

// Writer streams a response body into the cache while the same bytes go to
// the client (tee). A nil *Writer is a safe no-op.
type Writer struct {
        s    *Store
        key  string
        f    *os.File
        size int64
        over bool
        done bool
}

// Begin starts a cache write for key with the given status/headers/TTL.
// Returns nil when the store is disabled — the nil Writer is usable.
func (s *Store) Begin(key string, status int, header http.Header, ttlSeconds int) *Writer {
        if !s.Enabled() {
                return nil
        }
        bodyPath, _ := s.paths(key)
        if err := os.MkdirAll(filepath.Dir(bodyPath), 0o755); err != nil {
                return nil
        }
        tmp := bodyPath + "." + randSuffix() + ".tmp"
        f, err := os.Create(tmp)
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
        m := meta{Status: status, Header: hdr, Expiry: time.Now().Unix() + int64(ttlSeconds)}
        raw, _ := json.Marshal(m)
        if _, err := f.Write(append(raw, '\n')); err != nil { // preamble line
                _ = f.Close()
                _ = os.Remove(tmp)
                return nil
        }
        return &Writer{s: s, key: key, f: f}
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
        if w.size == 0 {
                w.teardown()
                return
        }
        if err := w.f.Sync(); err != nil {
                w.teardown()
                return
        }
        tmp := w.f.Name()
        if err := w.f.Close(); err != nil {
                _ = os.Remove(tmp)
                return
        }
        bodyPath, metaPath := w.s.paths(w.key)
        if err := os.Rename(tmp, bodyPath); err != nil {
                _ = os.Remove(tmp)
                return
        }
        // Read the preamble meta back (first JSON line of the body file).
        f, err := os.Open(bodyPath)
        if err != nil {
                _ = os.Remove(bodyPath)
                return
        }
        var m meta
        err = json.NewDecoder(f).Decode(&m)
        f.Close()
        if err != nil || m.Status == 0 {
                _ = os.Remove(bodyPath)
                return
        }
        m.Size = w.size
        m.Access = time.Now().Unix()
        raw, _ := json.Marshal(m)
        if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
                _ = os.Remove(bodyPath)
                return
        }
        w.s.addSize(w.size)
        w.s.evict(w.key)
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
                name := w.f.Name()
                _ = w.f.Close()
                _ = os.Remove(name)
                w.f = nil
        }
}

func (s *Store) addSize(n int64) {
        s.mu.Lock()
        s.total += n
        s.mu.Unlock()
}

// evict drops least-recently-used entries until the budget is met.
func (s *Store) evict(protect string) {
        s.mu.Lock()
        total := s.total
        s.mu.Unlock()
        if total <= s.maxBytes {
                return
        }

        type item struct {
                key    string
                metaP  string
                bodyP  string
                size   int64
                access int64
        }
        var items []item
        _ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
                if err != nil || d.IsDir() || !strings.HasSuffix(path, ".meta") {
                        return nil
                }
                raw, err := os.ReadFile(path)
                if err != nil {
                        return nil
                }
                var m meta
                if json.Unmarshal(raw, &m) != nil {
                        return nil
                }
                key := strings.TrimSuffix(filepath.Base(path), ".meta")
                items = append(items, item{key, path, strings.TrimSuffix(path, ".meta") + ".body", m.Size, m.Access})
                return nil
        })
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

// OpenBody opens the cached body file for serving.
func (e *Entry) OpenBody() (*os.File, error) { return os.Open(e.Body) }
