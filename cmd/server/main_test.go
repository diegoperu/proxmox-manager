package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestInjectTheme(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html lang="it"><head></head><body><html lang="it"></body></html>`)
	for _, th := range []string{"dark", "light", "eva01", "catppuccino"} {
		got := string(injectTheme(page, th))
		if !strings.Contains(got, `<html lang="it" class="`+th+`">`) {
			t.Errorf("theme %q not injected: %s", th, got)
		}
		if strings.Count(got, `class="`+th+`"`) != 1 {
			t.Errorf("theme %q injected more than once", th)
		}
	}
	// anything outside the whitelist must leave the page untouched (no attribute injection)
	for _, th := range []string{"", "DARK", "dark light", `x" onload="alert(1)`, "../x", "catpuccino"} {
		if got := injectTheme(page, th); string(got) != string(page) {
			t.Errorf("theme %q must not modify the page", th)
		}
	}
	// no <html lang="it"> tag: nothing to do, no panic
	if got := injectTheme([]byte("plain"), "dark"); string(got) != "plain" {
		t.Errorf("unexpected change: %s", got)
	}
}

func TestStaticHandler(t *testing.T) {
	h := staticHandler()
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	cases := []struct {
		target, ctype string
	}{
		{"/static/chart-4.4.0.umd.min.js", "text/javascript"},
		{"/static/xterm-5.3.0.css", "text/css"},
		{"/static/fonts/jetbrains-mono-latin-var.woff2", "font/woff2"},
	}
	for _, c := range cases {
		rec := get(c.target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.target, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.ctype) {
			t.Errorf("%s: content-type %q, want %q", c.target, ct, c.ctype)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.target)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty body", c.target)
		}
	}

	notFound := []string{
		"/static/",                          // no directory listing
		"/static/fonts/",                    // no directory listing
		"/static/THIRD_PARTY.md",            // extension not in the allowlist
		"/static/missing.js",                // unknown file
		"/static/../templates/index.html",   // traversal out of web/static
		"/static/..%2ftemplates/index.html", // encoded traversal
		"/static/fonts/../../templates/index.html",
	}
	for _, target := range notFound {
		if rec := get(target); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
	}
}

// The app must not depend on external hosts: every script/stylesheet/font is local,
// and every /static/ reference points at an embedded file.
func TestIndexUsesOnlyEmbeddedAssets(t *testing.T) {
	b, err := fs.ReadFile(staticFiles, "web/templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)

	external := regexp.MustCompile(`(?i)(<script[^>]+src|<link[^>]+href|url\()\s*=?\s*["']?https?://`)
	if m := external.FindString(page); m != "" {
		t.Errorf("external asset reference found: %q", m)
	}

	refs := regexp.MustCompile(`/static/[A-Za-z0-9._/-]+\.(?:js|css|woff2)`).FindAllString(page, -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no /static/ assets")
	}
	for _, ref := range refs {
		if _, err := fs.Stat(staticFiles, "web"+ref); err != nil {
			t.Errorf("%s referenced but not embedded: %v", ref, err)
		}
	}
}
