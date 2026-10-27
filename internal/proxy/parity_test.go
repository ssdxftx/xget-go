// Parity regression tests: lock behavior to the official xixu-me/Xget
// semantics using httptest upstreams standing in for real platforms.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"xget/internal/cache"
	"xget/internal/config"
	"xget/internal/platform"
)

// testHandler spins a handler whose mock upstreams (platform "gist" and
// container registry "cr-quay") point at an httptest server.
func testHandler(t *testing.T, mux *http.ServeMux) *Handler {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	origGist, origQuay := platform.Catalog["gist"], platform.Catalog["cr-quay"]
	platform.Catalog["gist"] = srv.URL
	platform.Catalog["cr-quay"] = srv.URL
	platform.RefreshIndex()
	t.Cleanup(func() {
		platform.Catalog["gist"] = origGist
		platform.Catalog["cr-quay"] = origQuay
		platform.RefreshIndex()
	})
	cfg := &config.Config{
		Port: "0", HomepageURL: "https://github.com/xixu-me/Xget",
		TimeoutSecond: 5, MaxRetries: 3, RetryDelayMs: 0,
		MaxPathLength: 2048, CacheTTL: 300,
		CacheDir:   t.TempDir(),
		CacheMaxMB: 16, CacheMaxFileMB: 8,
		AllowedMethods: []string{"GET", "HEAD"},
		AllowedOrigins: []string{"*"},
	}
	h := New(cfg)
	h.client = srv.Client()
	h.dockerClient = &http.Client{Transport: srv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() { h.store = &cache.Store{} })
	return h
}

func doGet(h *Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 1. Retryable-status exhaustion must surface the LAST upstream response,
// not a synthetic 500 (official returns the final upstream response).
func TestRetryableStatusExhaustionReturnsLastUpstreamResponse(t *testing.T) {
	hits := 0
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.Header().Set("X-Upstream-Marker", "real-500")
			http.Error(w, "genuine upstream failure", 500)
		})
		return m
	}())
	h.cfg.MaxRetries = 2

	rec := doGet(h, "/gist/file.tar.gz", nil)
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Upstream server error (500)") {
		t.Fatalf("body = %q, want wrapped upstream error contract", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "genuine upstream failure") {
		t.Fatalf("body = %q, want upstream error text preserved (final response, not a synthetic one)", rec.Body.String())
	}
	if hits != 2 {
		t.Fatalf("upstream hits = %d, want 2 (retries)", hits)
	}
	if rec.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("error responses must not carry Accept-Ranges (official sets it only in finalizeSuccessfulResponse)")
	}
}

// 2. Conditional requests (If-None-Match) on protocol passthrough must let a
// 304 reach the client instead of being retried into a synthetic 500.
func TestConditional304PassesThrough(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") == `"abc"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.WriteHeader(500)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/gist/repo.git/info/refs", map[string]string{
		"User-Agent":    "git/2.34.1",
		"If-None-Match": `"abc"`,
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 passthrough (got synthetic error)", rec.Code)
	}
}

// 3. Docker anonymous-token retry must keep the ORIGINAL request method and
// accept any 2xx token response.
func TestDockerAnonymousRetryKeepsMethodAndAccepts2xxToken(t *testing.T) {
	var retryMethod, tokenStatus string
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
			tokenStatus = "201"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201) // official tokenResponse.ok accepts any 2xx
			_, _ = io.WriteString(w, `{"token":"tok-1"}`)
		})
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			retryMethod = r.Method
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+testRealmURL(r)+`",service="mock"`)
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "manifest")
		})
		return m
	}())

	req := httptest.NewRequest("PUT", "/cr/quay/v2/repo/manifests/latest", strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after anonymous retry", rec.Code)
	}
	if retryMethod != "PUT" {
		t.Fatalf("retry method = %s, want original PUT (official spreads the original fetch options)", retryMethod)
	}
	if tokenStatus != "201" {
		t.Fatalf("token service not exercised with 201 acceptance")
	}
}

func testRealmURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/token"
}

// 4. HEAD must never read the disk cache (official allowCacheApi is GET-only).
func TestHEADNeverReadsDiskCache(t *testing.T) {
	version := "v1"
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, version) // no Content-Length → Go tees via copy
		})
		return m
	}())

	first := doGet(h, "/gist/data.bin", nil)
	if first.Code != 200 {
		t.Fatalf("priming GET status = %d", first.Code)
	}
	version = "v2-LongerBody"

	head := httptest.NewRequest("HEAD", "/gist/data.bin", nil)
	headRec := httptest.NewRecorder()
	h.ServeHTTP(headRec, head)
	if headRec.Code != 200 {
		t.Fatalf("HEAD status = %d", headRec.Code)
	}
	if cl := headRec.Header().Get("Content-Length"); cl != "13" { // len("v2-LongerBody")
		t.Fatalf("HEAD Content-Length = %q, want 13 (fetched fresh from upstream, cache is GET-only)", cl)
	}
}

// 5. Non-preflight responses always advertise the CONFIGURED method list,
// even for protocol requests (official addCorsHeaders uses
// config.SECURITY.ALLOWED_METHODS; only the preflight handler widens it).
func TestCorsAllowMethodsUsesConfiguredList(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
		return m
	}())

	rec := doGet(h, "/gist/x", map[string]string{
		"User-Agent": "git/2.34.1", // protocol traits
		"Origin":     "https://web.example",
	})
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, HEAD" {
		t.Fatalf("ACA-M = %q, want configured %q", got, "GET, HEAD")
	}
}

// 6. Homepage redirect mirrors Response.redirect: empty body, no HTML.
func TestHomepageRedirectHasEmptyBody(t *testing.T) {
	h := testHandler(t, http.NewServeMux())
	rec := doGet(h, "/", nil)
	if rec.Code != 302 {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://github.com/xixu-me/Xget" {
		t.Fatalf("Location = %q", loc)
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("redirect body = %q, want empty (official Response.redirect)", body)
	}
}

// 7. A registry 401 without a challenge but with its own JSON UNAUTHORIZED
// payload passes through untouched (official isCustomError branch).
func TestDocker401CustomJSONErrorPassesThrough(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"denied"}]}`)
		})
		return m
	}())

	rec := doGet(h, "/cr/quay/v2/repo/manifests/latest", nil)
	if rec.Code != 401 {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Fatalf("body = %q, want registry's own JSON error preserved", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "This may be a private repository") {
		t.Fatalf("body = %q, custom registry error must not be re-wrapped", rec.Body.String())
	}
}

// 8. GET requests must not forward a request body upstream.
func TestGetBodyNotForwarded(t *testing.T) {
	var gotBody string
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = io.WriteString(w, "ok")
		})
		return m
	}())

	req := httptest.NewRequest("GET", "/gist/x", strings.NewReader("should-not-travel"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if gotBody != "" {
		t.Fatalf("upstream received GET body %q; official drops bodies for GET/HEAD", gotBody)
	}
}

// 9. Docker non-401 upstream errors (404/429/5xx) get the standard JSON
// error contract, exactly like standard platforms — the official
// finalizeErrorResponse only exempts 401 (docker) and git/LFS/AI/HF.
func TestDockerNon401ErrorGetsStandardWrap(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/cr/quay/v2/repo/manifests/latest", nil)
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Upstream server error (404)") {
		t.Fatalf("body = %q, want the standard wrap applied to docker non-401 errors", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "MANIFEST_UNKNOWN") {
		t.Fatalf("body = %q, want the upstream error text preserved inside the contract", rec.Body.String())
	}
}

// 10. Git/LFS/AI/HF upstream errors pass through raw for every status
// (official early return before the standard wrap).
func TestGitErrorsAlwaysPassThrough(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "repository not found", 404)
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	rec := doGet(h, "/gist/repo.git/info/refs", map[string]string{"User-Agent": "git/2.34.1"})
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Upstream server error") {
		t.Fatalf("body = %q, git protocol errors must relay raw", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "repository not found") {
		t.Fatalf("body = %q, want the upstream error body untouched", rec.Body.String())
	}
}

// 11. A cache hit must serve EXACTLY the body bytes stored on the miss —
// the JSON preamble line that Begin() writes into the body file must never
// leak into a served response (regression: hits once sent preamble+body,
// overflowing the declared Content-Length and truncating the client body).
func TestCacheHitServesExactStoredBytes(t *testing.T) {
	payload := strings.Repeat("xget-cache-payload;", 300) // 5700 bytes, > preamble
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(200)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())

	miss := doGet(h, "/gist/data.bin", nil)
	if miss.Code != 200 || miss.Body.String() != payload {
		t.Fatalf("miss: status=%d len=%d, want 200/%d", miss.Code, miss.Body.Len(), len(payload))
	}

	hit := doGet(h, "/gist/data.bin", nil)
	if hit.Code != 200 {
		t.Fatalf("hit status = %d, want 200", hit.Code)
	}
	if !strings.Contains(hit.Header().Get("X-Performance-Metrics"), `"cache_hit"`) {
		t.Fatalf("second request must be served from cache, metrics=%q", hit.Header().Get("X-Performance-Metrics"))
	}
	if hit.Body.String() != payload {
		t.Fatalf("hit body len=%d, want exactly %d stored bytes (preamble leak?)", hit.Body.Len(), len(payload))
	}
}

// 12. Range requests served from a disk-cache hit must slice the STORED
// body, not the raw file (the JSON preamble would otherwise shift every
// absolute Seek and corrupt the slice).
func TestCacheHitRangeSlicesStoredBody(t *testing.T) {
	payload := strings.Repeat("0123456789", 200) // 2000 bytes
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(200)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())

	if rec := doGet(h, "/gist/movie.bin", nil); rec.Code != 200 {
		t.Fatalf("miss status = %d, want 200", rec.Code)
	}

	req := httptest.NewRequest("GET", "/gist/movie.bin", nil)
	req.Header.Set("Range", "bytes=1990-1999")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "0123456789" {
		t.Fatalf("range slice = %q, want the last 10 stored bytes", got)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 1990-1999/2000" {
		t.Fatalf("Content-Range = %q, want bytes 1990-1999/2000", cr)
	}

	// Prefix range crossing no preamble either.
	req2 := httptest.NewRequest("GET", "/gist/movie.bin", nil)
	req2.Header.Set("Range", "bytes=0-9")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPartialContent || rec2.Body.String() != "0123456789" {
		t.Fatalf("prefix slice: status=%d body=%q, want 206/0123456789", rec2.Code, rec2.Body.String())
	}
}

// 13. Locally generated error contracts must match createErrorResponse
// byte-for-byte: bare "text/plain" (no charset suffix) and an ISO timestamp
// with Date#toISOString's fixed millisecond precision.
func TestErrorContractHeadersAndTimestamp(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(500)
			_, _ = io.WriteString(w, "boom")
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	// Local plain-text error: 405 for a non-allowed method.
	req := httptest.NewRequest("POST", "/gist/file.tar.gz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want bare text/plain", ct)
	}

	// Wrapped upstream error: JSON details with millisecond timestamp.
	rec2 := doGet(h, "/gist/file.tar.gz", nil)
	if rec2.Code != 500 {
		t.Fatalf("status = %d, want 500", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if !reISOTimestamp.MatchString(rec2.Body.String()) {
		t.Fatalf("body = %q, want a Date#toISOString style timestamp", rec2.Body.String())
	}
}

var reISOTimestamp = regexp.MustCompile(`"timestamp":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z"`)

// 14. X-Performance-Metrics must serialize the upstream mark timeline
// (attempt_N / success / client_error / cache_hit / complete), not a custom
// summary object.
func TestPerformanceMetricsMarks(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/missing") {
				http.Error(w, "nope", 404)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "payload")
		})
		return m
	}())
	h.cfg.MaxRetries = 1

	ok := doGet(h, "/gist/data.bin", nil)
	metrics := ok.Header().Get("X-Performance-Metrics")
	for _, want := range []string{`"attempt_0"`, `"success"`, `"complete"`} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("metrics = %q, want %s", metrics, want)
		}
	}
	if strings.Contains(metrics, "total_ms") {
		t.Fatalf("metrics = %q, must not carry the custom summary schema", metrics)
	}

	hit := doGet(h, "/gist/data.bin", nil)
	if m := hit.Header().Get("X-Performance-Metrics"); !strings.Contains(m, `"cache_hit"`) {
		t.Fatalf("metrics = %q, want cache_hit", m)
	}

	miss := doGet(h, "/gist/missing", nil)
	if m := miss.Header().Get("X-Performance-Metrics"); !strings.Contains(m, `"client_error"`) {
		t.Fatalf("metrics = %q, want client_error", m)
	}
}

// 15. A Range request whose upstream ignores Range and answers 200 must be
// cached in full and re-served as a 206 slice, marking
// range_cache_hit_after_full_cache (port of the post-put cache re-match).
func TestRangeAfterFullCacheSlicesAndMarks(t *testing.T) {
	payload := "hello-world-payload"
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Deliberately ignores Range.
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(200)
			_, _ = io.WriteString(w, payload)
		})
		return m
	}())

	req := httptest.NewRequest("GET", "/gist/file.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "hello" {
		t.Fatalf("body = %q, want hello", got)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-4/19" {
		t.Fatalf("Content-Range = %q, want bytes 0-4/19", cr)
	}
	if m := rec.Header().Get("X-Performance-Metrics"); !strings.Contains(m, `"range_cache_hit_after_full_cache"`) {
		t.Fatalf("metrics = %q, want range_cache_hit_after_full_cache", m)
	}
}

// 16. A present `service` query parameter short-circuits Git detection: any
// value other than the two pack services means the request is NOT a Git
// protocol request, so the method whitelist still applies (official early
// return inside isGitRequest).
func TestGitServiceParamShortCircuits(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		return m
	}())

	req := httptest.NewRequest("POST", "/gist/x?service=other", nil)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (service=other must not count as Git)", rec.Code)
	}

	// The two real pack services still mark the request as Git and allow POST.
	req2 := httptest.NewRequest("POST", "/gist/x?service=git-upload-pack", nil)
	req2.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (git-upload-pack keeps protocol passthrough)", rec2.Code)
	}
}

// 17. The /v2/auth login proxy relays the upstream probe/token response
// through the protocol branch, which adds CORS only — no security headers
// (official handleDockerAuth returns the raw Response).
func TestDockerAuthProxyOmitsSecurityHeaders(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "{}")
		})
		return m
	}())

	rec := doGet(h, "/cr/quay/v2/auth?scope=repository:test/repo:pull", nil)
	if rec.Code != 200 || rec.Body.String() != "{}" {
		t.Fatalf("status=%d body=%q, want 200/{}", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("security headers must be omitted for the protocol-branch auth proxy")
	}
}

// 18. Homepage redirects for protocol requests carry CORS only; the security
// headers reach non-protocol redirects through the performance-header path.
func TestHomepageRedirectSecurityHeadersByTraits(t *testing.T) {
	h := testHandler(t, func() *http.ServeMux {
		m := http.NewServeMux()
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
		return m
	}())

	git := doGet(h, "/unknown/x", map[string]string{"User-Agent": "git/2.34.1"})
	if git.Code != http.StatusFound {
		t.Fatalf("git redirect status = %d, want 302", git.Code)
	}
	if git.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("protocol redirects must not carry security headers")
	}

	plain := doGet(h, "/unknown/x", nil)
	if plain.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", plain.Code)
	}
	if plain.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("non-protocol redirects must carry security headers")
	}
}
