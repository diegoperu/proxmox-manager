package main

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"proxmox-manager/internal/config"
)

// Inline scripts, event handlers and styles are part of the current single page.
// Keep unsafe-inline until those can be moved out; never permit unsafe-eval.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(proxies []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
			if requestHTTPS(r, proxies) {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			}
			// Do not wrap ResponseWriter: console WebSockets need http.Hijacker.
			next.ServeHTTP(w, r)
		})
	}
}

func requestPeer(r *http.Request) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	return a.Unmap(), err
}

func requestHTTPS(r *http.Request, proxies []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	// Trust only the direct peer, like auth.ClientIP. Reject ambiguous lists or
	// repeated protocol headers rather than accepting a client-supplied value.
	values := r.Header.Values("X-Forwarded-Proto")
	if len(values) != 1 || values[0] != "https" {
		return false
	}
	peer, err := requestPeer(r)
	if err != nil {
		return false
	}
	for _, p := range proxies {
		if p.Contains(peer) {
			return true
		}
	}
	return false
}

// localVersionProbe permits the installer's health check without creating or
// exposing an account/session. Caddy always sends forwarding headers, so a
// proxied request keeps the normal reader rule even though its peer is loopback.
func localVersionProbe(r *http.Request) bool {
	peer, err := requestPeer(r)
	if err != nil || !peer.IsLoopback() {
		return false
	}
	for name := range r.Header {
		name = strings.ToLower(name)
		if name == "forwarded" || strings.HasPrefix(name, "x-forwarded-") {
			return false
		}
	}
	return true
}

// startupWarnings only diagnoses exposure; local development keeps working.
func startupWarnings(cfg config.Config) []string {
	var warnings []string
	host, _, err := net.SplitHostPort(cfg.ListenAddr)
	ip, ipErr := netip.ParseAddr(host)
	loopback := err == nil && (strings.EqualFold(host, "localhost") || (ipErr == nil && ip.IsLoopback()))
	if !loopback && len(cfg.TrustedProxies) == 0 {
		warnings = append(warnings, "WARNING: listen_addr is not loopback and trusted_proxies is empty: service is reachable without the proxy")
	}
	if len(cfg.AllowedHosts) == 0 {
		warnings = append(warnings, "WARNING: allowed_hosts is empty: DNS rebinding protection is off")
	}
	return warnings
}
