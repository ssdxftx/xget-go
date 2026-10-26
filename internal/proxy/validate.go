// Request validation, ported from src/utils/validation.js of the original
// project: method whitelist, path length limit, traversal and control
// character rejection.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"xget/internal/config"
)

// rePctEncoded matches a well-formed percent-escape (%XX). Only the
// presence of one triggers decoding, mirroring the original
// /%[0-9a-fA-F]{2}/ gate in decodePathnameForValidation (a bare % in a
// filename is passed through untouched).
var rePctEncoded = regexp.MustCompile(`%[0-9a-fA-F]{2}`)

// hasPlainDotDot detects literal traversal segments without decoding,
// mirroring the raw-URL check /(^|\/)+\.\.($|\/)/ in validateRequest.
func hasPlainDotDot(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

var protocolMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

func methodAllowed(method string, allowed []string) bool {
	for _, m := range allowed {
		if m == method {
			return true
		}
	}
	return false
}

// decodeTwice best-effort decodes a percent-encoded path up to two times,
// mirroring decodePathnameForValidation (catches %2e%2e%2f traversal).
// Decoding is only attempted when a well-formed escape exists; a malformed
// escape alongside a valid one makes the whole decode fail (ok=false),
// exactly like the original decodeURIComponent failure path.
func decodeTwice(p string) (string, bool) {
	decoded := p
	for i := 0; i < 2; i++ {
		if !rePctEncoded.MatchString(decoded) {
			break
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		decoded = next
	}
	return decoded, true
}

func hasPathTraversal(p string) bool {
	decoded, ok := decodeTwice(p)
	if !ok {
		return true
	}
	decoded = strings.ReplaceAll(decoded, "\\", "/")
	for _, seg := range strings.Split(decoded, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func hasASCIIControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= 31 || s[i] == 127 {
			return true
		}
	}
	return false
}

// Validate mirrors validateRequest: protocol requests widen the method
// whitelist; everyone is subject to path length and traversal checks.
func Validate(r *http.Request, v *view, traits Traits, cfg *config.Config) (int, string) {
	allowed := cfg.AllowedMethods
	if traits.IsProtocol() {
		allowed = protocolMethods
	}
	if !methodAllowed(r.Method, allowed) {
		return http.StatusMethodNotAllowed, "Method not allowed"
	}

	if len(v.Path) > cfg.MaxPathLength {
		return http.StatusRequestURITooLong, "Path too long"
	}

	// Raw path as received over the wire (before any normalization):
	// literal ".." segments only, no decoding (port of the raw-URL
	// traversal regex in validateRequest).
	rawPath := v.RawPath
	if rawPath == "" {
		rawPath = v.Path
	}
	rawPath = strings.ReplaceAll(rawPath, "\\", "/")
	if hasPlainDotDot(rawPath) {
		return http.StatusBadRequest, "Invalid path"
	}

	if hasASCIIControlChars(v.Path) {
		return http.StatusBadRequest, "Invalid path"
	}
	if hasPathTraversal(v.Path) {
		return http.StatusBadRequest, "Invalid path"
	}
	return 0, ""
}
