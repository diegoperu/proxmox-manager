package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuditFilters(t *testing.T) {
	s, _, _ := newTestStore(t)
	// The server's local calendar, including a DST change, defines inclusive date filters.
	old := time.Local
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	at := func(day string) int64 {
		d, err := time.ParseInLocation("2006-01-02 15:04:05", day, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return d.Unix()
	}
	entries := []AuditEntry{
		{User: "anna", Action: "percent %", Target: "node=n_1", Status: 200, Time: at("2026-10-24 23:59:59")},
		{User: "bruno", Action: "normal", Target: "node=nX1", Status: 399, Time: at("2026-10-25 00:00:00")},
		{User: "anna", Action: "quote ' OR 1=1 --", Target: `path=a\b`, Status: 400, Time: at("2026-10-25 23:59:59")},
		{User: "anna", Action: "normal", Target: "node=n1", Status: 500, Time: at("2026-10-26 00:00:00")},
	}
	for _, e := range entries {
		if err := s.AddAudit(e); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		f    AuditFilter
		ids  []int64
	}{
		{"default", AuditFilter{}, []int64{4, 3, 2, 1}},
		{"percent literal", AuditFilter{Q: "%"}, []int64{1}},
		{"underscore literal", AuditFilter{Q: "_"}, []int64{1}},
		{"quote literal", AuditFilter{Q: "' OR 1=1 --"}, []int64{3}},
		{"backslash literal", AuditFilter{Q: `a\b`}, []int64{3}},
		{"exact user", AuditFilter{User: "anna"}, []int64{4, 3, 1}},
		{"ok boundary", AuditFilter{Outcome: "ok"}, []int64{2, 1}},
		{"error boundary", AuditFilter{Outcome: "error"}, []int64{4, 3}},
		{"one local day", AuditFilter{Since: "2026-10-25", Until: "2026-10-25"}, []int64{3, 2}},
		{"since", AuditFilter{Since: "2026-10-26"}, []int64{4}},
		{"until", AuditFilter{Until: "2026-10-24"}, []int64{1}},
		{"first page", AuditFilter{Limit: 2}, []int64{4, 3}},
		{"second page", AuditFilter{Limit: 2, Before: 3}, []int64{2, 1}},
		{"combined", AuditFilter{User: "anna", Outcome: "error", Q: "quote", Since: "2026-10-25", Until: "2026-10-25", Before: 4, Limit: 1}, []int64{3}},
		{"empty", AuditFilter{Q: "missing"}, []int64{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := s.ListAudit(c.f)
			if err != nil {
				t.Fatal(err)
			}
			ids := []int64{}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, c.ids) {
				t.Fatalf("got %v want %v", ids, c.ids)
			}
		})
	}
}

func TestAuditFilterValidation(t *testing.T) {
	for _, f := range []AuditFilter{
		{Limit: -1}, {Limit: 501}, {Before: -1}, {User: "../anna"}, {Q: strings.Repeat("a", 65)},
		{Q: "line\nbreak"}, {Q: "\x7f"}, {Q: string([]byte{0xff})}, {Outcome: "success"},
		{Since: "2026-2-01"}, {Since: "2026-02-30"}, {Until: "not-a-date"},
		{Since: "2026-11-23", Until: "2026-11-22"},
		{Since: "2026-11-23", Until: "0001-01-01"}, {Since: "0000-01-01"},
	} {
		if f.Validate() == nil {
			t.Errorf("accepted invalid filter %+v", f)
		}
	}
	for _, f := range []AuditFilter{{Q: strings.Repeat("è", 64)}, {Limit: 500, Before: 1}, {Since: "2024-02-29"}} {
		if err := f.Validate(); err != nil {
			t.Errorf("rejected valid filter %+v: %v", f, err)
		}
	}
}

func TestAuditAuthenticationOutcomes(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, tc := range []struct {
		event, detail string
		status        int
	}{
		{"login", "riuscito", 200}, {"login", "fallito", 401}, {"login", "limite di tentativi per indirizzo", 429},
		{"logout", "riuscito", 200}, {"setup", "primo amministratore creato", 200}, {"setup", "codice errato", 403},
		{"password", "cambiata", 200}, {"password", "rifiutata", 403},
	} {
		if err := s.AddAudit(AuditEntry{Action: tc.event, Detail: tc.detail}); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListAudit(AuditFilter{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Status != tc.status {
			t.Errorf("%s %s: %v", tc.event, tc.detail, rows)
		}
	}
}
