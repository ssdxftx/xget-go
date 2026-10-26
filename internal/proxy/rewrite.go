// Platform-specific upstream response rewriting, ported from
// src/utils/rewrite.js of the original project: keeps follow-up requests
// flowing through this instance by replacing upstream file hosts inside
// index/metadata responses.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"regexp"
	"strings"
)

var (
	rePyPIFileHost  = regexp.MustCompile(`https://files\.pythonhosted\.org`)
	reNpmRegistry   = regexp.MustCompile(`https://registry\.npmjs\.org/([^/"]+)`)
	reFlathubRepo   = regexp.MustCompile(`https://(?:dl\.)?flathub\.org/repo/`)
	reFlatpakRefExt = regexp.MustCompile(`(?i)\.(flatpakrepo|flatpakref)$`)
)

// isFlatpakReferenceFilePath reports whether the request targets a Flatpak
// descriptor file (.flatpakrepo / .flatpakref).
func isFlatpakReferenceFilePath(requestPath string) bool {
	return reFlatpakRefExt.MatchString(requestPath)
}

// shouldRewriteTextResponse mirrors shouldRewriteTextResponse: which
// successful responses get their body rewritten.
func shouldRewriteTextResponse(plat, requestPath, contentType string) bool {
	switch plat {
	case "pypi":
		return strings.Contains(contentType, "text/html")
	case "npm":
		return strings.Contains(contentType, "application/json")
	case "flathub":
		return isFlatpakReferenceFilePath(requestPath)
	}
	return false
}

// rewriteTextResponse mirrors rewriteTextResponse: rewrites upstream hosts
// so the client's next hop returns to this instance.
func rewriteTextResponse(plat, requestPath, original, origin string) string {
	switch plat {
	case "pypi":
		return rePyPIFileHost.ReplaceAllLiteralString(original, origin+"/pypi/files")
	case "npm":
		return reNpmRegistry.ReplaceAllString(original, origin+"/npm/$1")
	case "flathub":
		if isFlatpakReferenceFilePath(requestPath) {
			return reFlathubRepo.ReplaceAllLiteralString(original, origin+"/flathub/repo/")
		}
	}
	return original
}

// mayRewritePlatform reports whether a platform can ever rewrite bodies.
// Used to request identity encoding upstream so the rewriter always sees
// plain text (Go's stdlib cannot decode brotli).
func mayRewritePlatform(plat string) bool {
	return plat == "pypi" || plat == "npm" || plat == "flathub"
}
