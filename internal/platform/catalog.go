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
// Full mirror of the original PLATFORM_CATALOG: code hosting, AI model
// hubs, package registries, Linux distributions, AI inference providers
// and container registries. Unknown keys are unknown — the handler
// redirects to the homepage, exactly like the original.
var Catalog = map[string]string{
	// Code Repositories & Version Control
	"gh":       "https://github.com",
	"gist":     "https://gist.github.com",
	"gl":       "https://gitlab.com",
	"gitea":    "https://gitea.com",
	"codeberg": "https://codeberg.org",
	"sf":       "https://sourceforge.net",
	"aosp":     "https://android.googlesource.com",
	"hf":       "https://huggingface.co",
	"civitai":  "https://civitai.com",

	// Package Managers
	"npm":              "https://registry.npmjs.org",
	"pypi":             "https://pypi.org",
	"pypi-files":       "https://files.pythonhosted.org",
	"conda":            "https://repo.anaconda.com",
	"conda-community":  "https://conda.anaconda.org",
	"maven":            "https://repo1.maven.org",
	"apache":           "https://downloads.apache.org",
	"gradle":           "https://plugins.gradle.org",
	"homebrew":         "https://github.com/Homebrew",
	"homebrew-api":     "https://formulae.brew.sh/api",
	"homebrew-bottles": "https://ghcr.io",
	"rubygems":         "https://rubygems.org",
	"cran":             "https://cran.r-project.org",
	"cpan":             "https://www.cpan.org",
	"ctan":             "https://tug.ctan.org",
	"golang":           "https://proxy.golang.org",
	"nuget":            "https://api.nuget.org",
	"crates":           "https://crates.io",
	"packagist":        "https://repo.packagist.org",
	"flathub":          "https://dl.flathub.org",

	// Linux Distributions
	"debian":   "https://deb.debian.org",
	"ubuntu":   "https://archive.ubuntu.com",
	"fedora":   "https://mirrors.kernel.org/fedora",
	"rocky":    "https://download.rockylinux.org",
	"opensuse": "https://download.opensuse.org",
	"arch":     "https://geo.mirror.pkgbuild.com",

	// Other Resources
	"arxiv":   "https://arxiv.org",
	"fdroid":  "https://f-droid.org",
	"jenkins": "https://updates.jenkins.io",

	// AI Inference Providers
	"ip-openai":        "https://api.openai.com",
	"ip-anthropic":     "https://api.anthropic.com",
	"ip-gemini":        "https://generativelanguage.googleapis.com",
	"ip-vertexai":      "https://aiplatform.googleapis.com",
	"ip-cohere":        "https://api.cohere.ai",
	"ip-mistralai":     "https://api.mistral.ai",
	"ip-xai":           "https://api.x.ai",
	"ip-githubmodels":  "https://models.github.ai",
	"ip-nvidiaapi":     "https://integrate.api.nvidia.com",
	"ip-perplexity":    "https://api.perplexity.ai",
	"ip-braintrust":    "https://api.braintrust.dev",
	"ip-groq":          "https://api.groq.com",
	"ip-cerebras":      "https://api.cerebras.ai",
	"ip-sambanova":     "https://api.sambanova.ai",
	"ip-siray":         "https://api.siray.ai",
	"ip-huggingface":   "https://router.huggingface.co",
	"ip-together":      "https://api.together.xyz",
	"ip-replicate":     "https://api.replicate.com",
	"ip-fireworks":     "https://api.fireworks.ai",
	"ip-nebius":        "https://api.studio.nebius.ai",
	"ip-jina":          "https://api.jina.ai",
	"ip-voyageai":      "https://api.voyageai.com",
	"ip-falai":         "https://fal.run",
	"ip-novita":        "https://api.novita.ai",
	"ip-burncloud":     "https://ai.burncloud.com",
	"ip-openrouter":    "https://openrouter.ai",
	"ip-poe":           "https://api.poe.com",
	"ip-featherlessai": "https://api.featherless.ai",
	"ip-hyperbolic":    "https://api.hyperbolic.xyz",

	// Container Registries
	"cr-docker":       "https://registry-1.docker.io",
	"cr-quay":         "https://quay.io",
	"cr-gcr":          "https://gcr.io",
	"cr-mcr":          "https://mcr.microsoft.com",
	"cr-ecr":          "https://public.ecr.aws",
	"cr-ghcr":         "https://ghcr.io",
	"cr-gitlab":       "https://registry.gitlab.com",
	"cr-redhat":       "https://registry.redhat.io",
	"cr-oracle":       "https://container-registry.oracle.com",
	"cr-cloudsmith":   "https://docker.cloudsmith.io",
	"cr-digitalocean": "https://registry.digitalocean.com",
	"cr-vmware":       "https://projects.registry.vmware.com",
	"cr-k8s":          "https://registry.k8s.io",
	"cr-heroku":       "https://registry.heroku.com",
	"cr-suse":         "https://registry.suse.com",
	"cr-opensuse":     "https://registry.opensuse.org",
	"cr-gitpod":       "https://registry.gitpod.io",
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
// Port of transformPath + PLATFORM_PATH_TRANSFORMERS (complete set).
func TransformPath(effectivePath, key string) string {
	if _, ok := Catalog[key]; !ok {
		return effectivePath
	}

	prefix := URLPrefix(key)
	stripped := "/"
	if len(effectivePath) > len(prefix) {
		stripped = "/" + effectivePath[len(prefix):]
	} else if len(effectivePath) >= len(prefix) {
		stripped = "/"
	}

	switch key {
	case "crates":
		return transformCratesPath(stripped)
	case "fedora":
		return transformFedoraPath(stripped)
	case "jenkins":
		return transformJenkinsPath(stripped)
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

// transformFedoraPath strips the redundant /pub/fedora/linux prefix, port
// of transformFedoraPath: the upstream base already ends in /fedora.
func transformFedoraPath(p string) string {
	const marker = "/pub/fedora/linux"
	if p == marker {
		return "/"
	}
	if strings.HasPrefix(p, marker+"/") {
		return strings.TrimPrefix(p, marker)
	}
	return p
}

// transformJenkinsPath pins update-center requests to the /current channel,
// port of transformJenkinsPath: /experimental, /download and /current pass
// through, /update-center(.actual).json maps to /current/..., everything
// else is rooted under /current.
func transformJenkinsPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return p
	}
	if p == "/update-center.json" {
		return "/current/update-center.json"
	}
	if p == "/update-center.actual.json" {
		return "/current/update-center.actual.json"
	}
	if strings.HasPrefix(p, "/experimental/") ||
		strings.HasPrefix(p, "/download/") ||
		strings.HasPrefix(p, "/current/") {
		return p
	}
	return "/current" + p
}

// IsContainer reports whether the platform is a container registry.
func IsContainer(key string) bool { return strings.HasPrefix(key, "cr-") }

// IsAIProvider reports whether the platform is an AI inference provider.
func IsAIProvider(key string) bool { return strings.HasPrefix(key, "ip-") }
