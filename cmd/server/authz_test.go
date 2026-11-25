package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxmox-manager/internal/auth"
	"proxmox-manager/internal/handlers"

	"github.com/go-chi/chi/v5"
)

// Every registered route has a rule, and every rule names a registered route (no stale entries).
func TestEveryRouteHasAnAccessRule(t *testing.T) {
	store, _ := newAuthStore(t)
	r := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)
	keys, err := routeKeys(r)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
		if _, ok := routeAccess[k]; !ok {
			t.Errorf("route without an access rule: %s", k)
		}
	}
	for k := range routeAccess {
		if !seen[k] {
			t.Errorf("access rule for a route that does not exist: %s", k)
		}
	}
}

// The roles of D136 on representative routes.
func TestRoleTableMatchesD136(t *testing.T) {
	want := map[string]access{
		"GET /api/vms": reader, "POST /api/nodes/{node}/qemu/{vmid}/action/{action}": operator,
		"GET /api/nodes/{node}/qemu/{vmid}/termproxy-ws": operator, "POST /api/batch": operator,
		"POST /api/provision/job": operator, "DELETE /api/nodes/{node}/qemu/{vmid}": adminTOTP,
		"POST /api/nodes/{node}/cmd/{cmd}": adminTOTP, "POST /api/balance/step": admin,
		"POST /api/nodes/{node}/qemu/{vmid}/gpu": admin, "POST /api/clusters": adminTOTP,
		"POST /api/users": adminTOTP, "GET /api/audit": admin, "POST /api/config": admin,
	}
	for k, v := range want {
		if routeAccess[k] != v {
			t.Errorf("%s: %+v, want %+v", k, routeAccess[k], v)
		}
	}
	for k, v := range routeAccess {
		if strings.HasPrefix(k, "DELETE ") && v.role != auth.RoleAdmin {
			t.Errorf("deletion not reserved to administrators: %s", k)
		}
	}
}

func newAuthStore(t *testing.T) (*auth.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := auth.Open(filepath.Join(dir, "a.db"), filepath.Join(dir, "a.key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

// totpNow computes the current RFC 6238 code (independent of the auth package).
func totpAt(secretB32 string, step int64) string {
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		panic(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1000000)
}

type actor struct {
	cookie *http.Cookie
	csrf   string
	secret string
	step   int64 // next TOTP step to use (each step is single use)
}

// code returns the next unused TOTP code that the server still accepts now: steps are single use and only
// +-1 step around the current time is valid, so a 30-second boundary crossed during the test must not leave
// the next code behind the window (that made the test fail now and then).
func (a *actor) code() string {
	// Never compute a code in the last 2 s of a 30 s window: the server could check it in the next window and,
	// with the code one step behind, refuse it (intermittent failure seen about once in 25 runs).
	for time.Now().Unix()%30 >= 28 {
		time.Sleep(100 * time.Millisecond)
	}
	if cur := time.Now().Unix() / 30; a.step < cur-1 {
		a.step = cur - 1
	}
	c := totpAt(a.secret, a.step)
	a.step++
	return c
}

func login(t *testing.T, s *auth.Store, name string, role auth.Role) *actor {
	t.Helper()
	const pw = "password di prova lunga"
	secret, err := s.CreateUser(name, role, pw)
	if err != nil {
		t.Fatal(err)
	}
	a := &actor{secret: secret, step: time.Now().Unix()/30 - 1}
	tok, sess, err := s.Login(name, pw, a.code(), "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	a.cookie = &http.Cookie{Name: auth.CookieName, Value: tok}
	a.csrf = sess.CSRF
	return a
}

type reply struct {
	code int
	body map[string]any
}

func call(r http.Handler, method, path string, a *actor, csrf bool, totp string, body string) reply {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if body != "" || method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	if a != nil {
		req.AddCookie(a.cookie)
		if csrf {
			req.Header.Set(auth.CSRFHeader, a.csrf)
		}
	}
	if totp != "" {
		req.Header.Set(StepUpHeader, totp)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return reply{w.Code, m}
}

// denied reports whether the authorization layer (not the handler) refused the request.
func denied(rp reply) bool {
	if rp.code != http.StatusUnauthorized && rp.code != http.StatusForbidden {
		return false
	}
	msg, _ := rp.body["error"].(string)
	for _, s := range []string{"configurazione iniziale", "accesso richiesto", "sessione scaduta", "ruolo non permette", "token CSRF", "codice TOTP", "accesso negato"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func TestAuthorizationFlow(t *testing.T) {
	store, _ := newAuthStore(t)
	router := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)

	// First start: no account, every protected route answers "setup"; pages and setup stay reachable.
	rp := call(router, "GET", "/api/vms", nil, false, "", "")
	if rp.code != http.StatusUnauthorized || rp.body["setup"] != true {
		t.Fatalf("setup mode: %+v", rp)
	}
	if rp := call(router, "GET", "/", nil, false, "", ""); rp.code != http.StatusOK {
		t.Fatalf("page in setup mode: %d", rp.code)
	}
	if rp := call(router, "GET", "/api/auth/setup", nil, false, "", ""); rp.code != http.StatusOK || rp.body["open"] != true {
		t.Fatalf("setup status: %+v", rp)
	}

	adm := login(t, store, "capo", auth.RoleAdmin)
	op := login(t, store, "operaio", auth.RoleOperator)
	rd := login(t, store, "lettore", auth.RoleReader)

	// No session.
	if rp := call(router, "GET", "/api/vms", nil, false, "", ""); rp.code != http.StatusUnauthorized || rp.body["login"] != true {
		t.Fatalf("anonymous: %+v", rp)
	}
	// Reader reads but cannot act.
	if rp := call(router, "GET", "/api/vms", rd, false, "", ""); denied(rp) {
		t.Fatalf("reader read refused: %+v", rp)
	}
	act := "/api/nodes/n1/qemu/101/action/stop"
	if rp := call(router, "POST", act, rd, true, "", "{}"); rp.code != http.StatusForbidden {
		t.Fatalf("reader action: %+v", rp)
	}
	if rp := call(router, "GET", "/api/nodes/n1/qemu/101/termproxy-ws", rd, false, "", ""); rp.code != http.StatusForbidden {
		t.Fatalf("reader console: %+v", rp)
	}
	// Operator: CSRF token required on writes.
	if rp := call(router, "POST", act, op, false, "", "{}"); rp.code != http.StatusForbidden || !strings.Contains(fmt.Sprint(rp.body["error"]), "CSRF") {
		t.Fatalf("operator without CSRF: %+v", rp)
	}
	if rp := call(router, "POST", act, op, true, "", "{}"); denied(rp) {
		t.Fatalf("operator action refused: %+v", rp)
	}
	// Operator cannot delete; administrator needs the TOTP code.
	del := "/api/nodes/n1/qemu/101"
	if rp := call(router, "DELETE", del, op, true, op.code(), ""); rp.code != http.StatusForbidden {
		t.Fatalf("operator delete: %+v", rp)
	}
	if rp := call(router, "DELETE", del, adm, true, "", ""); rp.code != http.StatusForbidden || rp.body["reason"] != "step_up" {
		t.Fatalf("admin delete without code: %+v", rp)
	}
	if rp := call(router, "DELETE", del, adm, true, "000000", ""); rp.body["reason"] != "step_up" {
		t.Fatalf("admin delete with a wrong code: %+v", rp)
	}
	if rp := call(router, "DELETE", del, adm, true, adm.code(), ""); denied(rp) {
		t.Fatalf("admin delete with code refused: %+v", rp)
	}
	// Users API. TOTP steps are single use and only +-1 step is accepted: each administrator below uses at
	// most three codes (login included).
	if rp := call(router, "GET", "/api/users", op, false, "", ""); rp.code != http.StatusForbidden {
		t.Fatalf("operator lists users: %+v", rp)
	}
	if rp := call(router, "PUT", "/api/users/capo", adm, true, adm.code(), `{"disabled":true}`); rp.code != http.StatusConflict {
		t.Fatalf("self-disable: %+v", rp)
	}
	adm2 := login(t, store, "vice", auth.RoleAdmin)
	rp = call(router, "POST", "/api/users", adm2, true, adm2.code(), `{"username":"nuovo","role":"reader"}`)
	if rp.code != http.StatusOK || rp.body["password"] == nil || rp.body["totp_secret"] == nil {
		t.Fatalf("create user: %+v", rp)
	}
	if rp := call(router, "PUT", "/api/users/operaio", adm2, true, adm2.code(), `{"role":"reader"}`); rp.code != http.StatusOK {
		t.Fatalf("change role: %+v", rp)
	}
	// The demotion applies to the operator's existing session.
	if rp := call(router, "POST", act, op, true, "", "{}"); rp.code != http.StatusForbidden {
		t.Fatalf("demoted operator still acts: %+v", rp)
	}
	// Every write is in the action log, with user, route and target, never the generated password.
	rows, err := store.ListAudit(auth.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var sawStop, sawCreate bool
	for _, e := range rows {
		if e.User == "operaio" && e.Action == "POST /api/nodes/{node}/qemu/{vmid}/action/{action}" && strings.Contains(e.Target, "vmid=101") && strings.Contains(e.Target, "action=stop") {
			sawStop = true
		}
		if e.User == "vice" && e.Action == "POST /api/users" {
			sawCreate = true
			if strings.Contains(e.Detail+e.Target, fmt.Sprint(rp.body["password"])) {
				t.Fatal("password in the action log")
			}
		}
	}
	if !sawStop || !sawCreate {
		t.Fatalf("action log incomplete: %+v", rows)
	}
}

func TestUnknownRuleIsRefusedAndUPIDIsLogged(t *testing.T) {
	store, _ := newAuthStore(t)
	router := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)
	router.Get("/api/zz-no-rule", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) })
	router.Post("/api/zz-task", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"UPID:n1:0001:0002:6B00193D:qmstop:101:pmgr@pve!manager:"}`))
	})
	routeAccess["POST /api/zz-task"] = operator
	defer delete(routeAccess, "POST /api/zz-task")
	adm := login(t, store, "capo", auth.RoleAdmin)
	if rp := call(router, "GET", "/api/zz-no-rule", adm, false, "", ""); rp.code != http.StatusForbidden {
		t.Fatalf("route without rule: %+v", rp)
	}
	if rp := call(router, "POST", "/api/zz-task", adm, true, "", "{}"); rp.code != http.StatusOK {
		t.Fatalf("task route: %+v", rp)
	}
	rows, _ := store.ListAudit(auth.AuditFilter{Limit: 10, User: "capo"})
	if len(rows) == 0 || rows[0].UPID != "UPID:n1:0001:0002:6B00193D:qmstop:101:pmgr@pve!manager:" {
		t.Fatalf("UPID not logged: %+v", rows)
	}
	m, err := store.AuditByUPID([]string{rows[0].UPID, "UPID:other"})
	if err != nil || m[rows[0].UPID] != "capo" || len(m) != 1 {
		t.Fatalf("by UPID: %v %v", m, err)
	}
	_ = chi.URLParam
}

// Regression (step 7 review): an encoded slash in a route parameter made the authorization layer look up a
// different path than the router and skip every check.
func TestEncodedPathsCannotSkipAuthorization(t *testing.T) {
	store, _ := newAuthStore(t)
	login(t, store, "capo", auth.RoleAdmin)
	router := buildRouter(handlers.New(), &auth.Handlers{Store: store}, store, nil)
	for _, c := range []struct {
		method, raw string
		want        int
	}{
		{"GET", "/api/nodes/a%2Fb/status", http.StatusBadRequest},
		{"GET", "/api/nodes/x%2F..%2F..%2Fauth%2Flogin/status", http.StatusBadRequest},
		{"GET", "/api/nodes/a%2fb/status", http.StatusBadRequest},
		{"GET", "/api/nodes/a%5Cb/status", http.StatusBadRequest},
		{"DELETE", "/api/nodes/a%2Fb/qemu/101", http.StatusBadRequest},
		{"GET", "/api/vms", http.StatusUnauthorized},
		{"GET", "/api/nodes/n%31/status", http.StatusUnauthorized}, // other encodings: still checked
		{"GET", "/api/does-not-exist", http.StatusNotFound},
	} {
		req := httptest.NewRequest(c.method, c.raw, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.raw, w.Code, w.Body, c.want)
		}
	}
}
