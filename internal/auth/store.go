package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Role is ordered: a higher role can do everything a lower one can (D136).
type Role int

const (
	RoleReader   Role = 1
	RoleOperator Role = 2
	RoleAdmin    Role = 3
)

func (r Role) String() string {
	switch r {
	case RoleReader:
		return "reader"
	case RoleOperator:
		return "operator"
	case RoleAdmin:
		return "admin"
	}
	return ""
}

// ParseRole accepts only the three role names.
func ParseRole(s string) (Role, bool) {
	switch s {
	case "reader":
		return RoleReader, true
	case "operator":
		return RoleOperator, true
	case "admin":
		return RoleAdmin, true
	}
	return 0, false
}

// Policy values (D138, D141).
const (
	SessionIdle     = 30 * time.Minute
	SessionAbsolute = 12 * time.Hour
	sessionTouch    = time.Minute // last_seen is written at most once a minute

	maxAccountFailures = 5
	accountLock        = 15 * time.Minute
	maxIPFailures      = 20
	ipWindow           = 15 * time.Minute
	maxTrackedIPs      = 10000

	setupCodeTTL = time.Hour
)

var (
	// ErrInvalidCredentials is the only answer to a failed login: it does not say whether the user exists,
	// is locked or disabled, or which factor was wrong.
	ErrInvalidCredentials = errors.New("credenziali non valide (o account temporaneamente bloccato)")
	ErrTooManyAttempts    = errors.New("troppi tentativi da questo indirizzo: riprova tra qualche minuto")
	ErrNoSession          = errors.New("sessione assente o scaduta")
	ErrBadUsername        = errors.New("nome utente non valido (minuscole, cifre, . _ -, da 2 a 32 caratteri, inizia con una lettera)")
	ErrUserExists         = errors.New("l'utente esiste gia'")
	ErrNoUser             = errors.New("utente inesistente")
	ErrBadCode            = errors.New("codice non valido")
	ErrSetupClosed        = errors.New("la configurazione iniziale non e' disponibile (esiste gia' un amministratore, o il codice e' scaduto: riavvia il servizio per averne uno nuovo)")
	ErrLastAdmin          = errors.New("operazione negata: deve restare almeno un amministratore attivo")
)

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,31}$`)

// ValidUsername reports whether a username is acceptable.
func ValidUsername(u string) bool { return usernamePattern.MatchString(u) }

// User is an account without its secrets.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"-"`
	RoleName string `json:"role"`
	Disabled bool   `json:"disabled"`
	Created  int64  `json:"created"`
}

// Session is a valid, authenticated session.
type Session struct {
	User    User
	CSRF    string // sent by the page in a header on every write; compared in constant time
	Expires time.Time
	idHash  string
}

// Store keeps accounts and sessions in the SQLite file of the manager (its own connection, WAL).
type Store struct {
	db   *sql.DB
	seal *sealer
	now  func() time.Time

	mu       sync.Mutex
	ipFails  map[string][]time.Time
	setup    *setupState
	setupLog func(code string)
}

type setupState struct {
	code    string
	expires time.Time
	pending []byte // TOTP secret offered to the first administrator, verified before the account exists
}

const schema = `
CREATE TABLE IF NOT EXISTS auth_users (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  username       TEXT NOT NULL UNIQUE,
  role           INTEGER NOT NULL CHECK (role IN (1,2,3)),
  pass_hash      TEXT NOT NULL,
  totp_enc       BLOB NOT NULL,
  totp_last_step INTEGER NOT NULL DEFAULT 0,
  failed         INTEGER NOT NULL DEFAULT 0,
  locked_until   INTEGER NOT NULL DEFAULT 0,
  disabled       INTEGER NOT NULL DEFAULT 0,
  created        INTEGER NOT NULL,
  updated        INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_sessions (
  id_hash   TEXT PRIMARY KEY,
  user_id   INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
  csrf      TEXT NOT NULL,
  created   INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_sessions_user ON auth_sessions(user_id);
`

// Open opens the account store. keyPath is the file with the key that encrypts TOTP secrets (created 0600
// when missing). When there is no user yet, a one-time setup code is generated and passed to logSetup
// (the caller writes it to the journal only, D145).
func Open(dbPath, keyPath string, logSetup func(code string)) (*Store, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	seal, err := newSealer(key)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_timeout=5000&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, seal: seal, now: time.Now, ipFails: map[string][]time.Time{}, setupLog: logSetup}
	if err := s.refreshSetup(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// CountUsers returns the number of accounts (disabled included).
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&n)
	return n, err
}

// refreshSetup opens setup mode with a new code when there are no users, and closes it otherwise.
func (s *Store) refreshSetup() error {
	n, err := s.CountUsers()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > 0 {
		s.setup = nil
		return nil
	}
	code, err := randomToken(16) // 128 bits
	if err != nil {
		return err
	}
	s.setup = &setupState{code: code, expires: s.now().Add(setupCodeTTL)}
	if s.setupLog != nil {
		s.setupLog(code)
	}
	return nil
}

// SetupOpen reports whether the first-start configuration is available (no user and a live code).
func (s *Store) SetupOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setup != nil && s.now().Before(s.setup.expires)
}

// checkSetupCode must be called with s.mu held. A wrong code counts as a failure of the caller's IP.
func (s *Store) checkSetupCode(code, ip string) error {
	if s.setup == nil || !s.now().Before(s.setup.expires) {
		return ErrSetupClosed
	}
	// An account may have been created meanwhile from the command line (another process on the same file).
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		s.setup = nil
		return ErrSetupClosed
	}
	if s.ipBlockedLocked(ip) {
		return ErrTooManyAttempts
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.setup.code)) != 1 {
		s.ipFailLocked(ip)
		return ErrBadCode
	}
	return nil
}

// SetupTOTP checks the setup code and returns a new TOTP secret (base32) for the first administrator. The
// account is created only by CompleteSetup, after a code from that secret has been verified.
func (s *Store) SetupTOTP(code, ip string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkSetupCode(code, ip); err != nil {
		return "", err
	}
	secret, b32s, err := NewTOTPSecret()
	if err != nil {
		return "", err
	}
	s.setup.pending = secret
	return b32s, nil
}

// CompleteSetup creates the first administrator and closes setup mode for good.
func (s *Store) CompleteSetup(code, ip, username, password, totp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkSetupCode(code, ip); err != nil {
		return err
	}
	if s.setup.pending == nil {
		return ErrBadCode
	}
	step, ok := verifyTOTP(s.setup.pending, totp, s.now(), 0)
	if !ok {
		s.ipFailLocked(ip)
		return ErrBadCode
	}
	// The code that proved the enrolment is consumed: it cannot be replayed to log in.
	if err := s.insertUser(username, RoleAdmin, password, s.setup.pending, step); err != nil {
		return err
	}
	s.setup = nil
	return nil
}

func (s *Store) insertUser(username string, role Role, password string, totpSecret []byte, lastStep int64) error {
	if !ValidUsername(username) {
		return ErrBadUsername
	}
	if role.String() == "" {
		return errors.New("ruolo non valido")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	box, err := s.seal.seal(totpSecret, username)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	_, err = s.db.Exec(`INSERT INTO auth_users (username, role, pass_hash, totp_enc, totp_last_step, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		username, int(role), hash, box, lastStep, now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrUserExists
	}
	return err
}

// CreateUser creates an account with the given password and a new TOTP secret, returned in base32 (shown once).
func (s *Store) CreateUser(username string, role Role, password string) (string, error) {
	secret, b32s, err := NewTOTPSecret()
	if err != nil {
		return "", err
	}
	if err := s.insertUser(username, role, password, secret, 0); err != nil {
		return "", err
	}
	if err := s.refreshSetup(); err != nil { // the first account closes setup mode
		return "", err
	}
	return b32s, nil
}

// ResetCredentials sets a new password and a new TOTP secret, unlocks the account and ends its sessions.
func (s *Store) ResetCredentials(username, password string) (string, error) {
	secret, b32s, err := NewTOTPSecret()
	if err != nil {
		return "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	box, err := s.seal.seal(secret, username)
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE auth_users SET pass_hash=?, totp_enc=?, totp_last_step=0, failed=0, locked_until=0, updated=? WHERE username=?`,
		hash, box, s.now().Unix(), username)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNoUser
	}
	if _, err := tx.Exec(`DELETE FROM auth_sessions WHERE user_id=(SELECT id FROM auth_users WHERE username=?)`, username); err != nil {
		return "", err
	}
	return b32s, tx.Commit()
}

// activeAdmins counts enabled administrators other than username.
func (s *Store) activeAdminsExcept(tx *sql.Tx, username string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM auth_users WHERE role=? AND disabled=0 AND username<>?`, int(RoleAdmin), username).Scan(&n)
	return n, err
}

// SetDisabled enables or disables an account; disabling ends its sessions. The last active administrator
// cannot be disabled.
func (s *Store) SetDisabled(username string, disabled bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role int
	if err := tx.QueryRow(`SELECT role FROM auth_users WHERE username=?`, username).Scan(&role); errors.Is(err, sql.ErrNoRows) {
		return ErrNoUser
	} else if err != nil {
		return err
	}
	if disabled && Role(role) == RoleAdmin {
		if n, err := s.activeAdminsExcept(tx, username); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	flag := 0
	if disabled {
		flag = 1
	}
	if _, err := tx.Exec(`UPDATE auth_users SET disabled=?, updated=? WHERE username=?`, flag, s.now().Unix(), username); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.Exec(`DELETE FROM auth_sessions WHERE user_id=(SELECT id FROM auth_users WHERE username=?)`, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetRole changes the role of an account. The last active administrator cannot be demoted.
func (s *Store) SetRole(username string, role Role) error {
	if role.String() == "" {
		return errors.New("ruolo non valido")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur, disabled int
	if err := tx.QueryRow(`SELECT role, disabled FROM auth_users WHERE username=?`, username).Scan(&cur, &disabled); errors.Is(err, sql.ErrNoRows) {
		return ErrNoUser
	} else if err != nil {
		return err
	}
	if Role(cur) == RoleAdmin && role != RoleAdmin && disabled == 0 {
		if n, err := s.activeAdminsExcept(tx, username); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(`UPDATE auth_users SET role=?, updated=? WHERE username=?`, int(role), s.now().Unix(), username); err != nil {
		return err
	}
	return tx.Commit()
}

// ListUsers returns all accounts without secrets.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, role, disabled, created FROM auth_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var role, disabled int
		if err := rows.Scan(&u.ID, &u.Username, &role, &disabled, &u.Created); err != nil {
			return nil, err
		}
		u.Role, u.RoleName, u.Disabled = Role(role), Role(role).String(), disabled == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// ── per-IP limiter (in memory) ───────────────────────────────────────────────

func (s *Store) ipBlockedLocked(ip string) bool {
	cut := s.now().Add(-ipWindow)
	list := s.ipFails[ip][:0]
	for _, t := range s.ipFails[ip] {
		if t.After(cut) {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		delete(s.ipFails, ip)
	} else {
		s.ipFails[ip] = list
	}
	return len(list) >= maxIPFailures
}

func (s *Store) ipFailLocked(ip string) {
	if len(s.ipFails) >= maxTrackedIPs {
		// Bounded memory: drop entries older than the window; if still full, drop everything (fail open on
		// the counter, the per-account lock still applies).
		for k := range s.ipFails {
			s.ipBlockedLocked(k)
		}
		if len(s.ipFails) >= maxTrackedIPs {
			s.ipFails = map[string][]time.Time{}
		}
	}
	s.ipFails[ip] = append(s.ipFails[ip], s.now())
}

// ── login, sessions, step-up ─────────────────────────────────────────────────

type userRow struct {
	User
	hash     string
	totpEnc  []byte
	lastStep int64
	failed   int
	locked   int64
}

func (s *Store) loadUser(where string, arg any) (userRow, error) {
	var r userRow
	var role, disabled int
	err := s.db.QueryRow(`SELECT id, username, role, pass_hash, totp_enc, totp_last_step, failed, locked_until, disabled, created FROM auth_users WHERE `+where, arg).
		Scan(&r.ID, &r.Username, &role, &r.hash, &r.totpEnc, &r.lastStep, &r.failed, &r.locked, &disabled, &r.Created)
	r.Role, r.RoleName, r.Disabled = Role(role), Role(role).String(), disabled == 1
	return r, err
}

// checkTOTP verifies a code for a user and consumes its time step atomically (replay-safe even with two
// concurrent requests carrying the same code).
func (s *Store) checkTOTP(r userRow, code string) (bool, error) {
	secret, err := s.seal.open(r.totpEnc, r.Username)
	if err != nil {
		return false, err
	}
	step, ok := verifyTOTP(secret, code, s.now(), r.lastStep)
	if !ok {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE auth_users SET totp_last_step=? WHERE id=? AND totp_last_step<?`, step, r.ID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) recordFailure(r userRow) {
	failed := r.failed + 1
	locked := r.locked
	if failed >= maxAccountFailures {
		failed, locked = 0, s.now().Add(accountLock).Unix()
	}
	if _, err := s.db.Exec(`UPDATE auth_users SET failed=?, locked_until=? WHERE id=?`, failed, locked, r.ID); err != nil {
		log.Printf("auth: cannot record a failed attempt for user id %d: %v", r.ID, err)
	}
}

// Login checks username, password and TOTP code and opens a session. It returns the session token (for
// the cookie) and the session. Every failure returns ErrInvalidCredentials, except the per-IP limit.
func (s *Store) Login(username, password, code, ip string) (string, Session, error) {
	s.mu.Lock()
	blocked := s.ipBlockedLocked(ip)
	s.mu.Unlock()
	if blocked {
		return "", Session{}, ErrTooManyAttempts
	}
	fail := func(r *userRow) (string, Session, error) {
		s.mu.Lock()
		s.ipFailLocked(ip)
		s.mu.Unlock()
		if r != nil {
			s.recordFailure(*r)
		}
		return "", Session{}, ErrInvalidCredentials
	}
	r, err := s.loadUser("username=?", username)
	if errors.Is(err, sql.ErrNoRows) || !ValidUsername(username) {
		VerifyPassword(password, dummyHash) // same cost as a real check
		return fail(nil)
	}
	if err != nil {
		return "", Session{}, err
	}
	okPass, _ := VerifyPassword(password, r.hash)
	if r.Disabled || s.now().Unix() < r.locked || !okPass {
		return fail(&r)
	}
	okCode, err := s.checkTOTP(r, code)
	if err != nil {
		return "", Session{}, err
	}
	if !okCode {
		return fail(&r)
	}
	token, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	now := s.now()
	if _, err := s.db.Exec(`UPDATE auth_users SET failed=0, locked_until=0 WHERE id=?`, r.ID); err != nil {
		return "", Session{}, err
	}
	if _, err := s.db.Exec(`INSERT INTO auth_sessions (id_hash, user_id, csrf, created, last_seen) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), r.ID, csrf, now.Unix(), now.Unix()); err != nil {
		return "", Session{}, err
	}
	return token, Session{User: r.User, CSRF: csrf, Expires: now.Add(SessionIdle), idHash: hashToken(token)}, nil
}

// Session validates a session token: the account must still be enabled, and neither the idle nor the
// absolute limit may be exceeded. The role is read fresh from the account on every call.
func (s *Store) Session(token string) (Session, error) {
	if token == "" || len(token) > 100 {
		return Session{}, ErrNoSession
	}
	id := hashToken(token)
	var r userRow
	var role, disabled int
	var csrf string
	var created, lastSeen int64
	err := s.db.QueryRow(`SELECT u.id, u.username, u.role, u.disabled, u.created, s.csrf, s.created, s.last_seen
		FROM auth_sessions s JOIN auth_users u ON u.id = s.user_id WHERE s.id_hash=?`, id).
		Scan(&r.ID, &r.Username, &role, &disabled, &r.Created, &csrf, &created, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	abs := time.Unix(created, 0).Add(SessionAbsolute)
	idle := time.Unix(lastSeen, 0).Add(SessionIdle)
	if disabled == 1 || !now.Before(abs) || !now.Before(idle) {
		if _, err := s.db.Exec(`DELETE FROM auth_sessions WHERE id_hash=?`, id); err != nil {
			log.Printf("auth: cannot delete an expired session: %v", err)
		}
		return Session{}, ErrNoSession
	}
	if now.Sub(time.Unix(lastSeen, 0)) >= sessionTouch {
		if _, err := s.db.Exec(`UPDATE auth_sessions SET last_seen=? WHERE id_hash=?`, now.Unix(), id); err != nil {
			// Not fatal: the session stays valid until the previous idle deadline.
			log.Printf("auth: cannot refresh a session: %v", err)
		} else {
			idle = now.Add(SessionIdle)
		}
	}
	exp := idle
	if abs.Before(exp) {
		exp = abs
	}
	r.Role, r.RoleName, r.Disabled = Role(role), Role(role).String(), false
	return Session{User: r.User, CSRF: csrf, Expires: exp, idHash: id}, nil
}

// Logout ends a session (no error if it does not exist).
func (s *Store) Logout(token string) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE id_hash=?`, hashToken(token))
	return err
}

// StepUp asks for the TOTP code again before a dangerous action (D138). A wrong code counts as a failure.
func (s *Store) StepUp(sess Session, code, ip string) error {
	s.mu.Lock()
	blocked := s.ipBlockedLocked(ip)
	s.mu.Unlock()
	if blocked {
		return ErrTooManyAttempts
	}
	r, err := s.loadUser("id=?", sess.User.ID)
	if err != nil {
		return ErrNoSession
	}
	ok, err := s.checkTOTP(r, code)
	if err != nil {
		return err
	}
	if !ok || r.Disabled {
		s.mu.Lock()
		s.ipFailLocked(ip)
		s.mu.Unlock()
		s.recordFailure(r)
		return ErrBadCode
	}
	return nil
}

// ChangePassword changes the password of the session user (old password and TOTP code required) and ends
// every other session of that user.
func (s *Store) ChangePassword(sess Session, oldPassword, newPassword, code, ip string) error {
	if err := ValidPassword(newPassword); err != nil {
		return err
	}
	r, err := s.loadUser("id=?", sess.User.ID)
	if err != nil {
		return ErrNoSession
	}
	okPass, _ := VerifyPassword(oldPassword, r.hash)
	if !okPass {
		s.mu.Lock()
		s.ipFailLocked(ip)
		s.mu.Unlock()
		s.recordFailure(r)
		return ErrInvalidCredentials
	}
	if err := s.StepUp(sess, code, ip); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE auth_users SET pass_hash=?, updated=? WHERE id=?`, hash, s.now().Unix(), r.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_sessions WHERE user_id=? AND id_hash<>?`, r.ID, sess.idHash); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckCSRF compares the token sent by the page with the session's, in constant time.
func CheckCSRF(sess Session, sent string) bool {
	return sent != "" && subtle.ConstantTimeCompare([]byte(sent), []byte(sess.CSRF)) == 1
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions() error {
	now := s.now()
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE created<? OR last_seen<?`,
		now.Add(-SessionAbsolute).Unix(), now.Add(-SessionIdle).Unix())
	return err
}
