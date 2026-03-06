package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	shouldFilterEntities := !cfg.IncludeAllEntities
	var entityIDs []string

	if shouldFilterEntities {
		// Bootstrap: fetch Lovelace config and extract entity IDs
		log.Printf("fetching lovelace config from %s ...", cfg.HomeAssistantURL)
		lovelaceConfig, err := fetchLovelaceConfig(cfg.HomeAssistantURL, cfg.AccessToken, cfg.DashboardURLPath)
		if err != nil {
			log.Fatalf("failed to fetch lovelace config: %v", err)
		}

		// Check for strategy dashboard
		if _, hasStrategy := lovelaceConfig["strategy"]; hasStrategy {
			log.Fatalf("dashboard uses a strategy config -- entity IDs cannot be extracted statically. " +
				"Use extra_entities in config to specify entities manually, or use a non-strategy dashboard.")
		}

		entityIDs = extractEntities(lovelaceConfig)

		// Merge extra entities from config
		seen := make(map[string]struct{}, len(entityIDs))
		for _, id := range entityIDs {
			seen[id] = struct{}{}
		}
		for _, id := range cfg.ExtraEntities {
			if _, exists := seen[id]; !exists {
				entityIDs = append(entityIDs, id)
				seen[id] = struct{}{}
			}
		}

		beforeGlobFilter := len(entityIDs)
		entityIDs = filterEntityIDsByGlob(entityIDs, cfg.IncludeEntityGlobs, cfg.ExcludeEntityGlobs)
		if len(cfg.IncludeEntityGlobs) > 0 || len(cfg.ExcludeEntityGlobs) > 0 {
			log.Printf("applied entity glob filters: include=%d exclude=%d (%d -> %d entities)",
				len(cfg.IncludeEntityGlobs), len(cfg.ExcludeEntityGlobs), beforeGlobFilter, len(entityIDs))
		}

		if len(entityIDs) == 0 {
			log.Fatalf("no entity IDs left after applying dashboard extraction, extra_entities, and glob filters")
		}

		sort.Strings(entityIDs)
		log.Printf("will filter subscribe_entities to %d entities:", len(entityIDs))
		for _, id := range entityIDs {
			log.Printf("  %s", id)
		}
	} else {
		log.Printf("include_all_entities enabled: disabling entity subscription/event filtering")
	}

	// Set up the reverse proxy target
	targetURL, err := url.Parse(cfg.HomeAssistantURL)
	if err != nil {
		log.Fatalf("invalid homeassistant_url: %v", err)
	}

	// WebSocket URL for upstream connections
	haWSURL, err := httpToWS(cfg.HomeAssistantURL)
	if err != nil {
		log.Fatalf("failed to build websocket URL: %v", err)
	}

	// HTTP reverse proxy
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := reverseProxy.Director
	reverseProxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = targetURL.Host
	}
	if cfg.Transparent {
		// Transparent mode: strip X-Forwarded-* headers so HA treats the
		// request as a direct client connection (no trusted_proxies needed).
		log.Printf("transparent mode enabled — stripping proxy headers")
		reverseProxy.Transport = &transparentTransport{base: http.DefaultTransport}
	}
	reverseProxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode >= 400 {
			log.Printf("upstream returned %d for %s %s", resp.StatusCode, resp.Request.Method, resp.Request.URL)
		}
		return nil
	}
	reverseProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("reverse proxy error for %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Single handler: intercept WebSocket upgrades on any path,
	// reverse proxy everything else.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("request: %s %s (ws_upgrade=%v)", r.Method, r.URL.Path, isWebSocketUpgrade(r))
		if isWebSocketUpgrade(r) {
			// Proxy the WebSocket, injecting entity filter on /api/websocket
			filterEntities := shouldFilterEntities && r.URL.Path == "/api/websocket"
			stateUpdateEvery := cfg.StateUpdateEvery
			if r.URL.Path != "/api/websocket" {
				stateUpdateEvery = 0
			}
			wsProxyWithInterval(haWSURL, entityIDs, filterEntities, stateUpdateEvery, w, r)
			return
		}
		reverseProxy.ServeHTTP(w, r)
	})

	log.Printf("starting proxy on %s → %s", cfg.ListenAddr, cfg.HomeAssistantURL)
	fmt.Printf("\nPoint your tablet browser to http://<this-host>%s\n\n", cfg.ListenAddr)

	if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// transparentTransport strips proxy-identifying headers before sending
// the request upstream, so HA sees a clean request as if from a direct client.
type transparentTransport struct {
	base http.RoundTripper
}

func (t *transparentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Del("X-Forwarded-Server")
	return t.base.RoundTrip(req)
}

func isWebSocketUpgrade(r *http.Request) bool {
	// Check Upgrade header
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	// Check Connection header (may be comma-separated: "keep-alive, Upgrade")
	for _, v := range r.Header["Connection"] {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}
