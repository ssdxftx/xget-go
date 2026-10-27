// Docker registry login proxy (/v2/auth), ported from the handleDockerAuth
// flow of src/protocols/docker.js: resolves the target registry from the
// request path and/or token scope, probes the upstream /v2/ endpoint for
// its WWW-Authenticate challenge, then relays the client's credentials to
// the upstream token service and returns the bearer token response.
//
// This is what makes `docker login <instance>/cr/<reg>` work for private
// repositories: the docker client performs its full credential handshake
// against this endpoint.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"io"
	"net/http"
	"regexp"
	"strings"

	"xget/internal/platform"
	"xget/internal/security"
)

var reCRAuthPath = regexp.MustCompile(`^/cr/([^/]+)/v2/auth/?$`)

// resolveDockerAuthTarget ports resolveDockerAuthTarget: the registry comes
// either from the path (/cr/<reg>/v2/auth) or from a scope of the form
// repository:cr/<reg>/<repo>:<actions>; Docker Hub single-segment repos are
// normalized to library/<name>.
func resolveDockerAuthTarget(path, scopeParam string) (key, upstreamScope string, err error) {
	if m := reCRAuthPath.FindStringSubmatch(path); m != nil {
		key = "cr-" + m[1]
	}

	repoPath := ""
	upstreamScope = scopeParam
	if scopeParam != "" {
		parts := strings.Split(scopeParam, ":")
		if len(parts) >= 3 && parts[0] == "repository" {
			full := parts[1]
			if strings.HasPrefix(full, "cr/") {
				for _, e := range platform.All() {
					if !platform.IsContainer(e.Key) {
						continue
					}
					prefix := strings.ReplaceAll(e.Key, "-", "/") // e.g. cr/ghcr
					if strings.HasPrefix(full, prefix+"/") {
						key = e.Key
						repoPath = full[len(prefix)+1:]
						break
					}
				}
			} else {
				repoPath = full
			}
			repoPath = platform.NormalizeRepo(key, repoPath)
			if repoPath != "" {
				upstreamScope = "repository:" + repoPath + ":" + strings.Join(parts[2:], ":")
			}
		}
	}

	if key == "" {
		return "", "", errUnsupportedRegistry
	}
	if _, ok := platform.Catalog[key]; !ok {
		return "", "", errUnsupportedRegistry
	}
	return key, upstreamScope, nil
}

type authTargetError struct{}

func (authTargetError) Error() string { return "Unsupported registry platform in scope" }

var errUnsupportedRegistry error = authTargetError{}

// handleDockerAuth implements the /v2/auth endpoint. localError is the
// caller's locally-generated-response writer so error replies carry the
// same global header set as every other response.
//
// Error semantics mirror the original flow, where every failure inside
// handleDockerAuth (unreachable registry, malformed challenge, unreachable
// token service, unconstructible fetch URL) propagates to the top-level
// catch and surfaces as the plain-text 500 "Internal Server Error"
// contract; only the explicit resolveDockerAuthTarget rejection maps to 400.
func (h *Handler) handleDockerAuth(w http.ResponseWriter, r *http.Request, path string, traits Traits, localError func(Traits, string, int)) {
	key, scope, err := resolveDockerAuthTarget(path, r.URL.Query().Get("scope"))
	if err != nil {
		localError(traits, "Invalid Docker authentication request", http.StatusBadRequest)
		return
	}
	upstream := platform.Catalog[key]

	// 1. Probe the upstream /v2/ endpoint for its auth challenge.
	v2req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream+"/v2/", nil)
	if err != nil {
		// Original: new URL(upstreamUrl + '/v2/') throwing → top-level catch → 500.
		localError(traits, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	v2resp, err := h.client.Do(v2req) // follows redirects, like the original
	if err != nil {
		// Original: the fetch throws → top-level catch → 500.
		localError(traits, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	forward := func(resp *http.Response) {
		defer func() {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}()
		hdr := w.Header()
		for k, vv := range resp.Header {
			if hopResponseHeaders[strings.ToLower(k)] {
				continue
			}
			for _, v := range vv {
				hdr.Add(k, v)
			}
		}
		security.AddSecurityHeaders(hdr)
		security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
		w.WriteHeader(resp.StatusCode)
		if r.Method != http.MethodHead && resp.Body != nil {
			_, _ = io.Copy(w, resp.Body)
		}
	}

	if v2resp.StatusCode != http.StatusUnauthorized {
		forward(v2resp) // registry needs no auth (or errored): relay as-is
		return
	}
	authStr := v2resp.Header.Get("WWW-Authenticate")
	if authStr == "" {
		forward(v2resp)
		return
	}
	challenge, err := platform.ParseAuthenticate(authStr)
	if err != nil {
		// Original: parseAuthenticate throws → top-level catch → 500.
		_, _ = io.Copy(io.Discard, io.LimitReader(v2resp.Body, 64<<10))
		v2resp.Body.Close()
		localError(traits, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(v2resp.Body, 64<<10))
	v2resp.Body.Close()

	// 2. Relay the client's credentials to the upstream token service.
	treq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, challenge.TokenURL(scope), nil)
	if err != nil {
		// Original: new URL(realm) throwing inside fetchToken → top-level
		// catch → 500.
		localError(traits, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		treq.Header.Set("Authorization", auth)
	}
	tres, err := h.client.Do(treq)
	if err != nil {
		// Original: the token fetch throws → top-level catch → 500.
		localError(traits, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	forward(tres)
}
