// Regression tests for parity bugs found during the port audit:
// Docker redirect context handling, streaming flushes, compressed cache
// entries vs Range, cached 416 framing and the range-after-full-cache
// fallback header set.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"bytes"
	"compress/gzip"
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

// A Docker registry that answers a blob request with a direct 3xx (no prior
// 401) must have the redirect followed. The redirect used to run on the
// per-attempt context that cancelBody.Close had already canceled.
func TestDockerPrimaryRedirectFollowed(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/blob")
			w.WriteHeader(http.StatusFound)
		})
		m.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "BLOBDATA")
		})
		return m
	}())

	rec := doGet(h, "/cr/quay/v2/repo/blobs/sha256:abc", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "BLOBDATA" {
		t.Fatalf("docker redirect: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// If the redirect chase after an anonymous-token retry fails, the port must
// fall back to the synthesized 401 challenge (the official try/catch path)
// instead of dereferencing the nil response.
func TestDockerAnonymousRetryRedirectFailureReturnsChallenge(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="x"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Location", "http://127.0.0.1:1/unreachable")
		w.WriteHeader(http.StatusFound)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	h := testHandler(t, mux)
	h.client = srv.Client()
	h.dockerClient = &http.Client{
		Transport:     srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	rec := doGet(h, "/cr/quay/v2/repo/blobs/sha256:abc", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want synthesized 401 challenge, got %d %q", rec.Code, rec.Body.String())
	}
}

func gzipString(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

// A full response cached in gzip form must not be byte-sliced for a later
// Range request: the cached bytes are the compressed stream.
func TestRangeOnCompressedCachedEntryNotSliced(t *testing.T) {
	payload := strings.Repeat("0123456789", 100)
	compressed := gzipString(payload)
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") == "" && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", strconv.Itoa(len(compressed)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(compressed)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())

	if rec := doGet(h, "/gist/file.txt", nil); rec.Code != http.StatusOK {
		t.Fatalf("prime status = %d", rec.Code)
	}

	req := httptest.NewRequest("GET", "/gist/file.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusPartialContent {
		if rec.Body.String() != "0123456789" {
			t.Fatalf("range body = %q, want the identity slice", rec.Body.String())
		}
		if rec.Header().Get("Content-Encoding") == "gzip" {
			t.Fatalf("partial response carries gzip encoding")
		}
	}

	// The Range fetch above replaced the compressed entry with the identity
	// full body, so the next Range is served straight from cache.
	req2 := httptest.NewRequest("GET", "/gist/file.txt", nil)
	req2.Header.Set("Range", "bytes=0-9")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPartialContent || rec2.Body.String() != "0123456789" {
		t.Fatalf("cached range: status=%d body=%q", rec2.Code, rec2.Body.String())
	}
	if m := rec2.Header().Get("X-Performance-Metrics"); !strings.Contains(m, `"cache_hit_full_content"`) {
		t.Fatalf("metrics = %q, want cache_hit_full_content", m)
	}
}

// An unsatisfiable Range served from the disk cache must not inherit the
// stored Content-Length (the error body is much shorter).
func TestCachedUnsatisfiableRangeContentLength(t *testing.T) {
	payload := strings.Repeat("Z", 2000)
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())

	if rec := doGet(h, "/gist/movie.bin", nil); rec.Code != http.StatusOK {
		t.Fatalf("prime status = %d", rec.Code)
	}

	req := httptest.NewRequest("GET", "/gist/movie.bin", nil)
	req.Header.Set("Range", "bytes=5000-6000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", rec.Code)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q, body = %d bytes", cl, rec.Body.Len())
	}
}

// The over-cap fallback of the range-after-full-cache path replays the full
// 200 and must still carry the CORS headers added after the early return.
func TestRangeAfterFullCacheFallbackKeepsCors(t *testing.T) {
	payload := strings.Repeat("A", 2<<20)
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
	h.store = cache.Open(t.TempDir(), 16, 1) // per-file cap 1 MiB

	req := httptest.NewRequest("GET", "/gist/big.bin", nil)
	req.Header.Set("Range", "bytes=0-4")
	req.Header.Set("Origin", "https://web.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 fallback", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("fallback lost CORS headers")
	}
}

// AI/SSE responses must reach the client incrementally. Before the fix the
// whole body was buffered until the handler returned.
func TestAIStreamingFlushedIncrementally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(700 * time.Millisecond)
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer upstream.Close()

	orig := platform.Catalog["ip-openai"]
	platform.Catalog["ip-openai"] = upstream.URL
	platform.RefreshIndex()
	t.Cleanup(func() {
		platform.Catalog["ip-openai"] = orig
		platform.RefreshIndex()
	})

	proxySrv := httptest.NewServer(testHandler(t, http.NewServeMux()))
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/ip/openai/v1/chat/completions",
		strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 16)
	n, _ := resp.Body.Read(buf)
	elapsed := time.Since(start)
	if n == 0 || !strings.Contains(string(buf[:n]), "first") {
		t.Fatalf("first chunk = %q", string(buf[:n]))
	}
	if elapsed > 350*time.Millisecond {
		t.Fatalf("first chunk delayed %v (response buffered)", elapsed)
	}
}

// HEAD on a rewritable PyPI index is host-bound in the official flow even
// though the empty body is not rewritten; it must not be cacheable.
func TestPypiHeadCacheControlNoStore(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer upstream.Close()

	orig := platform.Catalog["pypi"]
	platform.Catalog["pypi"] = upstream.URL
	platform.RefreshIndex()
	t.Cleanup(func() {
		platform.Catalog["pypi"] = orig
		platform.RefreshIndex()
	})

	h := testHandler(t, http.NewServeMux())
	req := httptest.NewRequest("HEAD", "/pypi/simple/demo/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}
