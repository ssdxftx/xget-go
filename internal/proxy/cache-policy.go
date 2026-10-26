// Cache policy, ported from src/upstream/cache-policy.js of the original
// project: classifies requests into private / bypass / edge modes and
// assigns TTL classes (immutable artifacts, metadata indexes, mutable
// fallback), then adjusts for upstream response privacy directives.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"net/http"
	"regexp"
	"strings"
)

// TTL classes of the original implementation.
const (
	metadataEdgeTTLSeconds = 60
	immutableEdgeTTL       = 86400
	immutableBrowserTTL    = 3600
	mutableEdgeTTL         = 300 // fallback when CACHE_TTL is unset
)

var (
	reGithubArtifact  = regexp.MustCompile(`(?i)\.(?:tgz|whl|jar|zip|gem|crate|deb|rpm|nupkg|tar\.gz|tar\.bz2|tar\.xz)(?:$|[?#])`)
	reMavenArtifact   = regexp.MustCompile(`(?i)\.(?:jar|pom|war|aar|module)(?:$|[?#])`)
	rePypiFileArt     = regexp.MustCompile(`(?i)\.(?:whl|zip|tar\.gz|tar\.bz2|tar\.xz)$`)
	reNpmTarball      = regexp.MustCompile(`(?i)^/(?:npm/)?(?:@[^/]+/)?[^/]+/-/[^/]+\.tgz$`)
	reVersionLikeTail = regexp.MustCompile(`(?:^|[-_.])v?\d`)
)

var movingVersionAliases = map[string]bool{
	"current": true, "dev": true, "edge": true, "latest": true, "main": true,
	"master": true, "nightly": true, "snapshot": true, "stable": true,
}

func hasVersionLikeToken(value string) bool {
	v := strings.ToLower(value)
	if movingVersionAliases[v] {
		return false
	}
	return reVersionLikeTail.MatchString(v)
}

func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

func getGithubReleaseTag(path string) string {
	parts := strings.Split(path, "/")
	filtered := parts[:0]
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	for i, seg := range filtered {
		if seg == "releases" && i+1 < len(filtered) && filtered[i+1] == "download" {
			if i+2 < len(filtered) {
				return filtered[i+2]
			}
			return ""
		}
	}
	return ""
}

// isGithubReleaseArtifact: release assets (not branch archives).
func isGithubReleaseArtifact(effectivePath, targetPath string) bool {
	effTag := getGithubReleaseTag(effectivePath)
	tgtTag := getGithubReleaseTag(targetPath)
	if !strings.Contains(effectivePath, "/releases/download/") &&
		!strings.Contains(targetPath, "/releases/download/") {
		return false
	}
	if !reGithubArtifact.MatchString(effectivePath) && !reGithubArtifact.MatchString(targetPath) {
		return false
	}
	return (effTag != "" && hasVersionLikeToken(effTag)) ||
		(tgtTag != "" && hasVersionLikeToken(tgtTag))
}

func isMavenReleaseArtifactPath(pathname string) bool {
	parts := strings.Split(pathname, "/")
	filtered := parts[:0]
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) < 4 || !reMavenArtifact.MatchString(pathname) {
		return false
	}
	version := filtered[len(filtered)-2]
	return strings.ContainsAny(version, "0123456789") &&
		!strings.Contains(strings.ToUpper(version), "SNAPSHOT")
}

func isVersionedPyPIFilePath(pathname string) bool {
	if !rePypiFileArt.MatchString(pathname) {
		return false
	}
	base := lastSegment(pathname)
	stem := rePypiFileArt.ReplaceAllString(base, "")
	return hasVersionLikeToken(stem)
}

// isImmutableArtifactRequest mirrors isImmutableArtifactRequest.
func isImmutableArtifactRequest(plat, effectivePath, targetURL string) bool {
	targetPath := targetURL
	if i := strings.IndexAny(targetPath, "?#"); i >= 0 {
		targetPath = targetPath[:i]
	}
	switch plat {
	case "npm":
		return reNpmTarball.MatchString(effectivePath) || reNpmTarball.MatchString(targetPath)
	case "pypi-files":
		return isVersionedPyPIFilePath(effectivePath) || isVersionedPyPIFilePath(targetPath)
	case "maven":
		return isMavenReleaseArtifactPath(effectivePath) || isMavenReleaseArtifactPath(targetPath)
	case "crates":
		return strings.HasSuffix(effectivePath, ".crate") || strings.HasSuffix(targetPath, ".crate")
	case "rubygems":
		return strings.HasSuffix(effectivePath, ".gem") || strings.HasSuffix(targetPath, ".gem")
	case "gh":
		return isGithubReleaseArtifact(effectivePath, targetPath)
	}
	return false
}

// isNpmMetadataPath: rewritten package metadata (not tarballs).
func isNpmMetadataPath(effectivePath string) bool {
	return strings.HasPrefix(effectivePath, "/npm/") &&
		!reNpmTarball.MatchString(effectivePath)
}

// shouldVaryCacheByOrigin mirrors shouldVaryCacheByOrigin: rewritten
// responses whose body embeds the requesting origin (npm package metadata,
// flathub .flatpakref/.flatpakrepo descriptors) must not share one cache
// entry across different client-facing origins.
func shouldVaryCacheByOrigin(plat, effectivePath string) bool {
	return (plat == "flathub" && isFlatpakReferenceFilePath(effectivePath)) ||
		(plat == "npm" && isNpmMetadataPath(effectivePath))
}

// isMetadataOrIndexPath mirrors isMetadataOrIndexPath.
func isMetadataOrIndexPath(plat, effectivePath string) bool {
	switch plat {
	case "npm":
		return isNpmMetadataPath(effectivePath)
	case "pypi":
		return strings.HasPrefix(effectivePath, "/pypi/simple/") || effectivePath == "/pypi/simple"
	case "maven":
		return strings.HasSuffix(effectivePath, "/maven-metadata.xml")
	case "flathub":
		return effectivePath == "/flathub/repo/summary" ||
			effectivePath == "/flathub/repo/summary.sig"
	}
	return false
}

// CacheMode classifies how the disk cache may participate.
type CacheMode string

const (
	CachePrivate CacheMode = "private" // sensitive: never store, no-store
	CacheBypass  CacheMode = "bypass"  // protocol/uncacheable: never store
	CacheEdge    CacheMode = "edge"    // store with the class TTL
)

// CachePolicy is the resolved per-request policy (port of the object
// returned by resolveCachePolicy).
type CachePolicy struct {
	Mode        CacheMode
	EdgeTTL     int // seconds, drives disk-cache expiry
	BrowserTTL  int // seconds, drives max-age
	Immutable   bool
	Cacheable   bool // true when the response may be written to the disk cache
	NoStore     bool // emit "no-store" instead of TTLs
	PrivateOnly bool // emit "private, no-store" + Vary
}

// resolveCachePolicy ports resolveCachePolicy (request-side).
func resolveCachePolicy(canUseCache, sensitive bool, traits Traits, plat, effectivePath, targetURL string, mutableFallbackTTL int) CachePolicy {
	isProtocol := traits.IsProtocol()

	if sensitive {
		return CachePolicy{Mode: CachePrivate, PrivateOnly: true}
	}
	if !canUseCache || isProtocol {
		return CachePolicy{Mode: CacheBypass, NoStore: true}
	}

	if isImmutableArtifactRequest(plat, effectivePath, targetURL) {
		return CachePolicy{
			Mode: CacheEdge, EdgeTTL: immutableEdgeTTL, BrowserTTL: immutableBrowserTTL,
			Immutable: true, Cacheable: true,
		}
	}
	if isMetadataOrIndexPath(plat, effectivePath) {
		return CachePolicy{
			Mode: CacheEdge, EdgeTTL: metadataEdgeTTLSeconds, BrowserTTL: 0, Cacheable: true,
		}
	}

	ttl := mutableFallbackTTL
	if ttl <= 0 {
		ttl = mutableEdgeTTL
	}
	return CachePolicy{Mode: CacheEdge, EdgeTTL: ttl, BrowserTTL: 0, Cacheable: true}
}

// hasCacheControlDirective reports whether the directive is present.
func hasCacheControlDirective(cacheControl, directive string) bool {
	for _, part := range strings.Split(cacheControl, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if name == directive {
			return true
		}
	}
	return false
}

// resolveResponseCachePolicy ports resolveResponseCachePolicy: upstream
// privacy directives downgrade the stored/emitted policy.
func resolveResponseCachePolicy(base CachePolicy, upstreamHeader http.Header) CachePolicy {
	if base.Mode == CachePrivate {
		return CachePolicy{Mode: CachePrivate, PrivateOnly: true}
	}
	cc := upstreamHeader.Get("Cache-Control")
	if upstreamHeader.Get("Set-Cookie") != "" || hasCacheControlDirective(cc, "private") {
		return CachePolicy{Mode: CachePrivate, PrivateOnly: true}
	}
	if hasCacheControlDirective(cc, "no-store") || hasCacheControlDirective(cc, "no-cache") {
		return CachePolicy{Mode: CacheBypass, NoStore: true}
	}
	for _, v := range upstreamHeader.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == "*" {
				return CachePolicy{Mode: CacheBypass, NoStore: true}
			}
		}
	}
	return base
}

// CacheControlValue builds the Cache-Control header value.
func (p CachePolicy) CacheControlValue() string {
	switch {
	case p.PrivateOnly:
		return "private, no-store"
	case p.NoStore:
		return "no-store"
	case p.Immutable:
		return "public, max-age=3600, s-maxage=86400, immutable"
	default:
		return "public, max-age=0, s-maxage=" + itoa(p.EdgeTTL) + ", must-revalidate"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
