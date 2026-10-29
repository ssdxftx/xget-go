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

// A standard request must forward the client's Accept-Encoding upstream and
// never hand a compressed body to a client that asked for identity.
func TestStandardRequestHonorsAcceptEncoding(t *testing.T) {
	var seen []string
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			ae := r.Header.Get("Accept-Encoding")
			seen = append(seen, ae)
			w.Header().Set("Content-Type", "text/plain")
			if strings.Contains(ae, "gzip") {
				body := gzipString("compressed")
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
			_, _ = io.WriteString(w, "plain")
		})
		return m
	}())

	identity := doGet(h, "/gist/ae-identity.txt", map[string]string{"Accept-Encoding": "identity"})
	if enc := identity.Header().Get("Content-Encoding"); enc != "" || identity.Body.String() != "plain" {
		t.Fatalf("identity client: encoding=%q body=%q", enc, identity.Body.String())
	}
	gz := doGet(h, "/gist/ae-gzip.txt", map[string]string{"Accept-Encoding": "gzip"})
	if enc := gz.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("gzip client: encoding=%q, want gzip", enc)
	}
	none := doGet(h, "/gist/ae-none.txt", nil)
	if enc := none.Header().Get("Content-Encoding"); enc != "" || none.Body.String() != "plain" {
		t.Fatalf("no-AE client: encoding=%q body=%q", enc, none.Body.String())
	}
	want := []string{"identity", "gzip", "identity"}
	if len(seen) != len(want) {
		t.Fatalf("upstream Accept-Encoding = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("upstream Accept-Encoding = %v, want %v", seen, want)
		}
	}
}

// A compressed upstream response is forwarded but never stored: the URL-only
// disk cache keeps identity representations only.
func TestCompressedResponseIsNotCached(t *testing.T) {
	hits := 0
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.Header().Set("Content-Type", "text/plain")
			if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(gzipString("compressed"))
				return
			}
			_, _ = io.WriteString(w, "plain")
		})
		return m
	}())

	if rec := doGet(h, "/gist/nc.txt", map[string]string{"Accept-Encoding": "gzip"}); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("prime encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	identity := doGet(h, "/gist/nc.txt", map[string]string{"Accept-Encoding": "identity"})
	if enc := identity.Header().Get("Content-Encoding"); enc != "" || identity.Body.String() != "plain" {
		t.Fatalf("identity after gzip: encoding=%q body=%q", enc, identity.Body.String())
	}
	if hits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (gzip response must not be cached)", hits)
	}
	cached := doGet(h, "/gist/nc.txt", nil)
	if hits != 2 {
		t.Fatalf("upstream hits after identity cache = %d, want 2", hits)
	}
	if !strings.Contains(cached.Header().Get("X-Performance-Metrics"), "cache_hit") {
		t.Fatalf("third request metrics = %q, want cache_hit", cached.Header().Get("X-Performance-Metrics"))
	}
}

// Legacy compressed cache entries are only served to clients that explicitly
// accept the stored coding.
func TestCacheEntryEncodingAcceptable(t *testing.T) {
	gzipEntry := &cache.Entry{Header: http.Header{"Content-Encoding": []string{"gzip"}}}
	identityReq := httptest.NewRequest("GET", "/", nil)
	identityReq.Header.Set("Accept-Encoding", "identity")
	if cacheEntryEncodingAcceptable(gzipEntry, identityReq) {
		t.Fatal("gzip entry accepted for an identity client")
	}
	gzipReq := httptest.NewRequest("GET", "/", nil)
	gzipReq.Header.Set("Accept-Encoding", "gzip, deflate")
	if !cacheEntryEncodingAcceptable(gzipEntry, gzipReq) {
		t.Fatal("gzip entry rejected for a gzip client")
	}
	plainEntry := &cache.Entry{Header: http.Header{}}
	if !cacheEntryEncodingAcceptable(plainEntry, identityReq) {
		t.Fatal("identity entry rejected for an identity client")
	}
}

// A non-protocol 304 passes through without the JSON error contract, but it
// still carries the security header set that every non-protocol response gets.
func TestNonProtocol304KeepsSecurityHeaders(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/gist/file.txt", nil)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY on a 304", got)
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("missing HSTS on a non-protocol 304")
	}
}

// The immutable-artifact checks must look at the target URL's pathname, not
// the full URL (the npm tarball regex is anchored at the path start).
func TestTargetPathnameEnablesAnchoredArtifactMatch(t *testing.T) {
	if got := targetPathname("https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz?x=1#f"); got != "/pkg/-/pkg-1.0.0.tgz" {
		t.Fatalf("targetPathname = %q", got)
	}
	if !isImmutableArtifactRequest("npm", "/other", "https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz") {
		t.Fatal("npm tarball in the target URL was not detected")
	}
}

// A present-but-empty "token" wins over "access_token", matching the official
// parser (the caller then treats the empty token as unusable).
func TestReadRegistryTokenPrecedence(t *testing.T) {
	if got := readRegistryToken(strings.NewReader(`{"token":"","access_token":"fallback"}`)); got != "" {
		t.Fatalf("empty token should win over access_token, got %q", got)
	}
	if got := readRegistryToken(strings.NewReader(`{"access_token":"fallback"}`)); got != "fallback" {
		t.Fatalf("access_token fallback = %q", got)
	}
}
