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
	h.Add("Vary", "Origin")
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

// WriteError emits a standardized error response with security headers,
// port of createErrorResponse (plain text by default, JSON with details
// when includeDetails is set).
func WriteError(w http.ResponseWriter, r *http.Request, message string, status int, includeDetails bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if includeDetails {
		w.Header().Set("Content-Type", "application/json")
	}
	AddSecurityHeaders(w.Header())
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return
	}
	if includeDetails {
		payload, _ := json.Marshal(struct {
			Error     string `json:"error"`
			Status    int    `json:"status"`
			Timestamp string `json:"timestamp"`
		}{Error: message, Status: status, Timestamp: time.Now().UTC().Format(time.RFC3339)})
		_, _ = w.Write(payload)
		return
	}
	_, _ = w.Write([]byte(message))
}

// BuildErrorResponse constructs a local (non-proxied) error response as an
// *http.Response so it can flow through the same finalize path as upstream
// responses, carrying security headers like createErrorResponse upstream.
func BuildErrorResponse(message string, status int, includeDetails bool) *http.Response {
	body := message
	ct := "text/plain; charset=utf-8"
	if includeDetails {
		ct = "application/json"
		payload, _ := json.Marshal(struct {
			Error     string `json:"error"`
			Status    int    `json:"status"`
			Timestamp string `json:"timestamp"`
		}{Error: message, Status: status, Timestamp: time.Now().UTC().Format(time.RFC3339)})
		body = string(payload)
	}
	h := http.Header{}
	h.Set("Content-Type", ct)
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
