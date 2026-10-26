// Request trait detection, ported from src/utils/validation.js and
// src/protocols/git.js / docker.js of the original project.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package proxy

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// view is a lightweight snapshot of the request fields used by protocol
// detection, so each detector does not repeat header lookups.
type view struct {
	Path       string        // escaped (still percent-encoded) path
	RawPath    string        // raw path as received
	Query      url.Values    // query parameters
	UserAgent  string        // User-Agent header
	Accept     string        // Accept header
	ContentType string       // Content-Type header
}

func newView(r *http.Request) *view {
	return &view{
		Path:        r.URL.EscapedPath(),
		RawPath:     r.URL.RawPath,
		Query:       r.URL.Query(),
		UserAgent:   r.Header.Get("User-Agent"),
		Accept:      r.Header.Get("Accept"),
		ContentType: r.Header.Get("Content-Type"),
	}
}

// Traits describes protocol-specific request shapes.
type Traits struct {
	Git    bool
	GitLFS bool
	Docker bool
}

// IsProtocol reports whether the request follows a special protocol that
// gets raw passthrough handling (headers preserved, no rewriting, no error
// wrapping, no performance header).
func (t Traits) IsProtocol() bool { return t.Git || t.GitLFS || t.Docker }

var reSHA256Object = regexp.MustCompile(`/objects/[a-fA-F0-9]{64}$`)
var reCRv2 = regexp.MustCompile(`^/cr/[^/]+/v2(?:/|$)`)

var dockerManifestAccepts = []string{
	"application/vnd.docker.distribution.manifest",
	"application/vnd.oci.image.manifest",
	"application/vnd.docker.image.rootfs.diff.tar.gzip",
}

func isDockerRequest(v *view) bool {
	path := v.Path
	if path == "/v2" || path == "/v2/" || strings.HasPrefix(path, "/v2/") {
		return true
	}
	if strings.HasPrefix(path, "/cr/") {
		if reCRv2.MatchString(path) {
			return true
		}
		ua := strings.ToLower(v.UserAgent)
		if strings.Contains(ua, "docker/") {
			return true
		}
		for _, m := range dockerManifestAccepts {
			if strings.Contains(v.Accept, m) || strings.Contains(v.ContentType, m) {
				return true
			}
		}
	}
	return false
}

func isGitRequest(v *view) bool {
	path := v.Path
	if strings.HasSuffix(path, "/info/refs") {
		return true
	}
	if strings.HasSuffix(path, "/git-upload-pack") || strings.HasSuffix(path, "/git-receive-pack") {
		return true
	}
	ua := v.UserAgent
	if strings.Contains(ua, "git/") || strings.HasPrefix(ua, "git/") {
		return true
	}
	if svc := v.Query.Get("service"); svc == "git-upload-pack" || svc == "git-receive-pack" {
		return true
	}
	ct := v.ContentType
	if strings.Contains(ct, "git-upload-pack") || strings.Contains(ct, "git-receive-pack") {
		return true
	}
	return false
}

func isGitLFSRequest(v *view) bool {
	path := v.Path
	if strings.Contains(path, "/info/lfs") {
		return true
	}
	if strings.Contains(path, "/objects/batch") {
		return true
	}
	if reSHA256Object.MatchString(path) {
		return true
	}
	if strings.Contains(v.Accept, "application/vnd.git-lfs") ||
		strings.Contains(v.ContentType, "application/vnd.git-lfs") {
		return true
	}
	return strings.Contains(v.UserAgent, "git-lfs")
}

// Detect computes protocol traits for a request.
func Detect(v *view) Traits {
	return Traits{
		Git:    isGitRequest(v),
		GitLFS: isGitLFSRequest(v),
		Docker: isDockerRequest(v),
	}
}
