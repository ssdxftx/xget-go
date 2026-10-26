// Upstream fetch pipeline, ported from src/upstream/fetch-upstream.js of the
// original project: request header construction, retry with linear backoff,
// per-attempt timeout (headers only — body streaming is unbounded so large
// downloads are never killed), HEAD content-length probing, Docker registry
// redirect handling and anonymous-token retry.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"xget/internal/platform"
	"xget/internal/security"
)

// mediaFilePattern matches URLs whose extension suggests binary content;
// those requests must not be compressed upstream so Range works cleanly.
var mediaFilePattern = regexp.MustCompile(
	`(?i)\.(mp4|avi|mkv|mov|wmv|flv|webm|mp3|wav|flac|aac|ogg|jpg|jpeg|png|gif|bmp|svg|pdf|zip|rar|7z|tar|gz|bz2|xz)$`)

// request hop-by-hop headers that are never forwarded upstream.
var hopRequestHeaders = map[string]bool{
	"host":              true,
	"connection":        true,
	"upgrade":           true,
	"proxy-connection":  true,
	"keep-alive":        true,
	"transfer-encoding": true,
	"te":                true,
	"trailer":           true,
}

// response hop-by-hop headers that are never copied back to the client.
var hopResponseHeaders = map[string]bool{
	"connection":         true,
	"keep-alive":         true,
	"proxy-authenticate": true,
	"proxy-connection":   true,
	"te":                 true,
	"trailer":            true,
	"transfer-encoding":  true,
	"upgrade":            true,
}

// maxBodyBuffer caps how much of an incoming request body is buffered so
// that retries can replay it (git packs, docker pushes).
const maxBodyBuffer = 32 << 20 // 32 MiB

// upstreamResult carries the final upstream response plus metrics.
type upstreamResult struct {
	resp     *http.Response
	local    bool // response was generated locally (timeout/failure)
	attempts int
	fetchMs  float64
}

// cancelBody couples the response body with the attempt context cancel func
// so streaming stays alive after headers arrive but resources are released
// deterministically once the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelBody) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.cancel)
	return err
}

// bodyProvider replays a buffered request body across attempts.
type bodyProvider struct {
	buf      []byte        // nil when bodyless
	stream   io.ReadCloser // single-shot body (oversized), first attempt only
	replayOK bool
}

func (b *bodyProvider) get() (io.ReadCloser, bool) {
	if b == nil {
		return nil, true
	}
	if b.buf != nil {
		return io.NopCloser(bytes.NewReader(b.buf)), true
	}
	if b.stream != nil {
		s := b.stream
		b.stream = nil
		return s, true
	}
	return nil, true
}

func (b *bodyProvider) replayable() bool { return b == nil || b.buf != nil || b.stream == nil }

// captureBody buffers small incoming bodies for replay, or switches to
// single-shot streaming for oversized ones (re-aggregating the probe bytes
// so the stream is not truncated).
func captureBody(r *http.Request) (*bodyProvider, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	if r.ContentLength == 0 && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBuffer+1))
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	if len(buf) > maxBodyBuffer {
		// Oversized: single-shot. Re-attach the already-read prefix plus the
		// remainder of the original stream.
		once := io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
		return &bodyProvider{stream: once}, nil
	}
	return &bodyProvider{buf: buf}, nil
}

// configureGitDefaults ports configureGitHeaders: fills in protocol-required
// defaults for clients that omit them (rare, but keeps curl/H2 clients
// interoperable with git HTTP endpoints).
func configureGitDefaults(h http.Header, in *http.Request, v *view, traits Traits) {
	if !traits.GitLFS {
		if h.Get("User-Agent") == "" {
			h.Set("User-Agent", "git/2.34.1")
		}
		if in.Method == http.MethodPost && strings.HasSuffix(v.Path, "/git-upload-pack") &&
			h.Get("Content-Type") == "" {
			h.Set("Content-Type", "application/x-git-upload-pack-request")
		}
		if in.Method == http.MethodPost && strings.HasSuffix(v.Path, "/git-receive-pack") &&
			h.Get("Content-Type") == "" {
			h.Set("Content-Type", "application/x-git-receive-pack-request")
		}
		return
	}
	if h.Get("User-Agent") == "" {
		h.Set("User-Agent", "git-lfs/3.0.0 (GitHub; darwin amd64; go 1.17.2)")
	}
	if strings.Contains(v.Path, "/objects/batch") {
		if h.Get("Accept") == "" {
			h.Set("Accept", "application/vnd.git-lfs+json")
		}
		if in.Method == http.MethodPost && h.Get("Content-Type") == "" {
			h.Set("Content-Type", "application/vnd.git-lfs+json")
		}
	}
	if reSHA256Object.MatchString(v.Path) && h.Get("Accept") == "" {
		h.Set("Accept", "application/octet-stream")
	}
}

// buildHeaders constructs upstream request headers for one attempt.
// in supplies the incoming request; v its pre-parsed view; traits the
// detected protocol traits used to pick defaults.
func buildHeaders(in *http.Request, v *view, passthrough bool, targetURL string, traits Traits, plat string) http.Header {
	h := http.Header{}

	if passthrough {
		for k, vv := range in.Header {
			if hopRequestHeaders[strings.ToLower(k)] {
				continue
			}
			for _, v := range vv {
				h.Add(k, v)
			}
		}
		// Protocol header defaults (ports of configureGitHeaders,
		// configureAIHeaders and configureHuggingFaceHeaders).
		if traits.Git || traits.GitLFS {
			configureGitDefaults(h, in, v, traits)
		}
		if traits.AI || traits.HF {
			if in.Method == http.MethodPost && h.Get("Content-Type") == "" {
				h.Set("Content-Type", "application/json")
			}
			if traits.AI && h.Get("User-Agent") == "" {
				h.Set("User-Agent", "Xget-AI-Proxy/1.0")
			}
		}
		return h
	}

	// Standard acceleration profile (mirrors createFetchOptions).
	h.Set("User-Agent", "Wget/1.21.3")
	enc := "gzip, deflate, br"
	rangeHeader := in.Header.Get("Range")
	if rangeHeader != "" || mediaFilePattern.MatchString(targetURL) {
		enc = "identity"
	}
	if mayRewritePlatform(plat) {
		// Rewritable metadata must arrive plain-text: the stdlib has no
		// brotli decoder and rewrites run on the raw body.
		enc = "identity"
	}
	h.Set("Accept-Encoding", enc)
	if origin := in.Header.Get("Origin"); origin != "" {
		h.Set("Origin", origin)
	}
	if auth := in.Header.Get("Authorization"); auth != "" {
		h.Set("Authorization", auth)
	}
	if rangeHeader != "" {
		h.Set("Range", rangeHeader)
	}
	return h
}

// fetchUpstream executes the upstream request with the retry pipeline.
// key/effectivePath come pre-resolved from the caller (docker paths are
// normalized before routing, so resolving them here from the raw path
// would yield no platform).
func (h *Handler) fetchUpstream(in *http.Request, v *view, targetURL string, traits Traits, passthrough bool, key, effectivePath string) *upstreamResult {
	start := time.Now()

	body, err := captureBody(in)
	if err != nil {
		return localTextResult(in.Context(), "Invalid request body", http.StatusBadRequest)
	}

	client := h.client
	if traits.Docker {
		client = h.dockerClient
	}

	maxAttempts := h.cfg.MaxRetries
	if body != nil && !body.replayable() {
		maxAttempts = 1 // cannot replay a huge streamed body
	}

	baseHeaders := buildHeaders(in, v, passthrough, targetURL, traits, key)
	var resp *http.Response
	var actualAttempts int
	timerFired := false
	transportFailed := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		actualAttempts = attempt + 1
		bodyRc, _ := body.get()
		req, err := http.NewRequestWithContext(in.Context(), in.Method, targetURL, bodyRc)
		if err != nil {
			return localTextResult(in.Context(), "Invalid upstream request", http.StatusBadRequest)
		}
		if bodyRc != nil && body != nil && body.buf != nil {
			req.ContentLength = int64(len(body.buf))
		}
		req.Header = buildHeaders(in, v, passthrough, targetURL, traits, key).Clone()

		// Timeout bounds time-to-headers only; once headers arrive the
		// timer is stopped so multi-GB downloads stream without cutoff.
		timerFired = false
		attemptCtx, cancel := context.WithCancel(in.Context())
		timer := time.AfterFunc(time.Duration(h.cfg.TimeoutSecond)*time.Second, func() {
			timerFired = true
			cancel()
		})
		req = req.WithContext(attemptCtx)

		r, err := client.Do(req)
		if err != nil {
			timer.Stop()
			cancel()
			if timerFired {
				return localTextResult(in.Context(), "Request timeout", http.StatusRequestTimeout)
			}
			if in.Context().Err() != nil {
				return localTextResult(in.Context(), "Client disconnected", http.StatusBadGateway)
			}
			// Transport-level failure → backoff and retry.
			transportFailed = true
			if attempt < maxAttempts-1 {
				time.Sleep(time.Duration(h.cfg.RetryDelayMs*(attempt+1)) * time.Millisecond)
			}
			continue
		}

		// Headers arrived in time; detach streaming from the timer.
		timer.Stop()
		if attemptCtx.Err() != nil { // timer fired in the race window
			r.Body.Close()
			cancel()
			return localTextResult(in.Context(), "Request timeout", http.StatusRequestTimeout)
		}
		r.Body = &cancelBody{ReadCloser: r.Body, cancel: cancel}

		if traits.Docker {
			var redirFailed bool
			r, redirFailed = h.followDockerRedirect(r, baseHeaders, body)
			transportFailed = transportFailed || redirFailed
		}

		if (r.StatusCode >= 200 && r.StatusCode < 300) || r.StatusCode == http.StatusPartialContent {
			resp = r
			break
		}

		if traits.Docker && r.StatusCode == http.StatusUnauthorized {
			resp = h.dockerAnonymousRetry(in.Context(), targetURL, key, effectivePath, baseHeaders, body, r, in)
			break
		}

		if r.StatusCode >= 400 && r.StatusCode < 500 {
			resp = r // client error: retrying will not help
			break
		}

		// 5xx / unexpected: drain and retry.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
		r.Body.Close()
		resp = nil
		if attempt < maxAttempts-1 {
			time.Sleep(time.Duration(h.cfg.RetryDelayMs*(attempt+1)) * time.Millisecond)
		}
	}

	if resp == nil {
		if transportFailed {
			// Mirror the original 502 for exhausted transport-level retries.
			return localTextResult(in.Context(), "Upstream request failed", http.StatusBadGateway)
		}
		return localTextResult(in.Context(), "No response received after all retry attempts", http.StatusInternalServerError)
	}

	return &upstreamResult{
		resp:     resp,
		attempts: actualAttempts,
		fetchMs:  float64(time.Since(start).Microseconds()) / 1000.0,
	}
}

func localTextResult(_ context.Context, msg string, status int) *upstreamResult {
	return &upstreamResult{
		resp:     security.BuildErrorResponse(msg, status, false),
		local:    true,
		attempts: 1,
	}
}

// redirectStatus reports 3xx codes that registries use for blob redirects.
func redirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// followDockerRedirect re-issues a registry redirect without forwarding
// credentials (port of followDockerRedirectIfNeeded). Chained redirects are
// resolved by the standard client; only GET/HEAD are re-issued bodyless,
// which covers every registry blob/manifest flow.
func (h *Handler) followDockerRedirect(resp *http.Response, headers http.Header, body *bodyProvider) (*http.Response, bool) {
	// The second return value reports a transport-level failure while
	// chasing the redirect, so the retry loop can classify exhaustion as
	// a 502 - the contract the original gets via fetch exceptions.
	if !redirectStatus(resp.StatusCode) {
		return resp, false
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return resp, false
	}
	base := resp.Request.URL
	next, err := base.Parse(loc)
	if err != nil {
		return resp, false
	}
	resp.Body.Close()

	hdr := headers.Clone()
	hdr.Del("Authorization")
	bodyRc, _ := body.get()
	req, err := http.NewRequestWithContext(resp.Request.Context(), resp.Request.Method, next.String(), bodyRc)
	if err != nil {
		return security.BuildErrorResponse("Bad registry redirect", http.StatusBadGateway, false), true
	}
	if bodyRc != nil && body != nil && body.buf != nil {
		req.ContentLength = int64(len(body.buf))
	}
	req.Header = hdr

	followed, err := h.client.Do(req) // default client: follows chains
	if err != nil {
		return security.BuildErrorResponse("Registry redirect failed", http.StatusBadGateway, false), true
	}
	return followed, false
}

// dockerAnonymousRetry answers a registry 401 challenge by fetching an
// anonymous pull token and retrying once (port of retryDockerWithAnonymousToken).
// On failure a synthesized 401 is returned whose realm points at this
// instance's /cr/<reg>/v2/auth, so docker clients run their credential
// flow through the login proxy (port of responseUnauthorized).
func (h *Handler) dockerAnonymousRetry(ctx context.Context, targetURL, key, effectivePath string,
	headers http.Header, body *bodyProvider, challenge *http.Response, r *http.Request) *http.Response {

	deny := func() *http.Response {
		challenge.Body.Close()
		return responseUnauthorized(r, key)
	}

	authStr := challenge.Header.Get("WWW-Authenticate")
	if authStr == "" {
		return deny()
	}
	ch, err := platform.ParseAuthenticate(authStr)
	if err != nil {
		return deny()
	}
	scope := platform.ScopeFromPath(key, effectivePath)

	treq, err := http.NewRequestWithContext(ctx, http.MethodGet, ch.TokenURL(scope), nil)
	if err != nil {
		return deny()
	}
	tres, err := h.client.Do(treq)
	if err != nil {
		return deny()
	}
	defer tres.Body.Close()
	if tres.StatusCode != http.StatusOK {
		return deny()
	}
	token := readRegistryToken(tres.Body)
	if token == "" {
		return deny()
	}

	hdr := headers.Clone()
	hdr.Set("Authorization", "Bearer "+token)
	bodyRc, _ := body.get()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, bodyRc)
	if err != nil {
		return deny()
	}
	if bodyRc != nil && body != nil && body.buf != nil {
		req.ContentLength = int64(len(body.buf))
	}
	req.Header = hdr

	retryResp, err := h.dockerClient.Do(req)
	if err != nil {
		return deny()
	}
	retryResp, _ = h.followDockerRedirect(retryResp, hdr, body)
	if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
		challenge.Body.Close()
		return retryResp
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(retryResp.Body, 64<<10))
	retryResp.Body.Close()
	return deny()
}

// readRegistryToken extracts token/access_token from a registry token
// response (port of readRegistryTokenResponse).
func readRegistryToken(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	for _, k := range []string{"token", "access_token"} {
		if s, ok := parsed[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// probeHEADContentLength backfills Content-Length on HEAD responses that
// lack it, using a single-byte Range probe (port of the HEAD fallback in
// executeFetch).
func (h *Handler) probeHEADContentLength(ctx context.Context, targetURL string, headers http.Header) string {
	hdr := headers.Clone()
	hdr.Set("Range", "bytes=0-0")
	hdr.Set("Accept-Encoding", "identity")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return ""
	}
	req.Header = hdr
	resp, err := h.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode == http.StatusPartialContent {
		cr := resp.Header.Get("Content-Range")
		if m := reContentRangeTotal.FindStringSubmatch(cr); m != nil {
			return m[1]
		}
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			return cl
		}
	}
	return ""
}

var reContentRangeTotal = regexp.MustCompile(`bytes\s+\d+-\d+/(\d+)`)
