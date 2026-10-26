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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
	// Track write state so a recovered panic can still emit the same 500
	// JSON contract the original produces via its top-level try/catch.
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic recovered: %v\n%s", rec, debug.Stack())
			if !sw.wrote {
				security.WriteError(sw, r, "Internal Server Error", http.StatusInternalServerError, true)
			}
		}
	}()

	start := time.Now()

	// Liveness probe is always open.
	if r.URL.Path == "/healthz" {
		sw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = sw.Write([]byte("ok"))
		return
	}

	v := newView(r)
	traits := Detect(v)
	path := v.Path

	// Locally generated responses all carry the header set the original
	// project applies globally in handleRequest: security headers, CORS
	// (when an Origin is present, advertising the CONFIGURED method list —
	// only the preflight handler widens it) and, for non-protocol requests,
	// the performance header.
	localError := func(tr Traits, msg string, status int) {
		security.AddSecurityHeaders(sw.Header())
		security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		h.emitMetrics(sw, r, tr, "", start, 0, 0, "none")
		security.WriteError(sw, r, msg, status, false)
	}
	// Homepage redirect mirrors Response.redirect: Location + 302, empty body.
	localRedirect := func(tr Traits, target string) {
		security.AddSecurityHeaders(sw.Header())
		security.AddCorsHeaders(sw.Header(), r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		h.emitMetrics(sw, r, tr, "", start, 0, 0, "none")
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
		h.emitMetrics(sw, r, traits, "", start, 0, 0, "none")
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
			h.serveFromCache(sw, r, entry, key, start)
			log.Printf("%s %s%s ← cache [%s] status=%d size=%d",
				clientIP(r), r.Method, r.URL.RequestURI(), keyOrDash(key), entry.Status, entry.Size)
			return
		}
	}
	cacheStatus := string(policy.Mode) // edge→edge / bypass / private

	up := h.fetchUpstream(r, v, targetURL, traits, passthrough, key, effectivePath)
	h.finalize(sw, r, up, v, traits, passthrough, key, effectivePath, targetURL, cacheURL, policy, cacheStatus, start)

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
func (h *Handler) finalize(w http.ResponseWriter, r *http.Request, up *upstreamResult, v *view, traits Traits, passthrough bool, key, effectivePath, targetURL, cacheURL string, policy CachePolicy, cacheStatus string, start time.Time) {
	resp := up.resp
	defer func() {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}
	}()

	// Error wrapping: standard requests get the JSON contract; docker 401s
	// without a challenge hint at private repositories — unless the registry
	// already answered with its own JSON UNAUTHORIZED error (official
	// isCustomError branch), which passes through untouched. Git/LFS/AI/HF
	// errors pass through untouched.
	if !up.local && resp.StatusCode >= 400 {
		switch {
		case !traits.IsProtocol():
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close() // fully consumed up to the cap; drop the rest
			resp = security.BuildErrorResponse(
				fmt.Sprintf("Upstream server error (%d): %s", resp.StatusCode, string(body)),
				resp.StatusCode, true)
			up.local = true
		case traits.Docker && resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") == "":
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
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
	if !traits.IsProtocol() && r.Method == http.MethodGet &&
		resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		shouldRewriteTextResponse(key, effectivePath, resp.Header.Get("Content-Type")) {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, rewriteMaxBytes+1))
		if len(raw) <= rewriteMaxBytes {
			rewritten := rewriteTextResponse(key, effectivePath, string(raw), requestOrigin(r))
			bodySrc = strings.NewReader(rewritten)
			hdr.Set("Content-Length", strconv.Itoa(len(rewritten)))
			hdr.Del("Content-Encoding") // identity was requested upstream
			originBoundRewrite = key == "pypi"
		} else {
			bodySrc = io.MultiReader(bytes.NewReader(raw), resp.Body)
		}
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

	security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
	h.emitMetrics(w, r, traits, key, start, up.fetchMs, up.attempts, cacheStatus)

	w.WriteHeader(resp.StatusCode)
	var copyErr error
	if r.Method != http.MethodHead && bodySrc != nil {
		dst := io.Writer(w)
		if cacheWriter != nil {
			dst = io.MultiWriter(w, cacheWriter)
		}
		_, copyErr = io.Copy(dst, bodySrc)
	}
	if copyErr != nil {
		cacheWriter.Abort() // client vanished: don't keep a partial file
	} else {
		cacheWriter.Commit()
	}
}

// emitMetrics sets the X-Performance-Metrics header (must run before the
// response status is written).
func (h *Handler) emitMetrics(w http.ResponseWriter, r *http.Request, traits Traits, key string, start time.Time, fetchMs float64, attempts int, cacheStatus string) {
	if traits.IsProtocol() {
		return
	}
	metrics := map[string]any{
		"total_ms":    round100(float64(time.Since(start).Microseconds()) / 1000.0),
		"upstream_ms": round100(fetchMs),
		"attempts":    attempts,
		"platform":    key,
		"cache":       cacheStatus,
	}
	if j, err := json.Marshal(metrics); err == nil {
		w.Header().Set("X-Performance-Metrics", string(j))
	}
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

func round100(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// regexpMustMatch compiles a JS-style pattern used once for /v2/auth.
func regexpMustMatch(pattern string) func(string) bool {
	re := regexp.MustCompile(`^/cr/[^/]+/v2/auth/?$`)
	_ = pattern
	return re.MatchString
}
