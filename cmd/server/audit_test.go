package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"proxmox-manager/internal/auth"
)

func TestAuditHTTPFilters(t *testing.T) {
	store, _ := newAuthStore(t)
	if err := store.AddAudit(auth.AuditEntry{User: "anna", Action: "quote '%_", Status: 403}); err != nil {
		t.Fatal(err)
	}
	u := usersAPI{store: store}
	for _, q := range []string{"limit=bad", "limit=0", "limit=501", "before=-1", "before=abc", "before=9223372036854775808", "user=../bad", "q=" + url.QueryEscape(strings.Repeat("a", 65)), "q=line%0Abreak", "since=2026-02-30", "until=2026-1-01", "since=2026-11-23&until=2026-11-22", "outcome=unknown"} {
		w := httptest.NewRecorder()
		u.audit(w, httptest.NewRequest("GET", "/api/audit?"+q, nil))
		if w.Code != 400 {
			t.Errorf("accepted %q: %d %s", q, w.Code, w.Body)
		}
	}
	for _, q := range []string{"", "limit=10&before=0&user=anna&outcome=error&q=" + url.QueryEscape("'%_")} {
		w := httptest.NewRecorder()
		u.audit(w, httptest.NewRequest("GET", "/api/audit?"+q, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "anna") {
			t.Errorf("%q: %d %s", q, w.Code, w.Body)
		}
	}
}
