// HTTP handler: the main request flow, ported from src/app/handle-request.js,
// src/routing/resolve-target.js and src/response/finalize-response.js of the
// original project.
//
// Flow: health → protocol traits → CORS preflight → docker API probe →
// homepage redirect → validation → path normalization → platform routing →
// upstream fetch with retries → response finalization. Auth parity with the
// original: no instance-level token gate — Authorization (and every other
// credential header) is forwarded to the upstream untouched.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"xget/internal/cache"
	"xget/internal/config"
	"xget/internal/platform"
	"xget/internal/security"
)

// statusWriter remembers whether the response status has been written so the
// panic-recovery path knows if a 500 JSON contract can still be emitted.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wrote {
		sw.status = code
		sw.wrote = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(p []byte) (int, error) {
	if !sw.wrote {
		sw.status = http.StatusOK
		sw.wrote = true
	}
	return sw.ResponseWriter.Write(p)
}

// Flush forwards to the underlying writer so streaming responses (SSE/AI)
// reach the client as chunks arrive instead of sitting in net/http's buffer.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		if !sw.wrote {
			sw.status = http.StatusOK
			sw.wrote = true
		}
		f.Flush()
	}
}

// flushWriter flushes after every write. Used for streaming bodies so
// small SSE events are not delayed until the handler returns.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// streamingResponse reports whether the body is consumed incrementally by
// the client and should therefore be flushed on every write.
func streamingResponse(resp *http.Response, traits Traits) bool {
	if traits.AI || traits.HF {
		return true
	}
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// Handler serves the accelerated proxy.
type Handler struct {
	cfg          *config.Config
	client       *http.Client // follows redirects (default fetch semantics)
	dockerClient *http.Client // does NOT follow redirects (manual auth-safe follow)
	store        *cache.Store // disk cache replacing the edge cache
}

// New wires up clients with a shared tuned transport.
func New(cfg *config.Config) *Handler {
	tr := &http.Transport{
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Handler{
		cfg:          cfg,
		client:       &http.Client{Transport: tr},
		dockerClient: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		store:        cache.Open(cfg.CacheDir, cfg.CacheMaxMB, cfg.CacheMaxFileMB),
	}
}

var reV2CRStrip = regexp.MustCompile(`^/cr/([^/]+)/`)

// normalizeDockerPath ports normalizeEffectivePath: accepts /cr/<reg>/... and
// /v2/cr/<reg>/... spellings, collapsing the latter into the former with the
// /v2 segment moved after the registry name.
func normalizeDockerPath(path string) (string, int, string) {
	if !strings.HasPrefix(path, "/cr/") && !strings.HasPrefix(path, "/v2/cr/") && path != "/v2/auth" {
		return "", http.StatusBadRequest, "container registry requests must use /cr/ prefix"
	}
	effective := strings.TrimPrefix(path, "/v2")
	if strings.HasPrefix(path, "/v2/cr/") {
		effective = reV2CRStrip.ReplaceAllString(effective, `/cr/${1}/v2/`)
	}
	return effective, 0, ""
}

// methodAllowed lists the effective methods for this request shape.
func (h *Handler) methodAllowed(traits Traits) []string {
	if traits.IsProtocol() {
		return protocolMethods
	}
	return h.cfg.AllowedMethods
}

// ServeHTTP implements the full request pipeline.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mon := newPerformanceMonitor()
	start := mon.start

	// Track write state so a recovered panic can still emit the same
	// plain-text 500 the original produces via its top-level try/catch.
	sw := &statusWriter{ResponseWriter: w}
	var detected Traits // captured so the panic path mirrors the official
	// performance-header gate (protocol requests get no perf header).
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic recovered: %v\n%s", rec, debug.Stack())
			if !sw.wrote {
				// The official top-level catch emits
				// createErrorResponse('Internal Server Error', 500)
				// WITHOUT the JSON-details flag: a plain-text body.
				security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
				h.emitMetrics(sw, r, detected, mon)
				security.WriteError(sw, r, "Internal Server Error", http.StatusInternalServerError, false)
			}
		}
	}()

	// Liveness probe is always open.
	if r.URL.Path == "/healthz" {
		sw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = sw.Write([]byte("ok"))
		return
	}

	v := newView(r)
	detected = Detect(v)
	traits := detected
	path := v.Path

	// Locally generated responses all carry the header set the original
	// project applies globally in handleRequest: security headers, CORS
	// (when an Origin is present, advertising the CONFIGURED method list —
	// only the preflight handler widens it) and, for non-protocol requests,
	// the performance header.
	localError := func(tr Traits, msg string, status int) {
		security.AddSecurityHeaders(sw.Header())
		security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		h.emitMetrics(sw, r, tr, mon)
		security.WriteError(sw, r, msg, status, false)
	}
	// Homepage redirect mirrors Response.redirect: Location + 302, empty body.
	localRedirect := func(tr Traits, target string) {
		// Security headers reach non-protocol redirects through the
		// performance-header path upstream; protocol redirects carry CORS only.
		if !tr.IsProtocol() {
			security.AddSecurityHeaders(sw.Header())
		}
		security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		h.emitMetrics(sw, r, tr, mon)
		sw.Header().Set("Location", target)
		sw.WriteHeader(http.StatusFound)
	}

	// CORS preflight.
	if r.Method == http.MethodOptions && r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != "" {
		requested := r.Header.Get("Access-Control-Request-Method")
		allowed := h.methodAllowed(traits)
		if !methodAllowed(requested, allowed) {
			localError(traits, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		hdr := http.Header{}
		if !security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods) {
			localError(traits, "Origin not allowed", http.StatusForbidden)
			return
		}
		// The preflight handler is the ONLY place the widened protocol
		// method list is advertised (mirrors the official override).
		hdr.Set("Access-Control-Allow-Methods", joinMethods(allowed))
		hdr.Set("Access-Control-Max-Age", "86400")
		security.AddSecurityHeaders(hdr)
		for k, vv := range hdr {
			for _, val := range vv {
				sw.Header().Add(k, val)
			}
		}
		h.emitMetrics(sw, r, traits, mon)
		sw.WriteHeader(http.StatusNoContent)
		return
	}

	// Docker registry API version probe.
	if traits.Docker && (path == "/v2" || path == "/v2/") {
		sw.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		sw.Header().Set("Content-Type", "application/json")
		security.AddSecurityHeaders(sw.Header())
		security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		sw.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = sw.Write([]byte("{}"))
		}
		return
	}

	// Bare root → homepage.
	if path == "/" || path == "" {
		localRedirect(traits, h.cfg.HomepageURL)
		return
	}

	// Validation.
	if status, msg := Validate(r, v, traits, h.cfg); status != 0 {
		localError(traits, msg, status)
		return
	}

	// Docker path normalization.
	effectivePath := path
	if traits.Docker {
		normalized, status, msg := normalizeDockerPath(path)
		if status != 0 {
			localError(traits, msg, status)
			return
		}
		effectivePath = normalized

		// The /v2/auth login proxy negotiates registry credentials
		// for private repositories (full docker login chain).
		if path == "/v2/auth" || regexpMustMatch(`/^\/cr\/[^/]+\/v2\/auth\/?$/`)(path) {
			h.handleDockerAuth(sw, r, path, traits, localError)
			log.Printf("%s %s%s → docker-auth", clientIP(r), r.Method, r.URL.RequestURI())
			return
		}
	}

	// Platform routing.
	key, ok := platform.Resolve(effectivePath)
	if !ok {
		localRedirect(traits, h.cfg.HomepageURL)
		return
	}
	platformPath := platform.PlatformPath(key)
	if effectivePath == platformPath || effectivePath == platformPath+"/" {
		localRedirect(traits, h.cfg.HomepageURL)
		return
	}

	transformed := platform.TransformPath(effectivePath, key)
	if platform.IsContainer(key) {
		transformed = platform.NormalizeRegistryApiPath(key, transformed)
	}

	targetURL := platform.Catalog[key] + transformed
	if q := r.URL.RawQuery; q != "" {
		targetURL += "?" + q
	}

	// Authorization is forwarded to the upstream as-is (parity with the
	// original createFetchOptions): it carries UPSTREAM credentials (GitHub
	// PAT, HF token, registry basic auth), never an instance token — this
	// proxy has none.

	// Protocol requests (git/LFS/docker/AI/HF) and non-GET/HEAD pass headers raw.
	passthrough := traits.IsProtocol() || (r.Method != http.MethodGet && r.Method != http.MethodHead)

	// Cache policy and lookup (port of tryReadCachedResponse: protocol
	// requests, sensitive headers and non-GET/HEAD never touch the cache).
	sensitive := r.Header.Get("Authorization") != "" ||
		r.Header.Get("Cookie") != "" ||
		r.Header.Get("Proxy-Authorization") != ""
	canUseCache := r.Method == http.MethodGet || r.Method == http.MethodHead
	policy := resolveCachePolicy(canUseCache, sensitive, traits, key, effectivePath, targetURL, h.cfg.CacheTTL)

	// Origin-bound rewrites (npm metadata, flathub .flatpakref) embed the
	// requesting origin into the cached body, so the cache key is derived
	// from cacheTargetUrl with a __xget_origin marker (port of
	// shouldVaryCacheByOrigin + cacheTargetUrl). The upstream URL itself
	// stays clean.
	cacheURL := targetURL
	if !traits.IsProtocol() && shouldVaryCacheByOrigin(key, effectivePath) {
		sep := "?"
		if strings.Contains(cacheURL, "?") {
			sep = "&"
		}
		cacheURL += sep + "__xget_origin=" + url.QueryEscape(requestOrigin(r))
	}

	// Cache lookup: GET only — the original's allowCacheApi is GET-only,
	// so HEAD always fetches fresh from the upstream (port of
	// tryReadCachedResponse: protocol requests, sensitive headers and
	// non-GET/HEAD never touch the cache).
	if policy.Cacheable && r.Method == http.MethodGet {
		if entry := h.store.Get(cache.Key(cacheURL)); entry != nil {
			// Byte-range slicing is only valid for identity-encoded bodies.
			// A compressed entry (gzip/br/deflate) must not be sliced; skip
			// the hit and refetch, because Range requests are sent upstream
			// with Accept-Encoding: identity.
			if r.Header.Get("Range") == "" || cacheEntrySliceable(entry) {
				hitMark := "cache_hit"
				if r.Header.Get("Range") != "" {
					// A Range request served from a full cached entry mirrors
					// the official cache_hit_full_content mark.
					hitMark = "cache_hit_full_content"
				}
				h.serveFromCache(sw, r, entry, mon, hitMark)
				log.Printf("%s %s%s ← cache [%s] status=%d size=%d",
					clientIP(r), r.Method, r.URL.RequestURI(), keyOrDash(key), entry.Status, entry.Size)
				return
			}
		}
	}
	cacheStatus := string(policy.Mode) // edge→edge / bypass / private

	up := h.fetchUpstream(r, v, targetURL, traits, passthrough, key, effectivePath, mon)
	h.finalize(sw, r, up, v, traits, passthrough, key, effectivePath, targetURL, cacheURL, policy, cacheStatus, start, mon)

	log.Printf("%s %s%s → %s [%s] status=%d attempts=%d fetch_ms=%.1f total_ms=%.1f cache=%s",
		clientIP(r), r.Method, r.URL.RequestURI(), targetURL, keyOrDash(key),
		up.resp.StatusCode, up.attempts, up.fetchMs,
		float64(time.Since(start).Microseconds())/1000.0, cacheStatus)
}

// finalize writes the upstream (or locally generated) response to the client,
// port of finalizeResponse: error wrapping (with the private-repository hint
// for bare docker 401s), text rewriting (pypi/npm/flathub), hop-by-hop
// stripping, cache-policy Cache-Control headers, disk-cache tee, security +
// CORS + performance headers and streaming.
func (h *Handler) finalize(w http.ResponseWriter, r *http.Request, up *upstreamResult, v *view, traits Traits, passthrough bool, key, effectivePath, targetURL, cacheURL string, policy CachePolicy, cacheStatus string, start time.Time, mon *performanceMonitor) {
	resp := up.resp
	defer func() {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
	}()

	// Error wrapping: port of finalizeErrorResponse. The official gate is
	// "not ok" (anything outside 2xx — the extra 206 check there is
	// redundant because 206 is already ok). Docker 401s get the
	// private-repository hint unless the registry already answered with
	// its own JSON UNAUTHORIZED error (official isCustomError branch);
	// 401s WITH a WWW-Authenticate challenge (our synthesized
	// responseUnauthorized) pass through untouched. Git/LFS/AI/HF errors
	// pass through untouched. Everything else — standard platforms AND
	// docker non-401 errors (404/429/5xx/...) alike — gets the JSON
	// error contract with the status preserved.
	if !up.local && !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		switch {
		case traits.Docker && resp.StatusCode == http.StatusUnauthorized:
			if resp.Header.Get("WWW-Authenticate") == "" {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close() // fully consumed up to the cap; drop the rest
				if resp.Header.Get("Content-Type") == "application/json" &&
					strings.Contains(string(body), "UNAUTHORIZED") {
					// Registry's own JSON error: relay as-is.
					resp.Body = io.NopCloser(bytes.NewReader(body))
				} else {
					resp = security.BuildErrorResponse(
						fmt.Sprintf("Authentication required for this container registry resource. This may be a private repository. Original error: %s", string(body)),
						resp.StatusCode, true)
					up.local = true
				}
			}
		case traits.Git || traits.GitLFS || traits.AI || traits.HF:
			// Protocol errors relay raw (official early return).
		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			resp = security.BuildErrorResponse(
				fmt.Sprintf("Upstream server error (%d): %s", resp.StatusCode, string(body)),
				resp.StatusCode, true)
			up.local = true
		}
	}

	hdr := w.Header()
	if up.local {
		for k, vv := range resp.Header {
			for _, val := range vv {
				hdr.Add(k, val)
			}
		}
	} else {
		for k, vv := range resp.Header {
			if hopResponseHeaders[strings.ToLower(k)] {
				continue
			}
			for _, val := range vv {
				hdr.Add(k, val)
			}
		}
		if resp.Uncompressed {
			hdr.Del("Content-Encoding")
			hdr.Del("Content-Length")
		}
	}

	// HEAD responses without Content-Length get a Range probe backfill
	// (port of the HEAD fallback in executeFetch).
	if r.Method == http.MethodHead && !up.local && resp.StatusCode >= 200 &&
		resp.StatusCode < 300 && resp.Header.Get("Content-Length") == "" {
		effective := rewriteEffectiveForProbe(r)
		if key2, ok2 := platform.Resolve(effective); ok2 {
			transformed := platform.TransformPath(effective, key2)
			if platform.IsContainer(key2) {
				transformed = platform.NormalizeRegistryApiPath(key2, transformed)
			}
			target := platform.Catalog[key2] + transformed
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
			hdr.Del("Content-Length")
			if cl := h.probeHEADContentLength(r.Context(), target, buildHeaders(r, v, passthrough, target, traits, key2)); cl != "" {
				hdr.Set("Content-Length", cl)
			}
		}
	}

	// Text rewriting keeps follow-up requests flowing through this
	// instance (port of the finalizeSuccessfulResponse rewriters, which
	// run for every ok (2xx) response).
	var bodySrc io.Reader = resp.Body
	originBoundRewrite := false
	rewriteCandidate := !traits.IsProtocol() &&
		resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		shouldRewriteTextResponse(key, effectivePath, resp.Header.Get("Content-Type"))
	if rewriteCandidate && r.Method == http.MethodGet {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, rewriteMaxBytes+1))
		if readErr != nil {
			// The upstream stream broke mid-body. Returning the partial
			// content would silently truncate a metadata/index response, so
			// mirror the official top-level catch with a plain-text 500.
			resp.Body.Close()
			clear(hdr)
			security.AddSecurityHeaders(hdr)
			security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
			h.emitMetrics(w, r, traits, mon)
			security.WriteError(w, r, "Internal Server Error", http.StatusInternalServerError, false)
			return
		}
		if len(raw) <= rewriteMaxBytes {
			rewritten := rewriteTextResponse(key, effectivePath, string(raw), requestOrigin(r))
			bodySrc = strings.NewReader(rewritten)
			hdr.Set("Content-Length", strconv.Itoa(len(rewritten)))
			hdr.Del("Content-Encoding") // identity was requested upstream
			originBoundRewrite = key == "pypi"
		} else {
			bodySrc = io.MultiReader(bytes.NewReader(raw), resp.Body)
		}
	} else if rewriteCandidate && key == "pypi" {
		// HEAD on a rewritable PyPI index: the official flow still runs the
		// rewrite (the empty HEAD body becomes ""), which marks the response
		// host-bound and downgrades it to no-store. Mirror that policy
		// decision while keeping the real Content-Length from the probe.
		originBoundRewrite = true
	}

	// Cache-policy headers + disk-cache write (port of finalizeSuccessfulResponse:
	// the whole block only runs for ok responses). A pypi rewrite binds the
	// body to this instance's origin, so the policy downgrades to bypass
	// (hasOriginBoundRewrite upstream).
	var cacheWriter *cache.Writer
	if !traits.IsProtocol() && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if originBoundRewrite {
			policy = CachePolicy{Mode: CacheBypass, NoStore: true}
			cacheStatus = string(policy.Mode)
		}
		respPolicy := resolveResponseCachePolicy(policy, resp.Header)
		hdr.Set("Cache-Control", respPolicy.CacheControlValue())
		if respPolicy.PrivateOnly {
			if vary := hdr.Get("Vary"); vary != "" {
				hdr.Set("Vary", vary+", Authorization, Cookie")
			} else {
				hdr.Set("Vary", "Authorization, Cookie")
			}
		}
		if resp.StatusCode == http.StatusOK && r.Method == http.MethodGet && respPolicy.Cacheable {
			cacheWriter = h.store.Begin(cache.Key(cacheURL), resp.StatusCode, hdr, respPolicy.EdgeTTL)
		}
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Accept-Ranges", "bytes")
		security.AddSecurityHeaders(hdr)
	}

	// Range request answered with a full 200: the official implementation
	// caches the complete body and re-matches with the Range header so the
	// edge returns 206. Port that by spooling the body while teeing it into
	// the cache, then serving the slice from the stored entry. When the entry
	// could not be stored (per-file cap or IO error), replay the spooled full
	// body as the original 200.
	if r.Header.Get("Range") != "" && resp.StatusCode == http.StatusOK && cacheWriter != nil {
		if h.serveRangeAfterFullCache(w, r, bodySrc, cacheWriter, cacheURL, mon) {
			return
		}
	}

	security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
	h.emitMetrics(w, r, traits, mon)

	w.WriteHeader(resp.StatusCode)
	var copyErr error
	if r.Method != http.MethodHead && bodySrc != nil {
		dst := io.Writer(w)
		if cacheWriter != nil {
			dst = io.MultiWriter(w, cacheWriter)
		}
		// Streaming bodies (SSE / AI inference) must be flushed as they
		// arrive; otherwise net/http buffers small events until the whole
		// response completes.
		if f, ok := w.(http.Flusher); ok && streamingResponse(resp, traits) {
			if cacheWriter != nil {
				dst = io.MultiWriter(&flushWriter{w: w, f: f}, cacheWriter)
			} else {
				dst = &flushWriter{w: w, f: f}
			}
		}
		_, copyErr = io.Copy(dst, bodySrc)
	}
	if copyErr != nil {
		cacheWriter.Abort() // client vanished: don't keep a partial file
	} else {
		cacheWriter.Commit()
	}
}

// serveRangeAfterFullCache ports the range_cache_hit_after_full_cache branch
// of finalizeSuccessfulResponse: a Range request whose upstream answered 200
// stores the full body and re-reads it through the Range header. The body is
// spooled to a temp file so an over-cap cache entry can still be replayed in
// full. Returns false only when spooling could not start (body untouched).
func (h *Handler) serveRangeAfterFullCache(w http.ResponseWriter, r *http.Request, body io.Reader, cw *cache.Writer, cacheURL string, mon *performanceMonitor) bool {
	tmp, err := os.CreateTemp("", "xget-range-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}()

	_, copyErr := io.Copy(io.MultiWriter(tmp, cw), body)
	if copyErr != nil {
		cw.Abort()
	}
	cw.Commit()

	if copyErr == nil {
		if entry := h.store.Get(cache.Key(cacheURL)); entry != nil && cacheEntrySliceable(entry) {
			mon.mark("range_cache_hit_after_full_cache")
			clear(w.Header())
			h.serveFromCache(w, r, entry, mon, "")
			return true
		}
	}

	// Entry not stored (cap/IO): replay the spooled full body unchanged.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return true
	}
	security.AddCorsHeaders(w.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
	h.emitMetrics(w, r, Traits{}, mon)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, tmp)
	return true
}

// emitMetrics records the terminal "complete" mark and sets the
// X-Performance-Metrics header (must run before the status is written).
// Protocol requests still record marks upstream but omit the header.
func (h *Handler) emitMetrics(w http.ResponseWriter, r *http.Request, traits Traits, mon *performanceMonitor) {
	mon.mark("complete")
	if traits.IsProtocol() {
		return
	}
	w.Header().Set("X-Performance-Metrics", mon.json())
}

// rewriteEffectiveForProbe recomputes the effective (docker-normalized) path
// used as the probe target for HEAD content-length backfill.
func rewriteEffectiveForProbe(r *http.Request) string {
	effective := r.URL.EscapedPath()
	if strings.HasPrefix(effective, "/v2/cr/") || effective == "/v2/auth" {
		n, _, _ := normalizeDockerPath(effective)
		if n != "" {
			effective = n
		}
	}
	return effective
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func keyOrDash(k string) string {
	if k == "" {
		return "-"
	}
	return k
}

func joinMethods(list []string) string {
	return strings.Join(list, ", ")
}

// regexpMustMatch compiles a JS-style pattern used once for /v2/auth.
func regexpMustMatch(pattern string) func(string) bool {
	re := regexp.MustCompile(`^/cr/[^/]+/v2/auth/?$`)
	_ = pattern
	return re.MatchString
}
