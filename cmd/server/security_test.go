package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxmox-manager/internal/auth"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/handlers"
)

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	store, _ := newAuthStore(t)
	router := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)
	for _, target := range []string{"/", "/static/xterm-5.3.0.js", "/missing", "/static/missing.js", "/api/vms"} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			for key, want := range map[string]string{
				"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
				"Referrer-Policy": "no-referrer", "Content-Security-Policy": contentSecurityPolicy,
			} {
				if got := rec.Header().Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
				t.Errorf("HSTS over HTTP: %q", got)
			}
		})
	}
}

func TestSecurityHeadersHSTS(t *testing.T) {
	proxies, err := auth.ParseTrustedProxies([]string{"127.0.0.1", "::1", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, peer string
		proto      []string
		tls, want  bool
	}{
		{"direct TLS", "198.51.100.1:20", nil, true, true},
		{"HTTP", "127.0.0.1:20", nil, false, false},
		{"trusted IPv4", "127.0.0.1:20", []string{"https"}, false, true},
		{"trusted IPv6", "[::1]:20", []string{"https"}, false, true},
		{"mapped IPv4", "[::ffff:127.0.0.1]:20", []string{"https"}, false, true},
		{"trusted CIDR", "192.0.2.8:20", []string{"https"}, false, true},
		{"untrusted spoof", "198.51.100.1:20", []string{"https"}, false, false},
		{"malformed peer", "proxy.invalid:20", []string{"https"}, false, false},
		{"trusted HTTP", "127.0.0.1:20", []string{"http"}, false, false},
		{"list", "127.0.0.1:20", []string{"https, http"}, false, false},
		{"duplicate", "127.0.0.1:20", []string{"https", "https"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			for _, value := range tc.proto {
				req.Header.Add("X-Forwarded-Proto", value)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			securityHeaders(proxies)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(rec, req)
			want := ""
			if tc.want {
				want = "max-age=31536000"
			}
			if got := rec.Header().Get("Strict-Transport-Security"); got != want {
				t.Errorf("HSTS = %q, want %q", got, want)
			}
		})
	}
}

func TestStartupWarnings(t *testing.T) {
	for _, tc := range []struct {
		listen         string
		proxies, hosts []string
		proxy, dns     bool
	}{
		{":8080", nil, nil, true, true},
		{"0.0.0.0:8080", nil, []string{"pm.example"}, true, false},
		{"[::]:8080", nil, nil, true, true},
		{"127.0.0.1:8080", nil, nil, false, true},
		{"[::1]:8080", nil, []string{"pm.example"}, false, false},
		{"localhost:8080", nil, []string{"pm.example"}, false, false},
		{":8080", []string{"127.0.0.1"}, []string{"pm.example"}, false, false},
		{"invalid", nil, nil, true, true},
	} {
		warnings := strings.Join(startupWarnings(config.Config{ListenAddr: tc.listen, TrustedProxies: tc.proxies, AllowedHosts: tc.hosts}), "\n")
		if got := strings.Contains(warnings, "without the proxy"); got != tc.proxy {
			t.Errorf("%s: proxy warning = %v, want %v", tc.listen, got, tc.proxy)
		}
		if got := strings.Contains(warnings, "DNS rebinding"); got != tc.dns {
			t.Errorf("%s: DNS warning = %v, want %v", tc.listen, got, tc.dns)
		}
	}
}

func TestVersionProbeRequiresDirectLoopback(t *testing.T) {
	store, _ := newAuthStore(t)
	router := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)
	for _, tc := range []struct {
		name, peer, target string
		headers            map[string]string
		want               int
	}{
		{"local IPv4", "127.0.0.1:20", "/api/version", nil, 200},
		{"local IPv6", "[::1]:20", "/api/version", nil, 200},
		{"mapped IPv4", "[::ffff:127.0.0.1]:20", "/api/version", nil, 200},
		{"remote", "198.51.100.1:20", "/api/version", nil, 401},
		{"spoofed client", "198.51.100.1:20", "/api/version", map[string]string{"X-Forwarded-For": "127.0.0.1"}, 401},
		{"Caddy", "127.0.0.1:20", "/api/version", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "198.51.100.1"}, 401},
		{"empty forwarded", "127.0.0.1:20", "/api/version", map[string]string{"X-Forwarded-For": ""}, 401},
		{"forwarded host", "127.0.0.1:20", "/api/version", map[string]string{"X-Forwarded-Host": "pm.example"}, 401},
		{"standard Forwarded", "[::1]:20", "/api/version", map[string]string{"Forwarded": "for=127.0.0.1"}, 401},
		{"malformed peer", "localhost:20", "/api/version", nil, 401},
		{"other endpoint", "127.0.0.1:20", "/api/config", nil, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.RemoteAddr = tc.peer
			for key, value := range tc.headers {
				req.Header[key] = []string{value}
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
