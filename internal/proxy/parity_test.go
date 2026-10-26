// Parity regression tests: lock behavior to the official xixu-me/Xget
// semantics using httptest upstreams standing in for real platforms.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
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
