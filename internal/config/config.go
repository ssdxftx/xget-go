// Package config loads runtime configuration from environment variables.
//
// Xget-Go - a single-binary port of xixu-me/Xget (AGPL-3.0-or-later).
package config

import (
        "os"
        "strconv"
        "strings"
)

// Config holds all runtime settings, mirroring the environment overrides of
// the original Cloudflare Workers implementation (src/config/index.js).
type Config struct {
        Port          string // PORT
        AccessToken   string // ACCESS_TOKEN (empty = auth disabled)
        HomepageURL   string // HOMEPAGE_URL (redirect target for / and unknown prefixes)
        TimeoutSecond int    // TIMEOUT_SECONDS (per attempt, until response headers)
        MaxRetries    int    // MAX_RETRIES
        RetryDelayMs  int    // RETRY_DELAY_MS (linear backoff: delay * attempt)
        MaxPathLength int    // MAX_PATH_LENGTH
        CacheTTL      int    // CACHE_TTL (mutable fallback cache TTL, seconds)

        CacheDir      string // CACHE_DIR ("off" disables the disk cache)
        CacheMaxMB    int    // CACHE_MAX_MB (total budget, LRU eviction)
        CacheMaxFileMB int   // CACHE_MAX_FILE_MB (per-entry cap)

        AllowedMethods []string // ALLOWED_METHODS (non-protocol requests)
        AllowedOrigins []string // ALLOWED_ORIGINS (CORS, "*" allows all)
}

func envDefault(key, def string) string {
        if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
                return strings.TrimSpace(v)
        }
        return def
}

func envInt(key string, def, min int) int {
        v := strings.TrimSpace(os.Getenv(key))
        if v == "" {
                return def
        }
        n, err := strconv.Atoi(v)
        if err != nil || n < min {
                return def
        }
        return n
}

func envList(key, def string) []string {
        v := envDefault(key, def)
        parts := strings.Split(v, ",")
        out := make([]string, 0, len(parts))
        for _, p := range parts {
                p = strings.TrimSpace(p)
                if p != "" {
                        out = append(out, p)
                }
        }
        if len(out) == 0 {
                out = strings.Split(def, ",")
        }
        return out
}

// FromEnv builds a Config from environment variables with safe defaults
// identical to the upstream project defaults.
func FromEnv() *Config {
        cacheDir := envDefault("CACHE_DIR", "cache")
        return &Config{
                Port:          envDefault("PORT", "8080"),
                AccessToken:   os.Getenv("ACCESS_TOKEN"),
                HomepageURL:   envDefault("HOMEPAGE_URL", "https://github.com/xixu-me/Xget"),
                TimeoutSecond: envInt("TIMEOUT_SECONDS", 30, 1),
                MaxRetries:    envInt("MAX_RETRIES", 3, 1),
                RetryDelayMs:  envInt("RETRY_DELAY_MS", 1000, 0),
                MaxPathLength: envInt("MAX_PATH_LENGTH", 2048, 1),
                CacheTTL:      envInt("CACHE_TTL", 300, 0),

                CacheDir:       cacheDir,
                CacheMaxMB:     envInt("CACHE_MAX_MB", 1024, 1),
                CacheMaxFileMB: envInt("CACHE_MAX_FILE_MB", 512, 1),

                AllowedMethods: envList("ALLOWED_METHODS", "GET,HEAD"),
                AllowedOrigins: envList("ALLOWED_ORIGINS", "*"),
        }
}
