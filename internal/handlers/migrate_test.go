package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
