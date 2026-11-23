package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── password ────────────────────────────────────────────────────────────────

func TestPasswordHashVerifyAndPolicy(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("hash format %q", h)
	}
	if ok, err := VerifyPassword("correct horse battery", h); !ok || err != nil {
		t.Fatal("right password refused")
	}
	if ok, _ := VerifyPassword("correct horse batterY", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("same salt twice")
	}
	for _, bad := range []string{"short", "eleven char", strings.Repeat("a", PasswordMaxBytes+1), "twelve chars\x00x", "tab\tinside the password", "\xff\xfe invalid utf8 x"} {
		if ValidPassword(bad) == nil {
			t.Fatalf("policy accepted %q", bad)
		}
	}
	if ValidPassword("dodici caratteri ok €") != nil {
		t.Fatal("policy refused a long unicode password")
	}
	// A tampered row must not make the server allocate huge memory or accept anything.
	for _, bad := range []string{
		"$argon2id$v=19$m=999999999,t=3,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=65536,t=99,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=16$m=65536,t=3,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
		"garbage",
	} {
		if ok, err := VerifyPassword("whatever password", bad); ok || err == nil {
			t.Fatalf("tampered hash accepted: %q", bad)
		}
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := RandomPassword()
		if err != nil || len(p) != 24 || ValidPassword(p) != nil || seen[p] {
			t.Fatalf("random password %q %v", p, err)
		}
		if strings.ContainsAny(p, "lIoO01") {
			t.Fatalf("look-alike character in %q", p)
		}
		seen[p] = true
	}
}

// ── TOTP ────────────────────────────────────────────────────────────────────

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	// RFC 6238 appendix B (SHA-1), last 6 digits of the 8-digit values.
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, ts/30); got != want {
			t.Fatalf("t=%d: %s, want %s", ts, got, want)
		}
	}
}

func TestTOTPWindowReplayAndFormat(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_000_000_000, 0)
	cur := now.Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		if _, ok := verifyTOTP(secret, totpCode(secret, cur+d), now, 0); !ok {
			t.Fatalf("step %+d refused", d)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := verifyTOTP(secret, totpCode(secret, cur+d), now, 0); ok {
			t.Fatalf("step %+d accepted", d)
		}
	}
	if _, ok := verifyTOTP(secret, totpCode(secret, cur), now, cur); ok {
		t.Fatal("replayed step accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "12a456", "-12345"} {
		if _, ok := verifyTOTP(secret, bad, now, 0); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	uri := TOTPURI("ProxmoxManager", "mario.rossi", "ABCDEF")
	if !strings.HasPrefix(uri, "otpauth://totp/ProxmoxManager:mario.rossi?") || !strings.Contains(uri, "secret=ABCDEF") {
		t.Fatalf("uri %q", uri)
	}
}

// ── secret key and encryption ───────────────────────────────────────────────

func TestKeyFileAndSealer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k")
	key, err := loadOrCreateKey(path)
	if err != nil || len(key) != 32 {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	again, err := loadOrCreateKey(path)
	if err != nil || string(again) != string(key) {
		t.Fatal("key not stable across loads")
	}
	s, _ := newSealer(key)
	box, _ := s.seal([]byte("secret"), "mario")
	if plain, err := s.open(box, "mario"); err != nil || string(plain) != "secret" {
		t.Fatal("round trip")
	}
	if _, err := s.open(box, "luigi"); err == nil {
		t.Fatal("ciphertext of another user accepted")
	}
	box[len(box)-1] ^= 1
	if _, err := s.open(box, "mario"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	os.Chmod(path, 0o640)
	if _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("group-readable key accepted")
	}
	os.Chmod(path, 0o600)
	link := filepath.Join(dir, "l")
	os.Symlink(path, link)
	if _, err := loadOrCreateKey(link); err == nil {
		t.Fatal("symlinked key accepted")
	}
	short := filepath.Join(dir, "s")
	os.WriteFile(short, []byte("short"), 0o600)
	if _, err := loadOrCreateKey(short); err == nil {
		t.Fatal("short key accepted")
	}
}

// ── store ───────────────────────────────────────────────────────────────────

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestStore(t *testing.T) (*Store, *clock, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var codes []string
	s, err := Open(filepath.Join(dir, "a.db"), filepath.Join(dir, "a.key"), func(c string) { codes = append(codes, c) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s.now = c.now
	// Open computed the setup deadline with the real clock: compute it again with the test clock.
	codes = nil
	if err := s.refreshSetup(); err != nil {
		t.Fatal(err)
	}
	return s, c, &codes
}

func secretOf(t *testing.T, b32s string) []byte {
	t.Helper()
	b, err := b32.DecodeString(b32s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func code(secret []byte, c *clock) string { return totpCode(secret, c.now().Unix()/30) }

const pw = "una password lunga e sicura"

func TestLoginSessionAndGenericErrors(t *testing.T) {
	s, c, _ := newTestStore(t)
	b32s, err := s.CreateUser("mario", RoleOperator, pw)
	if err != nil {
		t.Fatal(err)
	}
	sec := secretOf(t, b32s)
	if _, err := s.CreateUser("mario", RoleReader, pw); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.CreateUser("Mario!", RoleReader, pw); !errors.Is(err, ErrBadUsername) {
		t.Fatal("bad username accepted")
	}
	// Wrong password, wrong code, unknown user: same error.
	for _, try := range [][3]string{{"mario", "sbagliata sbagliata", code(sec, c)}, {"mario", pw, "000000"}, {"nessuno", pw, "123456"}} {
		if _, _, err := s.Login(try[0], try[1], try[2], "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("%v: %v", try, err)
		}
	}
	tok, sess, err := s.Login("mario", pw, code(sec, c), "10.0.0.1")
	if err != nil || sess.User.Role != RoleOperator || sess.CSRF == "" || len(tok) < 40 {
		t.Fatalf("login: %v %+v", err, sess)
	}
	// The same code cannot be used twice.
	if _, _, err := s.Login("mario", pw, code(sec, c), "10.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("TOTP replay accepted")
	}
	got, err := s.Session(tok)
	if err != nil || got.User.Username != "mario" || got.CSRF != sess.CSRF {
		t.Fatalf("session: %v", err)
	}
	if !CheckCSRF(got, sess.CSRF) || CheckCSRF(got, "") || CheckCSRF(got, "x"+sess.CSRF) {
		t.Fatal("CSRF check")
	}
	// The database stores only the hash of the session token.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE id_hash=?`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("raw session token stored")
	}
	// Role changes apply to existing sessions.
	if err := s.SetRole("mario", RoleReader); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Session(tok); got.User.Role != RoleReader {
		t.Fatal("role not refreshed")
	}
	if err := s.Logout(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("session alive after logout")
	}
	for _, bad := range []string{"", strings.Repeat("a", 101), "not-a-token"} {
		if _, err := s.Session(bad); !errors.Is(err, ErrNoSession) {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	s, c, _ := newTestStore(t)
	sec := secretOf(t, must(s.CreateUser("anna", RoleAdmin, pw)))
	tok, _, err := s.Login("anna", pw, code(sec, c), "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	// Activity every 20 minutes keeps the session alive...
	for i := 0; i < 40; i++ {
		c.add(20 * time.Minute)
		if c.now().Sub(time.Unix(1_800_000_000, 0)) >= SessionAbsolute {
			break
		}
		if _, err := s.Session(tok); err != nil {
			t.Fatalf("idle refresh failed after %d rounds: %v", i, err)
		}
	}
	// ...but never beyond 12 hours.
	if _, err := s.Session(tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("session alive beyond the absolute limit")
	}
	c.add(time.Minute)
	tok2, _, err := s.Login("anna", pw, code(sec, c), "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	c.add(SessionIdle + time.Second)
	if _, err := s.Session(tok2); !errors.Is(err, ErrNoSession) {
		t.Fatal("session alive after 30 idle minutes")
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestAccountLockoutAndIPLimit(t *testing.T) {
	s, c, _ := newTestStore(t)
	sec := secretOf(t, must(s.CreateUser("luca", RoleReader, pw)))
	for i := 0; i < maxAccountFailures; i++ {
		s.Login("luca", "password sbagliata!", "000000", "10.0.0.3")
	}
	c.add(31 * time.Second)
	if _, _, err := s.Login("luca", pw, code(sec, c), "10.0.0.4"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("locked account logged in")
	}
	c.add(accountLock)
	if _, _, err := s.Login("luca", pw, code(sec, c), "10.0.0.4"); err != nil {
		t.Fatalf("account still locked after %v: %v", accountLock, err)
	}
	// Per-IP limit, also for unknown users (no account to lock).
	for i := 0; i < maxIPFailures; i++ {
		s.Login("ghost", "password sbagliata!", "000000", "10.9.9.9")
	}
	if _, _, err := s.Login("luca", pw, "000000", "10.9.9.9"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("IP limit: %v", err)
	}
	c.add(ipWindow + time.Second)
	c.add(31 * time.Second)
	if _, _, err := s.Login("luca", pw, code(sec, c), "10.9.9.9"); err != nil {
		t.Fatalf("IP limit not lifted: %v", err)
	}
}

func TestDisableResetLastAdminAndStepUp(t *testing.T) {
	s, c, _ := newTestStore(t)
	sec := secretOf(t, must(s.CreateUser("admin", RoleAdmin, pw)))
	if err := s.SetDisabled("admin", true); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("last admin disabled")
	}
	if err := s.SetRole("admin", RoleOperator); !errors.Is(err, ErrLastAdmin) {
		t.Fatal("last admin demoted")
	}
	secB := secretOf(t, must(s.CreateUser("bea", RoleOperator, pw)))
	tok, sess, err := s.Login("bea", pw, code(secB, c), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	c.add(31 * time.Second)
	if err := s.StepUp(sess, "000000", "10.0.0.5"); !errors.Is(err, ErrBadCode) {
		t.Fatal("bad step-up code accepted")
	}
	if err := s.StepUp(sess, code(secB, c), "10.0.0.5"); err != nil {
		t.Fatalf("step-up: %v", err)
	}
	if err := s.StepUp(sess, code(secB, c), "10.0.0.5"); !errors.Is(err, ErrBadCode) {
		t.Fatal("step-up code replayed")
	}
	if err := s.SetDisabled("bea", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("disabled user's session alive")
	}
	c.add(31 * time.Second)
	if _, _, err := s.Login("bea", pw, code(secB, c), "10.0.0.5"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("disabled user logged in")
	}
	s.SetDisabled("bea", false)
	c.add(31 * time.Second)
	tok, _, err = s.Login("bea", pw, code(secB, c), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	newSec, err := s.ResetCredentials("bea", "nuova password lunghissima")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("session survived a reset")
	}
	c.add(31 * time.Second)
	if _, _, err := s.Login("bea", pw, code(secB, c), "10.0.0.5"); err == nil {
		t.Fatal("old credentials still valid after reset")
	}
	c.add(31 * time.Second)
	if _, _, err := s.Login("bea", "nuova password lunghissima", code(secretOf(t, newSec), c), "10.0.0.5"); err != nil {
		t.Fatalf("new credentials: %v", err)
	}
	if _, err := s.ResetCredentials("nessuno", pw); !errors.Is(err, ErrNoUser) {
		t.Fatal("reset of unknown user")
	}
	_ = sec
}

func TestChangePassword(t *testing.T) {
	s, c, _ := newTestStore(t)
	sec := secretOf(t, must(s.CreateUser("carla", RoleReader, pw)))
	tok, sess, _ := s.Login("carla", pw, code(sec, c), "10.0.0.6")
	c.add(31 * time.Second)
	other, _, _ := s.Login("carla", pw, code(sec, c), "10.0.0.7")
	c.add(31 * time.Second)
	if err := s.ChangePassword(sess, "sbagliata sbagliata", "altra password lunga", code(sec, c), "10.0.0.6"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("wrong old password accepted")
	}
	if err := s.ChangePassword(sess, pw, "corta", code(sec, c), "10.0.0.6"); !errors.Is(err, ErrWeakPassword) {
		t.Fatal("weak password accepted")
	}
	if err := s.ChangePassword(sess, pw, "altra password lunga", code(sec, c), "10.0.0.6"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(tok); err != nil {
		t.Fatal("current session ended by its own password change")
	}
	if _, err := s.Session(other); !errors.Is(err, ErrNoSession) {
		t.Fatal("other session survived the password change")
	}
}

func TestSetupFlow(t *testing.T) {
	s, c, codes := newTestStore(t)
	if !s.SetupOpen() || len(*codes) != 1 || len((*codes)[0]) < 20 {
		t.Fatalf("setup not open with a code: %v", *codes)
	}
	setup := (*codes)[0]
	if _, err := s.SetupTOTP("wrong", "10.0.1.1"); !errors.Is(err, ErrBadCode) {
		t.Fatal("wrong setup code accepted")
	}
	b32s, err := s.SetupTOTP(setup, "10.0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	sec := secretOf(t, b32s)
	if err := s.CompleteSetup(setup, "10.0.1.1", "root", pw, "000000"); !errors.Is(err, ErrBadCode) {
		t.Fatal("setup without a valid TOTP code")
	}
	if err := s.CompleteSetup(setup, "10.0.1.1", "root", pw, code(sec, c)); err != nil {
		t.Fatal(err)
	}
	// The enrolment code cannot be replayed to log in.
	if _, _, err := s.Login("root", pw, code(sec, c), "10.0.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("setup TOTP code replayed at login")
	}
	if s.SetupOpen() {
		t.Fatal("setup still open after the first admin")
	}
	if _, err := s.SetupTOTP(setup, "10.0.1.1"); !errors.Is(err, ErrSetupClosed) {
		t.Fatal("setup code reusable")
	}
	c.add(31 * time.Second)
	if _, sess, err := s.Login("root", pw, code(sec, c), "10.0.1.1"); err != nil || sess.User.Role != RoleAdmin {
		t.Fatalf("first admin login: %v", err)
	}
	// Setup code expires after an hour.
	s2, c2, codes2 := newTestStore(t)
	c2.add(setupCodeTTL + time.Second)
	if _, err := s2.SetupTOTP((*codes2)[0], "10.0.1.2"); !errors.Is(err, ErrSetupClosed) || s2.SetupOpen() {
		t.Fatal("expired setup code accepted")
	}
	// Creating a user from the command line closes setup mode too.
	s3, _, _ := newTestStore(t)
	s3.CreateUser("cli", RoleAdmin, pw)
	if s3.SetupOpen() {
		t.Fatal("setup open after CreateUser")
	}
	// Brute force on the setup code hits the per-IP limit.
	s4, _, codes4 := newTestStore(t)
	for i := 0; i < maxIPFailures; i++ {
		s4.SetupTOTP("guess", "10.0.1.3")
	}
	if _, err := s4.SetupTOTP((*codes4)[0], "10.0.1.3"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("setup brute force not limited: %v", err)
	}
}

func TestTOTPSecretsEncryptedAtRest(t *testing.T) {
	s, _, _ := newTestStore(t)
	b32s := must(s.CreateUser("dora", RoleReader, pw))
	var box []byte
	s.db.QueryRow(`SELECT totp_enc FROM auth_users WHERE username='dora'`).Scan(&box)
	if strings.Contains(string(box), string(secretOf(t, b32s))) {
		t.Fatal("TOTP secret stored in clear")
	}
}

// ── HTTP ────────────────────────────────────────────────────────────────────

func TestClientIP(t *testing.T) {
	proxies, err := ParseTrustedProxies([]string{"127.0.0.1", "10.10.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:4000", "1.2.3.4", "203.0.113.5"},                // untrusted peer: XFF ignored
		{"127.0.0.1:4000", "198.51.100.7", "198.51.100.7"},            // proxy: client from XFF
		{"127.0.0.1:4000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},   // forged leftmost entry ignored
		{"127.0.0.1:4000", "198.51.100.7, 10.10.1.1", "198.51.100.7"}, // chain of trusted proxies skipped
		{"127.0.0.1:4000", "", "127.0.0.1"},
		{"127.0.0.1:4000", "garbage", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r, proxies); got != c.want {
			t.Fatalf("%+v: %s", c, got)
		}
	}
	if _, err := ParseTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Fatal("bad proxy accepted")
	}
}

func TestLoginHandlerCookieAndStrictBody(t *testing.T) {
	s, c, _ := newTestStore(t)
	sec := secretOf(t, must(s.CreateUser("eva", RoleAdmin, pw)))
	h := &Handlers{Store: s}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		r.RemoteAddr = "10.0.2.1:5000"
		w := httptest.NewRecorder()
		h.Login(w, r)
		return w
	}
	if w := post(`{"username":"eva","password":"x","code":"1","extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	if w := post(`{"username":"eva","password":"sbagliata sbagliata","code":"000000"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", w.Code)
	}
	c.add(31 * time.Second)
	w := post(`{"username":"eva","password":"` + pw + `","code":"` + code(sec, c) + `"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	ck := w.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != CookieName || !ck[0].HttpOnly || !ck[0].Secure || ck[0].SameSite != http.SameSiteStrictMode || ck[0].Path != "/" {
		t.Fatalf("cookie %+v", ck)
	}
	var m meResponse
	json.Unmarshal(w.Body.Bytes(), &m)
	if m.Username != "eva" || m.Role != "admin" || m.CSRF == "" {
		t.Fatalf("me %+v", m)
	}
	if strings.Contains(w.Body.String(), ck[0].Value) {
		t.Fatal("session token in the body")
	}
	// Me with and without cookie.
	r := httptest.NewRequest("GET", "/api/auth/me", nil)
	r.AddCookie(ck[0])
	wm := httptest.NewRecorder()
	h.Me(wm, r)
	if wm.Code != http.StatusOK {
		t.Fatal("me with cookie")
	}
	wm = httptest.NewRecorder()
	h.Me(wm, httptest.NewRequest("GET", "/api/auth/me", nil))
	if wm.Code != http.StatusUnauthorized {
		t.Fatal("me without cookie")
	}
	// Logout needs the CSRF header.
	r = httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(ck[0])
	wl := httptest.NewRecorder()
	h.Logout(wl, r)
	if wl.Code != http.StatusForbidden {
		t.Fatal("logout without CSRF header")
	}
	r.Header.Set(CSRFHeader, m.CSRF)
	wl = httptest.NewRecorder()
	h.Logout(wl, r)
	if wl.Code != http.StatusOK {
		t.Fatal("logout")
	}
	if _, err := s.Session(ck[0].Value); err == nil {
		t.Fatal("session alive after logout")
	}
}
