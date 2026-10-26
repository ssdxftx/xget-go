// HTTP handler: the main request flow, ported from src/app/handle-request.js,
// src/routing/resolve-target.js and src/response/finalize-response.js of the
// original project.
//
// Flow: health → token auth → protocol traits → CORS preflight → docker API
// probe → homepage redirect → validation → path normalization → platform
// routing → upstream fetch with retries → response finalization.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
        "crypto/subtle"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "regexp"
        "strconv"
        "strings"
        "time"

        "xget/internal/config"
        "xget/internal/platform"
        "xget/internal/security"
)

// Handler serves the accelerated proxy.
type Handler struct {
        cfg          *config.Config
        client       *http.Client        // follows redirects (default fetch semantics)
        dockerClient *http.Client        // does NOT follow redirects (manual auth-safe follow)
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

// stripXgetToken removes the proxy-auth query parameter before the query is
// forwarded upstream (it must not leak the instance token).
func stripXgetToken(rawQuery string) string {
        if rawQuery == "" {
                return ""
        }
        parts := strings.Split(rawQuery, "&")
        kept := make([]string, 0, len(parts))
        for _, p := range parts {
                if p == "" || strings.HasPrefix(p, "xget_token=") {
                        continue
                }
                kept = append(kept, p)
        }
        return strings.Join(kept, "&")
}

// authorized checks the instance access token when one is configured.
// Accepted sources (first match wins):
//  1. X-Xget-Token header          (leaves Authorization free for upstream)
//  2. xget_token query parameter
//  3. Authorization: Bearer <token> (consumed; NOT forwarded upstream)
//
// Returns ok=true when auth is disabled or the token matches, and whether an
// Authorization header remains that should be forwarded upstream.
func authorized(r *http.Request, cfg *config.Config) (ok, forwardAuth bool) {
        token := cfg.AccessToken
        if token == "" {
                return true, r.Header.Get("Authorization") != ""
        }
        if t := r.Header.Get("X-Xget-Token"); t != "" {
                return subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1, r.Header.Get("Authorization") != ""
        }
        if t := r.URL.Query().Get("xget_token"); t != "" {
                return subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1, r.Header.Get("Authorization") != ""
        }
        if auth := r.Header.Get("Authorization"); auth != "" {
                const prefix = "Bearer "
                if strings.HasPrefix(auth, prefix) {
                        t := strings.TrimSpace(auth[len(prefix):])
                        return subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1, false
                }
                return false, false
        }
        return false, false
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
        start := time.Now()

        // Liveness probe is always open.
        if r.URL.Path == "/healthz" {
                w.Header().Set("Content-Type", "text/plain; charset=utf-8")
                _, _ = w.Write([]byte("ok"))
                return
        }

        // Instance access control (official README strongly recommends auth).
        if ok, _ := authorized(r, h.cfg); !ok {
                w.Header().Set("WWW-Authenticate", `Bearer realm="xget"`)
                security.WriteError(w, r, "Unauthorized: missing or invalid access token", http.StatusUnauthorized, false)
                return
        }

        v := newView(r)
        traits := Detect(v)
        path := v.Path

        // CORS preflight.
        if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
                requested := r.Header.Get("Access-Control-Request-Method")
                allowed := h.methodAllowed(traits)
                if !methodAllowed(requested, allowed) {
                        security.WriteError(w, r, "Method not allowed", http.StatusMethodNotAllowed, false)
                        return
                }
                hdr := http.Header{}
                if !security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, allowed) {
                        security.WriteError(w, r, "Origin not allowed", http.StatusForbidden, false)
                        return
                }
                hdr.Set("Access-Control-Allow-Methods", joinMethods(allowed))
                hdr.Set("Access-Control-Max-Age", "86400")
                security.AddSecurityHeaders(hdr)
                for k, vv := range hdr {
                        for _, val := range vv {
                                w.Header().Add(k, val)
                        }
                }
                w.WriteHeader(http.StatusNoContent)
                return
        }

        // Docker registry API version probe.
        if traits.Docker && (path == "/v2" || path == "/v2/") {
                w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
                w.Header().Set("Content-Type", "application/json")
                security.AddSecurityHeaders(w.Header())
                w.WriteHeader(http.StatusOK)
                if r.Method != http.MethodHead {
                        _, _ = w.Write([]byte("{}"))
                }
                return
        }

        // Bare root → homepage.
        if path == "/" || path == "" {
                http.Redirect(w, r, h.cfg.HomepageURL, http.StatusFound)
                return
        }

        // Validation.
        if status, msg := Validate(r, v, traits, h.cfg); status != 0 {
                security.WriteError(w, r, msg, status, false)
                return
        }

        // Docker path normalization.
        effectivePath := path
        if traits.Docker {
                normalized, status, msg := normalizeDockerPath(path)
                if status != 0 {
                        security.WriteError(w, r, msg, status, false)
                        return
                }
                effectivePath = normalized

                // The /v2/auth login-proxy endpoint belongs to the full edition.
                if path == "/v2/auth" || regexpMustMatch(`/^\/cr\/[^/]+\/v2\/auth\/?$/`)(path) {
                        security.WriteError(w, r,
                                "Docker login proxy (/v2/auth) requires the full edition of Xget; this standard binary supports anonymous pulls of public images",
                                http.StatusNotImplemented, false)
                        return
                }
        }

        // Platform routing.
        key, ok := platform.Resolve(effectivePath)
        if !ok {
                http.Redirect(w, r, h.cfg.HomepageURL, http.StatusFound)
                return
        }
        platformPath := platform.PlatformPath(key)
        if effectivePath == platformPath || effectivePath == platformPath+"/" {
                http.Redirect(w, r, h.cfg.HomepageURL, http.StatusFound)
                return
        }

        transformed := platform.TransformPath(effectivePath, key)
        if platform.IsContainer(key) {
                transformed = platform.NormalizeRegistryApiPath(key, transformed)
        }

        targetURL := platform.Catalog[key] + transformed
        if q := stripXgetToken(r.URL.RawQuery); q != "" {
                targetURL += "?" + q
        }

        // Authorization forwarding: when the instance token came via
        // X-Xget-Token/xget_token, a client Authorization header passes straight
        // through to the upstream (private repo downloads). When Bearer was used
        // for instance auth, upstream auth is intentionally not forwarded.
        _, forwardAuth := authorized(r, h.cfg)
        if !forwardAuth {
                r.Header.Del("Authorization")
        }

        // Protocol requests (git/LFS/docker) and non-GET/HEAD pass headers raw.
        passthrough := traits.IsProtocol() || (r.Method != http.MethodGet && r.Method != http.MethodHead)

        up := h.fetchUpstream(r, v, targetURL, traits, passthrough)
        h.finalize(w, r, up, traits, passthrough, key, start)

        log.Printf("%s %s%s → %s [%s] status=%d attempts=%d fetch_ms=%.1f total_ms=%.1f",
                clientIP(r), r.Method, r.URL.RequestURI(), targetURL, keyOrDash(key),
                up.resp.StatusCode, up.attempts, up.fetchMs,
                float64(time.Since(start).Microseconds())/1000.0)
}

// finalize writes the upstream (or locally generated) response to the client,
// port of finalizeResponse: error wrapping for standard requests, hop-by-hop
// stripping, cache hints, security + CORS + performance headers, streaming.
func (h *Handler) finalize(w http.ResponseWriter, r *http.Request, up *upstreamResult, traits Traits, passthrough bool, key string, start time.Time) {
        resp := up.resp
        defer func() {
                if resp.Body != nil {
                        _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
                        resp.Body.Close()
                }
        }()

        // Standard requests wrap upstream failures into the JSON error contract.
        if !traits.IsProtocol() && !up.local && resp.StatusCode >= 400 {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
                resp.Body.Close() // fully consumed up to the cap; drop the rest
                resp = security.BuildErrorResponse(
                        fmt.Sprintf("Upstream server error (%d): %s", resp.StatusCode, string(body)),
                        resp.StatusCode, true)
                up.local = true
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
                resp.StatusCode < 300 && resp.Header.Get("Content-Length") == "" && !traits.Docker {
                effective := rewriteEffectiveForProbe(r)
                if key2, ok2 := platform.Resolve(effective); ok2 {
                        transformed := platform.TransformPath(effective, key2)
                        if platform.IsContainer(key2) {
                                transformed = platform.NormalizeRegistryApiPath(key2, transformed)
                        }
                        target := platform.Catalog[key2] + transformed
                        if q := stripXgetToken(r.URL.RawQuery); q != "" {
                                target += "?" + q
                        }
                        hdr.Del("Content-Length")
                        if cl := h.probeHEADContentLength(r.Context(), target, buildHeaders(r, passthrough, target)); cl != "" {
                                hdr.Set("Content-Length", cl)
                        }
                }
        }

        // Standard requests receive cache hints and hardening headers.
        if !traits.IsProtocol() {
                if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
                        sensitive := r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != ""
                        if sensitive {
                                hdr.Set("Cache-Control", "private, max-age="+strconv.Itoa(h.cfg.CacheTTL))
                                if vary := hdr.Get("Vary"); vary != "" {
                                        hdr.Set("Vary", vary+", Authorization, Cookie")
                                } else {
                                        hdr.Set("Vary", "Authorization, Cookie")
                                }
                        } else {
                                hdr.Set("Cache-Control", "public, max-age="+strconv.Itoa(h.cfg.CacheTTL))
                        }
                }
                hdr.Set("X-Content-Type-Options", "nosniff")
                hdr.Set("Accept-Ranges", "bytes")
                security.AddSecurityHeaders(hdr)
        }

        allowed := h.methodAllowed(traits)
        security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, allowed)

        if !traits.IsProtocol() {
                metrics := map[string]any{
                        "total_ms":   round100(float64(time.Since(start).Microseconds()) / 1000.0),
                        "upstream_ms": round100(up.fetchMs),
                        "attempts":   up.attempts,
                        "platform":   key,
                }
                if j, err := json.Marshal(metrics); err == nil {
                        hdr.Set("X-Performance-Metrics", string(j))
                }
        }

        w.WriteHeader(resp.StatusCode)
        if r.Method != http.MethodHead && resp.Body != nil {
                _, _ = io.Copy(w, resp.Body)
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
