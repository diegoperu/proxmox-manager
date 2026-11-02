package main

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"proxmox-manager/internal/config"
)

// apiGuard protects /api from requests started by other web pages (CSRF) and from DNS rebinding.
// The app has no login, so what keeps a web page from driving it is that browsers tell us where a
// request comes from:
//   - Sec-Fetch-Site, when sent, must be same-origin (or none: typed URL / bookmark). Other
//     ports on the same host count as "same-site", not same-origin, and are refused.
//   - An Origin header, when sent (any method, WebSocket handshakes included), must be this very
//     host. Origin is not sent on same-origin GETs, so the SPA is unaffected.
//   - State-changing methods must carry Content-Type: application/json. A cross-site page cannot
//     send that without a CORS preflight, which only the origins in the CORS list may pass.
//   - Optionally (config "allowed_hosts"), the Host header must be in the list, which stops DNS
//     rebinding: a rebound page arrives with the attacker's host name in Host.
func apiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			deny(w, http.StatusForbidden, "richiesta da un'altra origine non ammessa")
			return
		}
		if hosts := config.AllowedHosts(); len(hosts) > 0 && !hostAllowed(r.Host, hosts) {
			deny(w, http.StatusForbidden, "host non ammesso")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originMatches(o, r.Host, r.Header.Get("X-Forwarded-Host")) {
			deny(w, http.StatusForbidden, "origine non ammessa")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				deny(w, http.StatusUnsupportedMediaType, "Content-Type deve essere application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}

// originMatches: the Origin's host[:port] must equal the Host the request was sent to (or the
// host a reverse proxy reports in X-Forwarded-Host: a page cannot set that header cross-site).
func originMatches(origin, host, forwardedHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // "null" and anything malformed
	}
	return strings.EqualFold(u.Host, host) || (forwardedHost != "" && strings.EqualFold(u.Host, forwardedHost))
}

// hostAllowed compares the host name (without port) with the allowlist, case-insensitively.
func hostAllowed(hostport string, allowed []string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(strings.ToLower(h), "[]")
	for _, a := range allowed {
		if strings.Trim(strings.ToLower(strings.TrimSpace(a)), "[]") == h {
			return true
		}
	}
	return false
}
