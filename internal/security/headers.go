// Package security ports the response-header utilities of the original
// project (src/utils/security.js): security headers, CORS handling and
// standardized error responses.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package security

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AddSecurityHeaders applies the same hardening headers as upstream.
func AddSecurityHeaders(h http.Header) {
	h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-XSS-Protection", "1; mode=block")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'none'")
	h.Set("Permissions-Policy", "interest-cohort=()")
}

// allowedOrigin resolves the CORS origin echo value, or "" when denied.
func allowedOrigin(r *http.Request, allowedOrigins []string) string {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	for _, o := range allowedOrigins {
		if o == "*" {
			return "*"
		}
	}
	for _, o := range allowedOrigins {
		if o == origin {
			return origin
		}
	}
	return ""
}

// AddCorsHeaders appends CORS headers when the request origin is allowed.
// Returns true when a CORS-enabled origin is present.
func AddCorsHeaders(h http.Header, r *http.Request, allowedOrigins, allowedMethods []string) bool {
	origin := allowedOrigin(r, allowedOrigins)
	if origin == "" {
		return false
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", joinComma(allowedMethods))
	if reqHdrs := r.Header.Get("Access-Control-Request-Headers"); reqHdrs != "" {
		h.Set("Access-Control-Allow-Headers", reqHdrs)
	}
	// Merge Origin into any existing Vary values (deduplicated), mirroring
	// the set-based merge of the original addCorsHeaders.
	seen := map[string]bool{}
	order := make([]string, 0, 4)
	for _, v := range h.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			p := strings.TrimSpace(part)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			order = append(order, p)
		}
	}
	if !seen["Origin"] {
		order = append(order, "Origin")
	}
	h.Set("Vary", strings.Join(order, ", "))
	return true
}

func joinComma(list []string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// isoTimestampLayout matches Date#toISOString: always UTC with exactly three
// fractional digits and a trailing Z.
const isoTimestampLayout = "2006-01-02T15:04:05.000Z"

// WriteError emits a standardized error response with security headers,
// port of createErrorResponse (plain text by default, JSON with details
// when includeDetails is set).
func WriteError(w http.ResponseWriter, r *http.Request, message string, status int, includeDetails bool) {
	payload := []byte(message)
	contentType := "text/plain"
	if includeDetails {
		contentType = "application/json"
		payload, _ = json.Marshal(struct {
			Error     string `json:"error"`
			Status    int    `json:"status"`
			Timestamp string `json:"timestamp"`
		}{Error: message, Status: status, Timestamp: time.Now().UTC().Format(isoTimestampLayout)})
	}
	w.Header().Set("Content-Type", contentType)
	// Frame the body explicitly so error responses (e.g. a cached 416) never
	// inherit a stale Content-Length from a previously copied resource.
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	AddSecurityHeaders(w.Header())
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(payload)
}

// BuildErrorResponse constructs a local (non-proxied) error response as an
// *http.Response so it can flow through the same finalize path as upstream
// responses, carrying security headers like createErrorResponse upstream.
func BuildErrorResponse(message string, status int, includeDetails bool) *http.Response {
	body := message
	ct := "text/plain"
	if includeDetails {
		ct = "application/json"
		payload, _ := json.Marshal(struct {
			Error     string `json:"error"`
			Status    int    `json:"status"`
			Timestamp string `json:"timestamp"`
		}{Error: message, Status: status, Timestamp: time.Now().UTC().Format(isoTimestampLayout)})
		body = string(payload)
	}
	h := http.Header{}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	AddSecurityHeaders(h)
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// WriteJSONError writes a JSON error body without the wrapping contract
// (used for synthesized protocol-level errors such as registry 401s).
func WriteJSONError(w http.ResponseWriter, r *http.Request, status int, headers http.Header, body string) {
	for k, vs := range headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	AddSecurityHeaders(w.Header())
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = fmt.Fprint(w, body)
	}
}
