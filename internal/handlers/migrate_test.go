package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/config"
)

// Invalid input never reaches Proxmox: the handler answers 400 before building a client (Handler{} has none).
func TestVMMigrateValidatesBeforeAnyCall(t *testing.T) {
	cases := []struct{ node, vmid, body string }{
		{"bad name", "174", `{"target":"n2","online":true}`},
		{"n1", "0", `{"target":"n2"}`},
		{"n1", "-3", `{"target":"n2"}`},
		{"n1", "abc", `{"target":"n2"}`},
		{"n1", "1234567890", `{"target":"n2"}`},
		{"n1", "174", `{"target":"n2;rm"}`},
		{"n1", "174", `{"target":""}`},
		{"n1", "174", `{"target":"n1"}`},
		{"n1", "174", `{"target":"../x"}`},
		{"n1", "174", `{"target":"n2","online":"yes"}`},
		{"n1", "174", `{"target":"n2","with-local-disks":1}`},
		{"n1", "174", `not json`},
		{"n1", "174", `{"target":"n2"}{"target":"n3"}`},
		{"n1", "174", `{"target":"` + strings.Repeat("a", 2000) + `"}`},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/nodes/x/qemu/y/migrate", strings.NewReader(c.body))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("node", c.node)
		rc.URLParams.Add("vmid", c.vmid)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		rec := httptest.NewRecorder()
		(&Handler{}).VMMigrate(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%+v: %d %s", c, rec.Code, rec.Body)
		}
	}
}

// D168: the manual migration is refused across CPU architectures (and from a known to an unknown one) before
// Proxmox is asked to migrate; same architecture, or both unknown as before, goes through.
func TestVMMigrateNeverAcrossArchitectures(t *testing.T) {
	var migrated int
	machines := map[string]string{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			n := strings.Split(r.URL.Path, "/")[4]
			if m := machines[n]; m != "" {
				w.Write([]byte(`{"data":{"current-kernel":{"machine":"` + m + `"}}}`))
			} else {
				w.Write([]byte(`{"data":{}}`))
			}
		case strings.HasSuffix(r.URL.Path, "/migrate") && r.Method == "POST":
			migrated++
			w.Write([]byte(`{"data":"UPID:n1:00000001:00000001:00000001:qmigrate:174:root@pam:"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldDir, _ := os.Getwd()
	old := config.Get()
	os.Chdir(t.TempDir())
	t.Cleanup(func() { config.Update(old); os.Chdir(oldDir) })
	os.WriteFile("config.json", []byte(`{"clusters":[{"label":"c","url":"`+srv.URL+`","api_token":"pmgr@pve!manager=11111111-2222-3333-4444-555555555555"}]}`), 0o600)
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	run := func() int {
		r := httptest.NewRequest("POST", "/api/nodes/n1/qemu/174/migrate", strings.NewReader(`{"target":"n2","online":true}`))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("node", "n1")
		rc.URLParams.Add("vmid", "174")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
		rec := httptest.NewRecorder()
		New().VMMigrate(rec, r)
		return rec.Code
	}
	for _, c := range []struct {
		from, to string
		code     int
		moved    bool
	}{
		{"x86_64", "aarch64", http.StatusConflict, false},
		{"aarch64", "x86_64", http.StatusConflict, false},
		{"x86_64", "", http.StatusConflict, false},
		{"aarch64", "aarch64", http.StatusOK, true},
		{"", "", http.StatusOK, true},
	} {
		machines["n1"], machines["n2"] = c.from, c.to
		before := migrated
		if code := run(); code != c.code || (migrated > before) != c.moved {
			t.Errorf("%q -> %q: code %d, migrated %v", c.from, c.to, code, migrated > before)
		}
	}
}
