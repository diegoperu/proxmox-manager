package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func withParams(r *http.Request, kv ...string) *http.Request {
	rctx := chi.NewRouteContext()
	for i := 0; i+1 < len(kv); i += 2 {
		rctx.URLParams.Add(kv[i], kv[i+1])
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// Actions, node commands and Batch are refused before any Proxmox call when the action is not in the
// whitelist, the target is malformed or the body carries parameters.
func TestActionGuard(t *testing.T) {
	h := New()
	cases := []struct {
		name string
		fn   http.HandlerFunc
		kv   []string
		body string
		want string
	}{
		{"vm unknown action", h.VMAction, []string{"node", "n1", "vmid", "101", "action", "migrate"}, "{}", "azione non consentita"},
		{"vm traversal node", h.VMAction, []string{"node", "..", "vmid", "101", "action", "stop"}, "{}", "nodo o VMID"},
		{"vm bad vmid", h.VMAction, []string{"node", "n1", "vmid", "abc", "action", "stop"}, "{}", "nodo o VMID"},
		{"vm extra params", h.VMAction, []string{"node", "n1", "vmid", "101", "action", "stop"}, `{"skiplock":"1"}`, "parametri non ammessi"},
		{"lxc reset", h.ContainerAction, []string{"node", "n1", "vmid", "101", "action", "reset"}, "{}", "azione non consentita"},
		{"node unknown command", h.NodeCommand, []string{"node", "n1", "cmd", "wakeonlan"}, "{}", "azione non consentita"},
		{"node params", h.NodeCommand, []string{"node", "n1", "cmd", "reboot"}, `{"command":"shutdown"}`, "parametri non ammessi"},
		{"batch params", h.BatchAction, nil, `{"vms":[{"node":"n1","vmid":101,"type":"qemu"}],"action":"stop","params":{"forceStop":"1"}}`, "parametri non ammessi"},
		{"batch action", h.BatchAction, nil, `{"vms":[{"node":"n1","vmid":101,"type":"qemu"}],"action":"delete"}`, "azione non consentita"},
		{"batch target", h.BatchAction, nil, `{"vms":[{"node":"../x","vmid":101,"type":"qemu"}],"action":"stop"}`, "nodo o VMID"},
		{"batch unknown field", h.BatchAction, nil, `{"vms":[],"action":"stop","x":1}`, "richiesta non valida"},
		{"batch empty", h.BatchAction, nil, `{"vms":[],"action":"stop"}`, "da 1 a 500"},
	}
	for _, c := range cases {
		r := withParams(httptest.NewRequest("POST", "/x", strings.NewReader(c.body)), c.kv...)
		w := httptest.NewRecorder()
		c.fn(w, r)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	// A valid request passes validation (it then fails only because no cluster is configured in the test).
	r := withParams(httptest.NewRequest("POST", "/x", strings.NewReader("{}")), "node", "n1", "vmid", "101", "action", "stop")
	w := httptest.NewRecorder()
	h.VMAction(w, r)
	if strings.Contains(w.Body.String(), "non consentita") || strings.Contains(w.Body.String(), "non ammessi") {
		t.Fatalf("valid action refused: %s", w.Body)
	}
}
