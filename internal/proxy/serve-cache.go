// Cache serving helpers: full and Range (206) responses from disk entries,
// origin detection for content rewriting, and the synthesized registry 401
// challenge that routes docker clients through /v2/auth.
// Ports the cache.match/Range semantics of the Cloudflare Cache API usage
// and responseUnauthorized of src/protocols/docker.js.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xget/internal/cache"
	"xget/internal/security"
)

// requestOrigin mirrors url.origin of the original implementation: the
// client-facing scheme/host, honouring the reverse-proxy proto header.
func requestOrigin(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// rewriteMaxBytes caps how much of a candidate response is buffered for
// text rewriting (index/metadata pages are far smaller in practice).
const rewriteMaxBytes = 32 << 20

// parseSingleRange parses a single "bytes=a-b" / "bytes=a-" / "bytes=-n"
// specification against size. ok=false means "unsupported → serve full".
func parseSingleRange(spec string, size int64) (start, end int64, ok bool) {
	if size <= 0 || !strings.HasPrefix(spec, "bytes=") {
		return 0, 0, false
	}
	spec = strings.TrimPrefix(spec, "bytes=")
	if strings.Contains(spec, ",") { // multiple ranges: unsupported
		return 0, 0, false
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false
	}
	a, b := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	switch {
	case a == "" && b == "":
		return 0, 0, false
	case a == "": // suffix range: last N bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	default:
		start, err := strconv.ParseInt(a, 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, false
		}
		end = size - 1
		if b != "" {
			v, err := strconv.ParseInt(b, 10, 64)
			if err != nil || v < start {
				return 0, 0, false
			}
			if v < end {
				end = v
			}
		}
		return start, end, true
	}
}

// serveFromCache writes a stored entry (full or 206 slice for GET Range,
// headers-only for HEAD). mirrored from the Cloudflare cache.match flow.
func (h *Handler) serveFromCache(w http.ResponseWriter, r *http.Request, entry *cache.Entry, key string, start time.Time) {
	hdr := w.Header()
	for k, vv := range entry.Header {
		if hopResponseHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			hdr.Add(k, v)
		}
	}
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Accept-Ranges", "bytes")
	security.AddSecurityHeaders(hdr)
	security.AddCorsHeaders(hdr, r, h.cfg.AllowedOrigins, h.cfg.AllowedMethods)
	h.emitMetrics(w, r, Traits{}, key, start, 0, 0, "hit")

	isHEAD := r.Method == http.MethodHead
	body, bodyStart, err := entry.OpenBody()
	if err != nil {
		// Entry vanished between Get and open; no upstream fallback here.
		security.WriteError(w, r, "Cached resource unavailable", http.StatusBadGateway, false)
		return
	}
	defer body.Close()

	if isHEAD {
		hdr.Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		w.WriteHeader(entry.Status)
		return
	}

	spec := r.Header.Get("Range")
	if start64, end64, ok := parseSingleRange(spec, entry.Size); ok {
		hdr.Set("Content-Range", cache.FormatRange(start64, end64, entry.Size))
		hdr.Set("Content-Length", strconv.FormatInt(end64-start64+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		// Seeks are absolute: offset by the JSON preamble length that
		// OpenBody reports as the body start.
		if _, err := body.Seek(bodyStart+start64, io.SeekStart); err == nil {
			_, _ = io.CopyN(w, body, end64-start64+1)
		}
		return
	}
	if spec != "" && strings.HasPrefix(spec, "bytes=") && rangeUnsatisfiable(spec, entry.Size) {
		hdr.Set("Content-Range", fmt.Sprintf("bytes */%d", entry.Size))
		security.WriteError(w, r, "Requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable, false)
		return
	}

	hdr.Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.WriteHeader(entry.Status)
	_, _ = io.Copy(w, body)
}

// rangeUnsatisfiable reports whether every byte position lies past the end.
func rangeUnsatisfiable(spec string, size int64) bool {
	spec = strings.TrimPrefix(spec, "bytes=")
	dash := strings.IndexByte(spec, '-')
	if dash <= 0 {
		return false
	}
	a, err := strconv.ParseInt(strings.TrimSpace(spec[:dash]), 10, 64)
	return err == nil && a >= size
}

// responseUnauthorized builds the registry-style 401 challenge whose realm
// points back at this instance's /cr/<reg>/v2/auth, so docker clients run
// their credential flow through the login proxy (port of responseUnauthorized).
func responseUnauthorized(r *http.Request, key string) *http.Response {
	realm := "/v2/auth"
	if key != "" {
		realm = "/cr/" + strings.TrimPrefix(key, "cr-") + "/v2/auth"
	}
	body := `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required","detail":null}]}`
	raw := "HTTP/1.1 401 Unauthorized\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("WWW-Authenticate: Bearer realm=\"%s%s\",service=\"Xget\"\r\n", requestOrigin(r), realm) +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)) +
		body
	resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(raw)), nil)
	if err != nil {
		return security.BuildErrorResponse("Authentication required", http.StatusUnauthorized, false)
	}
	return resp
}
