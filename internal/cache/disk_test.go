package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	return h
}

func writeEntry(t *testing.T, s *Store, key string, body []byte, ttl int) {
	t.Helper()
	w := s.Begin(key, http.StatusOK, testHeader(), ttl)
	if w == nil {
		t.Fatalf("Begin returned nil")
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Commit()
}

func readEntry(t *testing.T, s *Store, key string) string {
	t.Helper()
	e := s.Get(key)
	if e == nil {
		return ""
	}
	f, _, err := e.OpenBody()
	if err != nil {
		t.Fatalf("OpenBody: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(b)
}

func actualTotal(t *testing.T, s *Store) int64 {
	t.Helper()
	var total int64
	_ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".meta") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m meta
		if json.Unmarshal(raw, &m) == nil {
			total += m.Size
		}
		return nil
	})
	return total
}

func TestOverwriteDoesNotDoubleCount(t *testing.T) {
	s := Open(t.TempDir(), 16, 8)
	body := []byte("hello-world-1234567890")
	writeEntry(t, s, Key("u1"), body, 300)
	writeEntry(t, s, Key("u1"), body, 300)
	if s.total != int64(len(body)) {
		t.Fatalf("total = %d, want %d", s.total, len(body))
	}
	if got := actualTotal(t, s); got != s.total {
		t.Fatalf("actual total = %d, accounting = %d", got, s.total)
	}
	// The refreshed body is still served and the old versioned body is gone.
	if got := readEntry(t, s, Key("u1")); got != string(body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if n := countBodies(t, s); n != 1 {
		t.Fatalf("body files = %d, want 1 (superseded body not removed)", n)
	}
}

func TestExpiryDecrementsTotal(t *testing.T) {
	s := Open(t.TempDir(), 16, 8)
	body := []byte("abcdefghij")
	writeEntry(t, s, Key("u2"), body, 1)
	if s.total != int64(len(body)) {
		t.Fatalf("total before expiry = %d", s.total)
	}
	time.Sleep(1100 * time.Millisecond)
	if e := s.Get(Key("u2")); e != nil {
		t.Fatalf("expired entry still returned")
	}
	if s.total != 0 {
		t.Fatalf("total after expiry = %d, want 0", s.total)
	}
	if n := countBodies(t, s); n != 0 {
		t.Fatalf("body files after expiry = %d, want 0", n)
	}
}

func TestEmptyBodyIsCached(t *testing.T) {
	s := Open(t.TempDir(), 16, 8)
	writeEntry(t, s, Key("empty"), nil, 300)
	e := s.Get(Key("empty"))
	if e == nil {
		t.Fatalf("empty 200 entry was not cached")
	}
	if e.Size != 0 {
		t.Fatalf("size = %d, want 0", e.Size)
	}
}

func TestEvictionRecomputesAndRespectsBudget(t *testing.T) {
	s := Open(t.TempDir(), 1, 8) // 1 MiB budget
	payload := make([]byte, 400<<10)
	writeEntry(t, s, Key("a"), payload, 300)
	writeEntry(t, s, Key("b"), payload, 300)
	writeEntry(t, s, Key("c"), payload, 300)
	if s.total > s.maxBytes {
		t.Fatalf("total = %d, budget = %d", s.total, s.maxBytes)
	}
	if got := actualTotal(t, s); got != s.total {
		t.Fatalf("actual total = %d, accounting = %d", got, s.total)
	}
}

func TestOpenCleansOrphanBodiesAndTrimsBudget(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, 16, 8)
	writeEntry(t, s, Key("keep"), []byte("keep"), 300)
	// Simulate a crash between body create and metadata commit.
	orphan := filepath.Join(s.shardDir(Key("gone")), Key("gone")+".deadbeef.body")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	s2 := Open(dir, 16, 8)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan body was not cleaned up")
	}
	if got := readEntry(t, s2, Key("keep")); got != "keep" {
		t.Fatalf("valid entry lost on reopen: %q", got)
	}
}

func TestLegacyPreambleEntryStillReadable(t *testing.T) {
	dir := t.TempDir()
	key := Key("legacy")
	sub := filepath.Join(dir, key[:2])
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := "legacy-body"
	preamble := `{"status":200,"header":{"Content-Type":["text/plain"]},"expiry":` +
		strconv.FormatInt(time.Now().Unix()+300, 10) + `,"size":` + strconv.FormatInt(int64(len(payload)), 10) + "}\n"
	if err := os.WriteFile(filepath.Join(sub, key+".body"), []byte(preamble+payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, key+".meta"),
		[]byte(`{"status":200,"header":{"Content-Type":["text/plain"]},"expiry":`+
			strconv.FormatInt(time.Now().Unix()+300, 10)+`,"size":`+strconv.FormatInt(int64(len(payload)), 10)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, 16, 8)
	if got := readEntry(t, s, key); got != payload {
		t.Fatalf("legacy body = %q, want %q (preamble not skipped?)", got, payload)
	}
}

func countBodies(t *testing.T, s *Store) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".body") {
			n++
		}
		return nil
	})
	return n
}

func TestConcurrentCacheAccess(t *testing.T) {
	s := Open(t.TempDir(), 1, 1)
	body := bytes.Repeat([]byte("x"), 256<<10)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				key := Key(fmt.Sprintf("k%d", (worker+j)%6))
				w := s.Begin(key, 200, testHeader(), 300)
				if w != nil {
					_, _ = w.Write(body)
					w.Commit()
				}
				if e := s.Get(key); e != nil {
					if f, _, err := e.OpenBody(); err == nil {
						_, _ = io.Copy(io.Discard, f)
						_ = f.Close()
					}
				}
			}
		}(i)
	}
	wg.Wait()
	if got := actualTotal(t, s); got != s.total {
		t.Fatalf("actual total = %d, accounting = %d", got, s.total)
	}
}
