package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// CookieName uses the __Host- prefix: the browser accepts it only with Secure, Path=/ and no Domain, so a
// sibling host cannot set or overwrite it.
const CookieName = "__Host-pmgr"

// CSRFHeader carries the session's CSRF token on every request that writes.
const CSRFHeader = "X-CSRF-Token"

// ParseTrustedProxies validates the trusted_proxies list (IP addresses or CIDR prefixes).
func ParseTrustedProxies(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, v := range list {
		v = strings.TrimSpace(v)
		if p, err := netip.ParsePrefix(v); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(v)
		if err != nil {
			return nil, errors.New("trusted_proxies: indirizzo non valido")
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func trusted(a netip.Addr, proxies []netip.Prefix) bool {
	for _, p := range proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the address of the real client. X-Forwarded-For is honoured only when the direct peer
// is a trusted proxy, and then the rightmost address that is not itself a trusted proxy is taken (the
// leftmost entries are written by the client and can be forged).
func ClientIP(r *http.Request, proxies []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !trusted(peer, proxies) {
		return peer.String()
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !trusted(a, proxies) {
			return a.String()
		}
	}
	return peer.String()
}

// Handlers serves /api/auth/*. The pages and the other routes are protected by the middleware (step 3).
type Handlers struct {
	Store   *Store
	Proxies []netip.Prefix
	// Audit records a security event (login, logout, setup, password change); may be nil.
	Audit func(r *http.Request, user, event, outcome string)
}

const maxAuthBody = 4096

func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "richiesta troppo grande")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.More() {
		writeErr(w, http.StatusBadRequest, "richiesta non valida")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("auth: write response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (h *Handlers) audit(r *http.Request, user, event, outcome string) {
	if h.Audit != nil {
		h.Audit(r, user, event, outcome)
	}
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(SessionAbsolute / time.Second)})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// SessionFrom returns the session of the request, if any.
func (h *Handlers) SessionFrom(r *http.Request) (Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, ErrNoSession
	}
	return h.Store.Session(c.Value)
}

type meResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	CSRF     string `json:"csrf"`
	Expires  int64  `json:"expires"`
}

func me(s Session) meResponse {
	return meResponse{Username: s.User.Username, Role: s.User.RoleName, CSRF: s.CSRF, Expires: s.Expires.Unix()}
}

// Login: POST {username, password, code}.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	ip := ClientIP(r, h.Proxies)
	token, sess, err := h.Store.Login(req.Username, req.Password, req.Code, ip)
	switch {
	case errors.Is(err, ErrTooManyAttempts):
		w.Header().Set("Retry-After", "900")
		h.audit(r, req.Username, "login", "limite di tentativi per indirizzo")
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, ErrInvalidCredentials):
		h.audit(r, req.Username, "login", "fallito")
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		log.Printf("auth: login error: %v", err)
		writeErr(w, http.StatusInternalServerError, "errore interno")
		return
	}
	setSessionCookie(w, token)
	h.audit(r, sess.User.Username, "login", "riuscito")
	writeJSON(w, http.StatusOK, me(sess))
}

// Logout: POST. Requires the CSRF header of the session.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err == nil {
		if sess, err := h.Store.Session(c.Value); err == nil {
			if !CheckCSRF(sess, r.Header.Get(CSRFHeader)) {
				writeErr(w, http.StatusForbidden, "richiesta non valida")
				return
			}
			if err := h.Store.Logout(c.Value); err != nil {
				log.Printf("auth: logout: %v", err)
			}
			h.audit(r, sess.User.Username, "logout", "riuscito")
		}
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Me: GET. The current user, role and CSRF token, or 401.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	sess, err := h.SessionFrom(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, ErrNoSession.Error())
		return
	}
	writeJSON(w, http.StatusOK, me(sess))
}

// SetupStatus: GET {open}. Says only whether the first-start configuration is available.
func (h *Handlers) SetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"open": h.Store.SetupOpen()})
}

// SetupTOTP: POST {code}. Checks the one-time code and returns the TOTP secret for the first administrator.
func (h *Handlers) SetupTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	secret, err := h.Store.SetupTOTP(req.Code, ClientIP(r, h.Proxies))
	if err != nil {
		h.setupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": TOTPURI("ProxmoxManager", "amministratore", secret)})
}

// SetupComplete: POST {code, username, password, totp}. Creates the first administrator; then the user logs in.
func (h *Handlers) SetupComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	if err := h.Store.CompleteSetup(req.Code, ClientIP(r, h.Proxies), req.Username, req.Password, req.TOTP); err != nil {
		h.setupError(w, r, err)
		return
	}
	h.audit(r, req.Username, "setup", "primo amministratore creato")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) setupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTooManyAttempts):
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ErrSetupClosed):
		writeErr(w, http.StatusGone, err.Error())
	case errors.Is(err, ErrBadCode):
		h.audit(r, "", "setup", "codice errato")
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrBadUsername), errors.Is(err, ErrWeakPassword), errors.Is(err, ErrUserExists):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("auth: setup error: %v", err)
		writeErr(w, http.StatusInternalServerError, "errore interno")
	}
}

// ChangePassword: POST {old, new, code}. Session and CSRF header required.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	sess, err := h.SessionFrom(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, ErrNoSession.Error())
		return
	}
	if !CheckCSRF(sess, r.Header.Get(CSRFHeader)) {
		writeErr(w, http.StatusForbidden, "richiesta non valida")
		return
	}
	var req struct {
		Old  string `json:"old"`
		New  string `json:"new"`
		Code string `json:"code"`
	}
	if !decodeStrict(w, r, &req) {
		return
	}
	err = h.Store.ChangePassword(sess, req.Old, req.New, req.Code, ClientIP(r, h.Proxies))
	switch {
	case err == nil:
		h.audit(r, sess.User.Username, "password", "cambiata")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrTooManyAttempts):
		writeErr(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrBadCode):
		h.audit(r, sess.User.Username, "password", "rifiutata")
		writeErr(w, http.StatusForbidden, "password attuale o codice non validi")
	default:
		log.Printf("auth: change password: %v", err)
		writeErr(w, http.StatusInternalServerError, "errore interno")
	}
}
