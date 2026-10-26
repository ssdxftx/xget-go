// Xget-Go — a single-binary port of xixu-me/Xget.
//
// An ultra-high-performance acceleration engine for developer resources:
// code hosting, AI model hubs, package registries and container registries.
// This standard edition covers URL prefix acceleration, Range/partial
// content, retry with linear backoff, HEAD content-length probing, Docker
// anonymous-pull token retry, Git/LFS/Docker header passthrough, request
// validation, security headers and optional instance token auth.
//
// Original project: https://github.com/xixu-me/Xget (AGPL-3.0-or-later)
// This port inherits AGPL-3.0-or-later.
package main

import (
        "context"
        "fmt"
        "log"
        "net/http"
        "os"
        "os/signal"
        "strings"
        "syscall"
        "time"

        "xget/internal/config"
        "xget/internal/platform"
        "xget/internal/proxy"
)

var version = "dev"

func main() {
        if len(os.Args) > 1 {
                switch os.Args[1] {
                case "-v", "--version", "version":
                        fmt.Printf("xget-go %s (port of xixu-me/Xget, AGPL-3.0-or-later)\n", version)
                        return
                case "-h", "--help", "help":
                        printUsage()
                        return
                }
        }

        cfg := config.FromEnv()

        log.SetFlags(log.LstdFlags | log.LUTC)
        log.Printf("xget-go %s starting — port=%s auth=%s retries=%d timeout=%ds platforms=%d cache=%s",
                version, cfg.Port, authLabel(cfg), cfg.MaxRetries, cfg.TimeoutSecond,
                len(platform.Catalog), cacheLabel(cfg))
        if cfg.AccessToken == "" {
                log.Printf("WARNING: ACCESS_TOKEN is empty — the instance is open to anyone who can reach it. " +
                        "The official Xget README strongly recommends putting instances behind authentication or IP allowlists.")
        }

        h := proxy.New(cfg)

        // Mount the proxy handler directly (no http.ServeMux): ServeMux silently
        // cleans ".." segments with a 301 before handlers run, which would defeat
        // the traversal rejection contract of the original project (400).
        srv := &http.Server{
                Addr:              ":" + cfg.Port,
                Handler:           h,
                ReadHeaderTimeout: 15 * time.Second,
                IdleTimeout:       120 * time.Second,
                MaxHeaderBytes:    1 << 20,
        }

        done := make(chan struct{})
        go func() {
                sig := make(chan os.Signal, 1)
                signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
                <-sig
                log.Printf("shutting down gracefully...")
                ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
                defer cancel()
                _ = srv.Shutdown(ctx)
                close(done)
        }()

        log.Printf("listening on http://0.0.0.0:%s — usage: http://<host>:%s/<prefix>/<path>", cfg.Port, cfg.Port)
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                log.Fatalf("server error: %v", err)
        }
        <-done
        log.Printf("bye")
}

func authLabel(cfg *config.Config) string {
        if strings.TrimSpace(cfg.AccessToken) == "" {
                return "disabled"
        }
        return "token"
}

func cacheLabel(cfg *config.Config) string {
        if cfg.CacheDir == "" || strings.EqualFold(cfg.CacheDir, "off") || strings.EqualFold(cfg.CacheDir, "none") {
                return "disabled"
        }
        return fmt.Sprintf("%s (max %dMB/file %dMB)", cfg.CacheDir, cfg.CacheMaxMB, cfg.CacheMaxFileMB)
}

func printUsage() {
        fmt.Printf(`xget-go %s — single-binary port of xixu-me/Xget (AGPL-3.0-or-later)

Usage:
  xget-go                 start the server (all configuration via env vars)
  xget-go version|-v      print version

Environment variables:
  PORT              listen port                          (default 8080)
  ACCESS_TOKEN      instance token; empty disables auth  (default empty)
  TIMEOUT_SECONDS   per-attempt time-to-headers          (default 30)
  MAX_RETRIES       upstream retry attempts              (default 3)
  RETRY_DELAY_MS    linear backoff base                  (default 1000)
  MAX_PATH_LENGTH   URL path length cap                  (default 2048)
  CACHE_TTL         mutable cache TTL, seconds           (default 300)
  CACHE_DIR         disk cache dir; off/none disables    (default cache)
  CACHE_MAX_MB      total cache budget, LRU eviction     (default 1024)
  CACHE_MAX_FILE_MB per-entry cache cap                  (default 512)
  ALLOWED_METHODS   methods for non-protocol requests    (default GET,HEAD)
  ALLOWED_ORIGINS   CORS origins, comma-separated        (default *)
  HOMEPAGE_URL      redirect target for /                (default upstream repo)

Auth: send X-Xget-Token: <token> (keeps Authorization free for upstream) or
Authorization: Bearer <token> (consumed by the proxy) or ?xget_token=<token>.

Platforms:`, version)
        for k := range platform.Catalog {
                fmt.Printf(" %s", k)
        }
        fmt.Println()
}
