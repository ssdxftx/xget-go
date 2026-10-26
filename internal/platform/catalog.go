// Package platform ports the Xget platform catalog and URL routing rules
// (src/config/platform-catalog.js, src/routing/* of the original project).
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package platform

import (
	"sort"
	"strings"
)

// Catalog maps a platform key to its upstream base URL.
//
// Core high-frequency subset of the original 100+ entry catalog, covering
// code hosting, AI model hubs, package registries and container registries.
// Unknown keys are unknown — the handler redirects to the homepage, exactly
// like the original.
var Catalog = map[string]string{
	// Code Repositories & Version Control
	"gh":       "https://github.com",
	"gist":     "https://gist.github.com",
	"gl":       "https://gitlab.com",
	"gitea":    "https://gitea.com",
	"codeberg": "https://codeberg.org",

	// AI / Model Hubs
	"hf":      "https://huggingface.co",
	"civitai": "https://civitai.com",

	// Package Managers
	"npm":             "https://registry.npmjs.org",
	"pypi":            "https://pypi.org",
	"pypi-files":      "https://files.pythonhosted.org",
	"conda":           "https://repo.anaconda.com",
	"conda-community": "https://conda.anaconda.org",
	"maven":           "https://repo1.maven.org",
	"golang":          "https://proxy.golang.org",
	"crates":          "https://crates.io",
	"nuget":           "https://api.nuget.org",
	"rubygems":        "https://rubygems.org",

	// Container Registries
	"cr-docker": "https://registry-1.docker.io",
	"cr-ghcr":   "https://ghcr.io",
	"cr-quay":   "https://quay.io",
	"cr-gcr":    "https://gcr.io",
	"cr-mcr":    "https://mcr.microsoft.com",
	"cr-k8s":    "https://registry.k8s.io",
}

// Entry is a flattened, pre-computed routing rule.
type Entry struct {
	Key    string // platform key, e.g. "pypi-files"
	Prefix string // URL prefix, e.g. "/pypi/files/"
	Base   string // upstream base URL
}

var sortedEntries = func() []Entry {
	out := make([]Entry, 0, len(Catalog))
	for k, base := range Catalog {
		out = append(out, Entry{
			Key:    k,
			Prefix: "/" + strings.ReplaceAll(k, "-", "/") + "/",
			Base:   base,
		})
	}
	// Longest prefix first so that "/pypi/files/..." resolves to
	// "pypi-files" instead of "pypi" (mirrors SORTED_PLATFORMS upstream).
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Prefix) != len(out[j].Prefix) {
			return len(out[i].Prefix) > len(out[j].Prefix)
		}
		return out[i].Key < out[j].Key
	})
	return out
}()

// All returns routing entries sorted by longest prefix first.
func All() []Entry { return sortedEntries }

// Resolve matches an effective request path against the catalog.
// It first tries the longest-prefix match, then falls back to the first
// path segment (so "/gh" still resolves to platform "gh" and is then
// bounced to the homepage, like the original resolveTarget).
func Resolve(effectivePath string) (string, bool) {
	for _, e := range sortedEntries {
		if strings.HasPrefix(effectivePath, e.Prefix) {
			return e.Key, true
		}
	}
	seg := effectivePath
	seg = strings.TrimPrefix(seg, "/")
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	if seg != "" {
		if _, ok := Catalog[seg]; ok {
			return seg, true
		}
	}
	return "", false
}

// URLPrefix is the request-path prefix for a platform key.
func URLPrefix(key string) string {
	return "/" + strings.ReplaceAll(key, "-", "/") + "/"
}

// PlatformPath is the canonical prefix path ("/" + key with '-' → '/'),
// used to detect bare-prefix requests such as "/pypi/files".
func PlatformPath(key string) string {
	return strings.TrimSuffix(URLPrefix(key), "/")
}

// TransformPath converts a routed request path into the upstream path:
// strips the platform prefix and applies platform-specific rewrites.
// Port of transformPath + PLATFORM_PATH_TRANSFORMERS (core subset).
func TransformPath(effectivePath, key string) string {
	base, ok := Catalog[key]
	_ = base
	if !ok {
		return effectivePath
	}

	prefix := URLPrefix(key)
	stripped := "/"
	if len(effectivePath) > len(prefix) {
		stripped = "/" + effectivePath[len(prefix):]
	} else if len(effectivePath) >= len(prefix) {
		stripped = "/"
	}

	if key == "crates" {
		return transformCratesPath(stripped)
	}
	return stripped
}

// transformCratesPath prepends the crates.io API root, port of
// transformCratesPath in the original project.
func transformCratesPath(p string) string {
	if p == "/" || strings.HasPrefix(p, "/?") {
		return "/api/v1/crates" + strings.TrimPrefix(p, "/")
	}
	return "/api/v1/crates" + p
}

// IsContainer reports whether the platform is a container registry.
func IsContainer(key string) bool { return strings.HasPrefix(key, "cr-") }
