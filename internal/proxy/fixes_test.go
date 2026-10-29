// Regression tests for the refactor-audit fixes: cache accounting/Vary,
// bounded range spooling, Docker sub-request timeouts, compressed error
// bodies, sensitive PyPI cache headers, forwarded-proto validation and the
// remaining edge cases.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"xget/internal/cache"
	"xget/internal/platform"
)

// overridePlatform points a platform at a test server for the duration of the
// test and restores the catalog afterwards.
func overridePlatform(t *testing.T, key, base string) {
	t.Helper()
	orig := platform.Catalog[key]
	platform.Catalog[key] = base
	platform.RefreshIndex()
	t.Cleanup(func() {
		platform.Catalog[key] = orig
		platform.RefreshIndex()
	})
}

func newUpstream(t *testing.T, fn http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	return srv
}

// A response with Vary: Origin must never be shared between client origins.
func TestCacheHonorsVaryOrigin(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Vary", "Origin")
			_, _ = io.WriteString(w, "origin="+r.Header.Get("Origin"))
		})
		return m
	}())

	a := doGet(h, "/gist/vary.txt", map[string]string{"Origin": "https://a.example"})
	b := doGet(h, "/gist/vary.txt", map[string]string{"Origin": "https://b.example"})
	if got := a.Body.String(); got != "origin=https://a.example" {
		t.Fatalf("origin A body = %q", got)
	}
	if got := b.Body.String(); got != "origin=https://b.example" {
		t.Fatalf("origin B body = %q, want a fresh fetch (cross-origin cache hit)", got)
	}
	if strings.Contains(b.Header().Get("X-Performance-Metrics"), "cache_hit") {
		t.Fatalf("Vary: Origin response was served from cache: %q", b.Header().Get("X-Performance-Metrics"))
	}
	if cc := b.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store for a Vary: Origin response", cc)
	}
}

// A sensitive PyPI rewrite keeps private/no-store and the Vary header.
func TestSensitivePypiKeepsPrivateCacheControl(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<a href="https://files.pythonhosted.org/packages/x/y.whl">y</a>`)
	})
	overridePlatform(t, "pypi", up.URL)
	h := testHandler(t, http.NewServeMux())

	rec := doGet(h, "/pypi/simple/demo/", map[string]string{"Authorization": "Bearer secret"})
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", cc)
	}
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Authorization") {
		t.Fatalf("Vary = %q, want Authorization/Cookie", vary)
	}
}

// Compressed upstream error pages must be decoded before they are embedded in
// the JSON error contract.
func TestCompressedUpstreamErrorIsDecoded(t *testing.T) {
	const payload = "REAL-UPSTREAM-ERROR-TEXT"
	compressed := gzipString(payload)
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(compressed)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/gist/f.bin", nil)
	if !strings.Contains(rec.Body.String(), payload) {
		t.Fatalf("error body = %q, want the decoded upstream text", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\ufffd") {
		t.Fatalf("error body contains replacement characters (binary leaked): %q", rec.Body.String())
	}
}

// Docker redirects are covered by TIMEOUT_SECONDS again.
func TestDockerRedirectRespectsTimeout(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/slow")
			w.WriteHeader(http.StatusFound)
		})
		m.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(3 * time.Second):
				_, _ = io.WriteString(w, "BLOB")
			case <-r.Context().Done():
			}
		})
		return m
	}())
	h.cfg.TimeoutSecond = 1

	start := time.Now()
	rec := doGet(h, "/cr/quay/v2/repo/blobs/sha256:abc", nil)
	elapsed := time.Since(start)
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", rec.Code)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("redirect follow took %v; TIMEOUT_SECONDS was not applied", elapsed)
	}
}

// bytes=-0 is unsatisfiable and must return 416 (not the full resource).
func TestSuffixZeroRangeIs416(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10")
			_, _ = io.WriteString(w, "0123456789")
		})
		return m
	}())
	if rec := doGet(h, "/gist/f.bin", nil); rec.Code != http.StatusOK {
		t.Fatalf("prime status = %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/gist/f.bin", nil)
	req.Header.Set("Range", "bytes=-0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", rec.Code)
	}
}

// The literal platform-key spelling keeps the path instead of slicing by
// prefix length (the original leaves it untouched).
func TestBareLiteralPlatformKeyKeepsPath(t *testing.T) {
	var gotPath string
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	})
	overridePlatform(t, "pypi-files", up.URL)
	h := testHandler(t, http.NewServeMux())

	rec := doGet(h, "/pypi-files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPath != "/pypi-files" {
		t.Fatalf("upstream path = %q, want /pypi-files", gotPath)
	}
}

// Invalid UTF-8 percent escapes are rejected like decodeURIComponent.
func TestInvalidUTF8PercentSequenceRejected(t *testing.T) {
	h := testHandler(t, http.NewServeMux())
	rec := doGet(h, "/gist/%FF", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A non-protocol 304 passes through without being wrapped in a JSON error.
func TestNonProtocol304PassesThrough(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/gist/f.bin", map[string]string{"If-None-Match": `"x"`})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Upstream server error") {
		t.Fatalf("304 was wrapped: %q", rec.Body.String())
	}
}

// Empty 200 responses are cached now.
func TestEmpty200IsCached(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
		})
		return m
	}())
	if rec := doGet(h, "/gist/empty", nil); rec.Code != http.StatusOK {
		t.Fatalf("prime status = %d", rec.Code)
	}
	rec := doGet(h, "/gist/empty", nil)
	if !strings.Contains(rec.Header().Get("X-Performance-Metrics"), "cache_hit") {
		t.Fatalf("second request was not a cache hit: %q", rec.Header().Get("X-Performance-Metrics"))
	}
}

// The over-cap range-after-full-cache fallback still streams the complete 200.
func TestRangeAfterFullCacheOverCapReplaysFullBody(t *testing.T) {
	payload := strings.Repeat("A", 2<<20) // 2 MiB
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())
	h.cfg.CacheMaxFileMB = 1
	h.store = cache.Open(t.TempDir(), 16, 1) // per-file cap 1 MiB

	req := httptest.NewRequest("GET", "/gist/big.bin", nil)
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 fallback", rec.Code)
	}
	if rec.Body.Len() != len(payload) {
		t.Fatalf("body length = %d, want %d (spool truncated the response)", rec.Body.Len(), len(payload))
	}
}

// An entry whose body file vanished is reported as "not served" so the caller
// can fall back to the upstream instead of writing a 502.
func TestServeFromCacheMissingBodyReturnsFalse(t *testing.T) {
	h := testHandler(t, http.NewServeMux())
	entry := &cache.Entry{Status: http.StatusOK, Header: http.Header{}, Body: "/nonexistent/xget-body", Size: 10}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/gist/x", nil)
	if h.serveFromCache(rec, req, entry, newPerformanceMonitor(), "cache_hit", false) {
		t.Fatalf("serveFromCache returned true for a missing body")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("serveFromCache wrote a body for a missing entry: %q", rec.Body.String())
	}
}

// A non-http(s) X-Forwarded-Proto must not leak into rewritten bodies.
func TestInvalidForwardedProtoNotUsedInRewrite(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"dist":{"tarball":"https://registry.npmjs.org/foo/-/foo-1.0.0.tgz"}}`)
	})
	overridePlatform(t, "npm", up.URL)
	h := testHandler(t, http.NewServeMux())

	rec := doGet(h, "/npm/foo", map[string]string{"X-Forwarded-Proto": "javascript"})
	if strings.Contains(rec.Body.String(), "javascript://") {
		t.Fatalf("rewritten body used an invalid forwarded proto: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "http://example.com/npm/foo") {
		t.Fatalf("rewritten body = %q, want http://example.com/npm/foo", rec.Body.String())
	}
}
