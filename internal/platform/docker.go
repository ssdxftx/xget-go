// Docker/OCI registry helpers ported from src/protocols/docker.js of the
// original project: WWW-Authenticate challenge parsing, token-service URL
// construction, scope extraction and registry path normalization. The
// /v2/auth login proxy itself lives in internal/proxy/docker-auth.go.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package platform

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	reRealm   = regexp.MustCompile(`realm="([^"]+)"`)
	reService = regexp.MustCompile(`service="([^"]+)"`)
)

// AuthChallenge is a parsed WWW-Authenticate: Bearer ... challenge.
type AuthChallenge struct {
	Realm   string
	Service string
}

// ParseAuthenticate parses a Docker/OCI registry WWW-Authenticate header.
func ParseAuthenticate(header string) (*AuthChallenge, error) {
	r := reRealm.FindStringSubmatch(header)
	s := reService.FindStringSubmatch(header)
	if r == nil || s == nil {
		return nil, fmt.Errorf("invalid WWW-Authenticate header: %s", header)
	}
	return &AuthChallenge{Realm: r[1], Service: s[1]}, nil
}

// TokenURL builds the token-service request URL for a challenge
// (realm + service + optional scope, like fetchToken upstream: query
// values are percent-encoded and an empty service is omitted).
func (c *AuthChallenge) TokenURL(scope string) string {
	q := url.Values{}
	if c.Service != "" {
		q.Set("service", c.Service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	if enc := q.Encode(); enc != "" {
		sep := "?"
		if strings.Contains(c.Realm, "?") {
			sep = "&"
		}
		return c.Realm + sep + enc
	}
	return c.Realm
}

// extractRepositoryPath pulls "namespace/repo" out of a registry API path.
func extractRepositoryPath(apiPath string) string {
	normalized := apiPath
	if strings.HasPrefix(normalized, "/v2/") {
		normalized = normalized[4:]
	} else {
		normalized = strings.TrimLeft(normalized, "/")
	}
	parts := strings.Split(normalized, "/")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 || strings.HasPrefix(filtered[0], "_") {
		return ""
	}
	for i, p := range filtered {
		switch p {
		case "manifests", "blobs", "tags", "referrers":
			if i == 0 {
				return ""
			}
			return strings.Join(filtered[:i], "/")
		}
	}
	return ""
}

// normalizeRepoPath maps single-segment Docker Hub images to library/<name>.
func normalizeRepoPath(key, repoPath string) string {
	if key == "cr-docker" && repoPath != "" && !strings.Contains(repoPath, "/") {
		return "library/" + repoPath
	}
	return repoPath
}

// NormalizeRepo exposes normalizeRepoPath for the /v2/auth login proxy
// (port of the normalizeRepoPath call inside resolveDockerAuthTarget).
func NormalizeRepo(key, repoPath string) string {
	return normalizeRepoPath(key, repoPath)
}

// NormalizeRegistryApiPath applies registry-specific path normalization,
// e.g. "docker pull ubuntu" → "/v2/library/ubuntu/manifests/latest".
func NormalizeRegistryApiPath(key, apiPath string) string {
	if key != "cr-docker" || !strings.HasPrefix(apiPath, "/v2/") {
		return apiPath
	}
	repo := extractRepositoryPath(apiPath)
	normalized := normalizeRepoPath(key, repo)
	if repo == "" || normalized == repo {
		return apiPath
	}
	return strings.Replace(apiPath, "/v2/"+repo, "/v2/"+normalized, 1)
}

// ScopeFromPath derives the registry token scope for anonymous token retry,
// port of getScopeFromUrl. Returns "" when no repository can be determined.
func ScopeFromPath(key, effectivePath string) string {
	if strings.Contains(effectivePath, "/_catalog") {
		return "registry:catalog:*"
	}
	apiPath := strings.TrimPrefix(effectivePath, URLPrefix(key))
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	apiPath = NormalizeRegistryApiPath(key, apiPath)
	repo := extractRepositoryPath(apiPath)
	if repo == "" {
		return ""
	}
	return "repository:" + repo + ":pull"
}
