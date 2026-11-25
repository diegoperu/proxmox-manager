package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"proxmox-manager/internal/auth"

	"github.com/go-chi/chi/v5"
)

// access is the rule of one route: the minimum role (0 = public) and whether the TOTP code must be typed
// again (D138).
type access struct {
	role   auth.Role
	stepUp bool
}

const public auth.Role = 0

var (
	reader    = access{role: auth.RoleReader}
	operator  = access{role: auth.RoleOperator}
	admin     = access{role: auth.RoleAdmin}
	adminTOTP = access{role: auth.RoleAdmin, stepUp: true}
	open_     = access{role: public}
)

// routeAccess is THE authorization table (D136): "METHOD pattern" -> rule. A route that is registered but
// missing here is refused (fail closed) and TestEveryRouteHasAnAccessRule fails, so a new route cannot be
// added without deciding who may call it.
var routeAccess = map[string]access{
	// Pages and static files: public (the page asks /api/auth/me and shows the login form).
	"GET /": open_, "GET /nodes": open_, "GET /vms": open_, "GET /batch": open_, "GET /cluster": open_,
	"GET /provision": open_, "GET /reports": open_, "GET /settings": open_, "GET /console": open_,
	"GET /static/*": open_,

	// Accounts: login and first-start setup are public by nature; me/logout check the session themselves.
	"POST /api/auth/login": open_, "POST /api/auth/logout": open_, "GET /api/auth/me": open_,
	"GET /api/auth/setup": open_, "POST /api/auth/setup/totp": open_, "POST /api/auth/setup/complete": open_,
	"POST /api/auth/password": reader,

	// Users and action log: administrators; changes ask for the TOTP code again.
	"GET /api/users": admin, "POST /api/users": adminTOTP, "PUT /api/users/{name}": adminTOTP,
	"POST /api/users/{name}/reset": adminTOTP, "GET /api/audit": admin,

	// Reading: every role.
	"GET /api/config": reader, "GET /api/clusters": reader, "GET /api/version": reader, "GET /api/update-status": reader,
	"GET /api/clusters/{idx}/network-settings": reader, "GET /api/clusters/{idx}/report-settings": reader,
	"GET /api/clusters/{idx}/node-readiness": reader,
	"GET /api/dashboard":                     reader, "GET /api/nodes": reader, "GET /api/nodes/{node}/status": reader,
	"GET /api/nodes/{node}/rrd": reader, "GET /api/nodes/{node}/storage": reader, "GET /api/vms": reader,
	"GET /api/nodes/{node}/qemu/{vmid}/status": reader, "GET /api/nodes/{node}/qemu/{vmid}/rrd": reader,
	"GET /api/nodes/{node}/qemu/{vmid}/fsinfo": reader, "GET /api/nodes/{node}/qemu/{vmid}/osinfo": reader,
	"GET /api/nodes/{node}/qemu/{vmid}/netinfo": reader, "GET /api/nodes/{node}/qemu/{vmid}/gpu/plan": reader,
	"GET /api/templates": reader, "GET /api/nextid": reader, "GET /api/groups": reader, "GET /api/storage": reader,
	"GET /api/pools": reader, "GET /api/tasks": reader, "GET /api/metrics": reader, "GET /api/top": reader,
	"GET /api/ceph/summary": reader, "GET /api/ceph": reader, "GET /api/gpus": reader, "GET /api/gpu-usage": reader,
	"GET /api/reports/{model}": reader, "GET /api/provision/advice": reader, "GET /api/provision/job": reader,
	"GET /api/balance": reader, "GET /api/balance/task": reader, "GET /api/balance/job": reader,

	// Operator: power actions, snapshots, console, commands in the guests, new VMs, groups, Batch.
	"POST /api/nodes/{node}/qemu/{vmid}/action/{action}": operator, "POST /api/nodes/{node}/qemu/{vmid}/snapshot": operator,
	"POST /api/nodes/{node}/qemu/{vmid}/dmesg": operator, "POST /api/nodes/{node}/qemu/{vmid}/adduser": operator,
	"POST /api/nodes/{node}/qemu/{vmid}/adddisk": operator, "POST /api/nodes/{node}/qemu/{vmid}/setup-disk": operator,
	"POST /api/nodes/{node}/qemu/{vmid}/termproxy": operator, "GET /api/nodes/{node}/qemu/{vmid}/termproxy-ws": operator,
	"POST /api/nodes/{node}/lxc/{vmid}/action/{action}": operator, "POST /api/nodes/{node}/lxc/{vmid}/snapshot": operator,
	"POST /api/batch": operator, "PUT /api/nodes/{node}/{kind}/{vmid}/groups": operator,
	"POST /api/provision/job": operator, "POST /api/update-check": operator,

	// Administrator: deletions, node power, migrations and balance, GPU assignment, settings, tokens.
	"DELETE /api/nodes/{node}/qemu/{vmid}": adminTOTP, "DELETE /api/nodes/{node}/lxc/{vmid}": adminTOTP,
	"POST /api/nodes/{node}/cmd/{cmd}":           adminTOTP,
	"POST /api/nodes/{node}/qemu/{vmid}/migrate": admin, "POST /api/balance/step": admin, "POST /api/balance/job/decide": admin,
	"POST /api/nodes/{node}/qemu/{vmid}/gpu": admin, "DELETE /api/nodes/{node}/qemu/{vmid}/gpu/{slot}": admin,
	"PUT /api/gpu-models": admin,
	"POST /api/config":    admin, "POST /api/config/test": admin,
	"POST /api/clusters": adminTOTP, "PUT /api/clusters/{idx}": adminTOTP, "DELETE /api/clusters/{idx}": adminTOTP,
	"POST /api/clusters/{idx}/test": admin, "PUT /api/clusters/{idx}/default": admin,
	"PUT /api/clusters/{idx}/network-settings": admin, "PUT /api/clusters/{idx}/report-settings": admin,
	"POST /api/clusters/{idx}/report-settings/price-check": admin,
}

// StepUpHeader carries the TOTP code for the actions that require it again.
const StepUpHeader = "X-TOTP-Code"

type ctxKey struct{}

// sessionFrom returns the session the authorization middleware attached to the request.
func sessionFrom(r *http.Request) (auth.Session, bool) {
	s, ok := r.Context().Value(ctxKey{}).(auth.Session)
	return s, ok
}

func jsonError(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("authz: write error response: %v", err)
	}
}

// recorder keeps the status and the beginning of the body of a write, to find the Proxmox UPID for the log.
type recorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

const recordLimit = 64 << 10

func (rw *recorder) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recorder) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	if room := recordLimit - len(rw.body); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		rw.body = append(rw.body, b[:room]...)
	}
	return rw.ResponseWriter.Write(b)
}

var upidPattern = regexp.MustCompile(`UPID:[A-Za-z0-9._@!:-]+`)

// targetOf summarises what a request acts on: the route parameters and the cluster, nothing from the body.
func targetOf(rctx *chi.Context, r *http.Request) string {
	parts := []string{}
	if c := r.URL.Query().Get("cluster"); c != "" {
		parts = append(parts, "cluster="+c)
	} else if c := r.Header.Get("X-Cluster-Index"); c != "" {
		parts = append(parts, "cluster="+c)
	}
	for i, k := range rctx.URLParams.Keys {
		if k == "*" {
			continue
		}
		parts = append(parts, k+"="+rctx.URLParams.Values[i])
	}
	return strings.Join(parts, " ")
}

// authorize is the authorization middleware (voce 20, step 3). Order of checks: route rule (fail closed),
// first-start mode, session, role, CSRF token on writes, TOTP step-up, then the handler; every write is
// logged with user, address, route, target, status and the Proxmox UPID it returned.
func authorize(root *chi.Mux, store *auth.Store, proxies []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The route must be looked up on exactly the path the router will use: chi routes on the still
			// encoded RawPath when there is one. Looking up the decoded Path instead let "/api/nodes/a%2Fb/status"
			// find no rule here (decoded it has an extra segment) while the router matched it with node="a/b":
			// the request skipped every check (found in the step 7 review, 2026-11-23).
			routePath := r.URL.Path
			if r.URL.RawPath != "" {
				routePath = r.URL.RawPath
			}
			// No Proxmox name contains a slash or a backslash: an encoded slash, or a backslash (Go keeps RawPath empty
			// for "%5C" because it is the default escaping), in an API path is refused outright.
			if strings.HasPrefix(r.URL.Path, "/api/") && (containsEncodedSeparator(r.URL.RawPath) || strings.Contains(r.URL.Path, `\`)) {
				jsonError(w, http.StatusBadRequest, map[string]any{"error": "indirizzo non valido"})
				return
			}
			rctx := chi.NewRouteContext()
			pattern := root.Find(rctx, r.Method, routePath)
			if pattern == "" {
				// The router will not find a route either (same path): it answers 404/405. Nothing under /api
				// may reach a handler without a rule, so refuse /api here instead of trusting that.
				if strings.HasPrefix(routePath, "/api/") {
					jsonError(w, http.StatusNotFound, map[string]any{"error": "non trovato"})
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			key := r.Method + " " + pattern
			rule, ok := routeAccess[key]
			if !ok {
				log.Printf("authz: route without an access rule refused: %s", key)
				jsonError(w, http.StatusForbidden, map[string]any{"error": "accesso negato"})
				return
			}
			if rule.role == public || (key == "GET /api/version" && localVersionProbe(r)) {
				next.ServeHTTP(w, r)
				return
			}
			if n, err := store.CountUsers(); err != nil {
				log.Printf("authz: count users: %v", err)
				jsonError(w, http.StatusInternalServerError, map[string]any{"error": "errore interno"})
				return
			} else if n == 0 {
				jsonError(w, http.StatusUnauthorized, map[string]any{"error": "configurazione iniziale richiesta", "setup": true})
				return
			}
			c, err := r.Cookie(auth.CookieName)
			if err != nil {
				jsonError(w, http.StatusUnauthorized, map[string]any{"error": "accesso richiesto", "login": true})
				return
			}
			sess, err := store.Session(c.Value)
			if err != nil {
				jsonError(w, http.StatusUnauthorized, map[string]any{"error": "sessione scaduta: accedi di nuovo", "login": true})
				return
			}
			if sess.User.Role < rule.role {
				jsonError(w, http.StatusForbidden, map[string]any{"error": "il tuo ruolo non permette questa azione", "required": rule.role.String()})
				return
			}
			write := r.Method != http.MethodGet && r.Method != http.MethodHead
			if write && !auth.CheckCSRF(sess, r.Header.Get(auth.CSRFHeader)) {
				jsonError(w, http.StatusForbidden, map[string]any{"error": "richiesta non valida (token CSRF)"})
				return
			}
			ip := auth.ClientIP(r, proxies)
			if rule.stepUp {
				switch err := store.StepUp(sess, r.Header.Get(StepUpHeader), ip); {
				case errors.Is(err, auth.ErrTooManyAttempts):
					jsonError(w, http.StatusTooManyRequests, map[string]any{"error": err.Error()})
					return
				case err != nil:
					jsonError(w, http.StatusForbidden, map[string]any{"error": "questa azione richiede il codice TOTP", "reason": "step_up"})
					return
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess))
			if !write {
				next.ServeHTTP(w, r)
				return
			}
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			if err := store.AddAudit(auth.AuditEntry{User: sess.User.Username, IP: ip, Action: key, Target: targetOf(rctx, r),
				Status: status, UPID: upidPattern.FindString(string(rec.body))}); err != nil {
				log.Printf("authz: action log: %v", err)
			}
		})
	}
}

// routeKeys lists the "METHOD pattern" of every registered route (used by the tests).
func routeKeys(r chi.Routes) ([]string, error) {
	var keys []string
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		keys = append(keys, method+" "+strings.TrimSuffix(route, "/*/")+"")
		return nil
	})
	sort.Strings(keys)
	return keys, err
}

// containsEncodedSeparator reports an encoded "/" or "\" (any case) in a raw path.
func containsEncodedSeparator(raw string) bool {
	l := strings.ToLower(raw)
	return strings.Contains(l, "%2f") || strings.Contains(l, "%5c")
}
