package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func run(t *testing.T, method, host string, hdr map[string]string) int {
	t.Helper()
	h := apiGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(method, "http://"+host+"/api/x", nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestAPIGuardSameOriginSPAPasses(t *testing.T) {
	json := map[string]string{"Content-Type": "application/json"}
	for name, c := range map[string]struct {
		method string
		hdr    map[string]string
	}{
		"GET senza header":              {"GET", nil},
		"GET same-origin":               {"GET", map[string]string{"Sec-Fetch-Site": "same-origin"}},
		"GET digitato (none)":           {"GET", map[string]string{"Sec-Fetch-Site": "none"}},
		"POST JSON same-origin":         {"POST", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "Origin": "http://pm.lan:8080"}},
		"POST JSON con charset":         {"POST", map[string]string{"Content-Type": "application/json; charset=utf-8", "Origin": "http://pm.lan:8080"}},
		"PUT JSON":                      {"PUT", json},
		"DELETE JSON":                   {"DELETE", json},
		"dietro proxy (X-Fwd-Host)":     {"POST", map[string]string{"Content-Type": "application/json", "Origin": "https://pm.example.org", "X-Forwarded-Host": "pm.example.org"}},
		"client non browser (curl) GET": {"GET", map[string]string{"User-Agent": "curl/8"}},
	} {
		if got := run(t, c.method, "pm.lan:8080", c.hdr); got != http.StatusNoContent {
			t.Errorf("%s: status %d, atteso 204", name, got)
		}
	}
}

func TestAPIGuardBlocksCrossSite(t *testing.T) {
	json := map[string]string{"Content-Type": "application/json"}
	merge := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range json {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]struct {
		method string
		hdr    map[string]string
		want   int
	}{
		"fetch cross-site":                     {"POST", merge(map[string]string{"Sec-Fetch-Site": "cross-site"}), 403},
		"altra porta stesso host":              {"POST", merge(map[string]string{"Sec-Fetch-Site": "same-site"}), 403},
		"GET cross-site":                       {"GET", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		"Origin di un altro sito":              {"POST", merge(map[string]string{"Origin": "http://evil.example"}), 403},
		"Origin con altra porta":               {"POST", merge(map[string]string{"Origin": "http://pm.lan:9999"}), 403},
		"Origin null":                          {"POST", merge(map[string]string{"Origin": "null"}), 403},
		"Origin non valido":                    {"POST", merge(map[string]string{"Origin": "::::"}), 403},
		"Origin schema strano":                 {"POST", merge(map[string]string{"Origin": "file://pm.lan:8080"}), 403},
		"WebSocket da altra origine (GET)":     {"GET", map[string]string{"Origin": "http://evil.example", "Upgrade": "websocket"}, 403},
		"POST text/plain (richiesta semplice)": {"POST", map[string]string{"Content-Type": "text/plain"}, 415},
		"POST form":                            {"POST", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		"POST senza Content-Type":              {"POST", nil, 415},
		"DELETE senza Content-Type":            {"DELETE", nil, 415},
		"Content-Type malformato":              {"POST", map[string]string{"Content-Type": ";;;"}, 415},
		"json finto (application/jsonx)":       {"POST", map[string]string{"Content-Type": "application/jsonx"}, 415},
	}
	for name, c := range cases {
		if got := run(t, c.method, "pm.lan:8080", c.hdr); got != c.want {
			t.Errorf("%s: status %d, atteso %d", name, got, c.want)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	allowed := []string{"pm.lan", " Localhost ", "[::1]", "10.0.0.5"}
	for _, ok := range []string{"pm.lan:8080", "PM.LAN", "localhost:8080", "[::1]:8080", "10.0.0.5:8080"} {
		if !hostAllowed(ok, allowed) {
			t.Errorf("%q dovrebbe essere ammesso", ok)
		}
	}
	for _, bad := range []string{"evil.example:8080", "pm.lan.evil.example", "10.0.0.6", "", "pm.lan@evil"} {
		if hostAllowed(bad, allowed) {
			t.Errorf("%q non dovrebbe essere ammesso", bad)
		}
	}
}

func TestOriginMatches(t *testing.T) {
	if !originMatches("http://pm.lan:8080", "pm.lan:8080", "") || !originMatches("HTTP://PM.LAN:8080", "pm.lan:8080", "") {
		t.Error("stessa origine rifiutata")
	}
	if originMatches("http://pm.lan", "pm.lan:8080", "") || originMatches("http://evil.example", "pm.lan:8080", "pm.lan:8080") {
		t.Error("origine diversa accettata")
	}
}
