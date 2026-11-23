package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"proxmox-manager/internal/auth"

	"github.com/go-chi/chi/v5"
)

// usersAPI serves /api/users and /api/audit (administrators; changes require the TOTP step-up, see routeAccess).
type usersAPI struct{ store *auth.Store }

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		jsonError(w, http.StatusBadRequest, map[string]any{"error": "richiesta troppo grande"})
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.More() {
		jsonError(w, http.StatusBadRequest, map[string]any{"error": "richiesta non valida"})
		return false
	}
	return true
}

func okJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("users: write response: %v", err)
	}
}

func (u usersAPI) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNoUser):
		jsonError(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case errors.Is(err, auth.ErrUserExists), errors.Is(err, auth.ErrLastAdmin):
		jsonError(w, http.StatusConflict, map[string]any{"error": err.Error()})
	case errors.Is(err, auth.ErrBadUsername), errors.Is(err, auth.ErrWeakPassword):
		jsonError(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		log.Printf("users: %v", err)
		jsonError(w, http.StatusInternalServerError, map[string]any{"error": "errore interno"})
	}
}

func (u usersAPI) list(w http.ResponseWriter, r *http.Request) {
	users, err := u.store.ListUsers()
	if err != nil {
		u.fail(w, err)
		return
	}
	okJSON(w, users)
}

// credentials are shown once to the administrator, who hands them to the person; never logged.
type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Secret   string `json:"totp_secret"`
	URI      string `json:"totp_uri"`
}

func (u usersAPI) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	role, ok := auth.ParseRole(req.Role)
	if !ok {
		jsonError(w, http.StatusBadRequest, map[string]any{"error": "ruolo non valido"})
		return
	}
	password, err := auth.RandomPassword()
	if err != nil {
		u.fail(w, err)
		return
	}
	secret, err := u.store.CreateUser(req.Username, role, password)
	if err != nil {
		u.fail(w, err)
		return
	}
	okJSON(w, credentials{req.Username, password, secret, auth.TOTPURI("ProxmoxManager", req.Username, secret)})
}

func (u usersAPI) update(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Role == nil && req.Disabled == nil {
		jsonError(w, http.StatusBadRequest, map[string]any{"error": "niente da cambiare"})
		return
	}
	// An administrator cannot disable or demote their own account from the page: it would end their own
	// access by mistake. Another administrator (or the command line) can.
	if sess, ok := sessionFrom(r); ok && sess.User.Username == name {
		if (req.Disabled != nil && *req.Disabled) || (req.Role != nil && *req.Role != "admin") {
			jsonError(w, http.StatusConflict, map[string]any{"error": "non puoi disattivare o declassare il tuo account"})
			return
		}
	}
	if req.Role != nil {
		role, ok := auth.ParseRole(*req.Role)
		if !ok {
			jsonError(w, http.StatusBadRequest, map[string]any{"error": "ruolo non valido"})
			return
		}
		if err := u.store.SetRole(name, role); err != nil {
			u.fail(w, err)
			return
		}
	}
	if req.Disabled != nil {
		if err := u.store.SetDisabled(name, *req.Disabled); err != nil {
			u.fail(w, err)
			return
		}
	}
	okJSON(w, map[string]bool{"ok": true})
}

func (u usersAPI) reset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	password, err := auth.RandomPassword()
	if err != nil {
		u.fail(w, err)
		return
	}
	secret, err := u.store.ResetCredentials(name, password)
	if err != nil {
		u.fail(w, err)
		return
	}
	okJSON(w, credentials{name, password, secret, auth.TOTPURI("ProxmoxManager", name, secret)})
}

func (u usersAPI) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	user := q.Get("user")
	if user != "" && !auth.ValidUsername(user) {
		jsonError(w, http.StatusBadRequest, map[string]any{"error": "utente non valido"})
		return
	}
	rows, err := u.store.ListAudit(limit, before, user)
	if err != nil {
		u.fail(w, err)
		return
	}
	okJSON(w, rows)
}
