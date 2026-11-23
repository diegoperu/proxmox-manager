package auth

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AuditRetention: the action log is a security record, kept 2 years (D139).
const AuditRetention = 2 * 365 * 24 * time.Hour

// AuditEntry is one row of the action log. It never holds passwords, codes, keys, tokens or guest content.
type AuditEntry struct {
	ID     int64  `json:"id"`
	Time   int64  `json:"time"`
	User   string `json:"user"`
	IP     string `json:"ip"`
	Action string `json:"action"` // "POST /api/nodes/{node}/qemu/{vmid}/action/{action}" or an event such as "login"
	Target string `json:"target"` // route parameters and cluster, e.g. "cluster=0 node=n1 vmid=101 action=stop"
	Status int    `json:"status"`
	UPID   string `json:"upid"` // Proxmox task id returned by the request, links the row to the Proxmox task log (D144)
	Detail string `json:"detail"`
}

const auditSchema = `
CREATE TABLE IF NOT EXISTS audit_log (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      INTEGER NOT NULL,
  user    TEXT NOT NULL,
  ip      TEXT NOT NULL,
  action  TEXT NOT NULL,
  target  TEXT NOT NULL,
  status  INTEGER NOT NULL,
  upid    TEXT NOT NULL,
  detail  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_log_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS audit_log_upid ON audit_log(upid);
`

// clean keeps printable text only and bounds its length, so a row can be shown as plain text.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AddAudit appends a row (append only: there is no update or delete except the retention prune).
func (s *Store) AddAudit(e AuditEntry) error {
	// Authentication events arrive without a status from the existing audit callback.
	// Store their actual HTTP outcome so failed logins are not displayed or filtered as successes.
	if e.Status == 0 {
		switch e.Action + ":" + e.Detail {
		case "login:riuscito", "logout:riuscito", "setup:primo amministratore creato", "password:cambiata":
			e.Status = 200
		case "login:fallito":
			e.Status = 401
		case "login:limite di tentativi per indirizzo":
			e.Status = 429
		case "setup:codice errato", "password:rifiutata":
			e.Status = 403
		}
	}
	if e.Time == 0 {
		e.Time = s.now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO audit_log (ts, user, ip, action, target, status, upid, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time, clean(e.User, 64), clean(e.IP, 64), clean(e.Action, 200), clean(e.Target, 300), e.Status, clean(e.UPID, 200), clean(e.Detail, 300))
	return err
}

// AuditFilter selects action log rows; dates use the server's local calendar.
type AuditFilter struct {
	Limit                          int
	Before                         int64
	User, Q, Since, Until, Outcome string
}

var ErrAuditFilter = errors.New("filtri del registro non validi")

func auditDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if len(value) != 10 {
		return time.Time{}, ErrAuditFilter
	}
	t, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil || t.Year() < 1 {
		return time.Time{}, ErrAuditFilter
	}
	return t, nil
}

// Validate also runs in the store so callers cannot bypass the HTTP boundary.
func (f AuditFilter) Validate() error {
	if f.Limit < 0 || f.Limit > 500 || f.Before < 0 || (f.User != "" && !ValidUsername(f.User)) ||
		!utf8.ValidString(f.Q) || utf8.RuneCountInString(f.Q) > 64 ||
		(f.Outcome != "" && f.Outcome != "ok" && f.Outcome != "error") {
		return ErrAuditFilter
	}
	for _, r := range f.Q {
		if !unicode.IsPrint(r) {
			return ErrAuditFilter
		}
	}
	since, err := auditDate(f.Since)
	if err != nil {
		return err
	}
	until, err := auditDate(f.Until)
	if err != nil || (f.Since != "" && f.Until != "" && since.After(until)) {
		return ErrAuditFilter
	}
	return nil
}

// ListAudit returns rows newest first; Before is an exclusive id cursor (0 = newest).
func (s *Store) ListAudit(f AuditFilter) ([]AuditEntry, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if f.Limit == 0 {
		f.Limit = 100
	}

	q := `SELECT id, ts, user, ip, action, target, status, upid, detail FROM audit_log WHERE 1=1`
	args := []any{}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.User != "" {
		q += ` AND user = ?`
		args = append(args, f.User)
	}
	if f.Q != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Q) + "%"
		q += ` AND (action LIKE ? ESCAPE '\' OR target LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern)
	}
	if f.Since != "" {
		since, _ := auditDate(f.Since)
		q += ` AND ts >= ?`
		args = append(args, since.Unix())
	}
	if f.Until != "" {
		until, _ := auditDate(f.Until)
		q += ` AND ts < ?`
		args = append(args, until.AddDate(0, 0, 1).Unix())
	}
	if f.Outcome == "ok" {
		q += ` AND status < 400`
	}
	if f.Outcome == "error" {
		q += ` AND status >= 400`
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.IP, &e.Action, &e.Target, &e.Status, &e.UPID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditByUPID maps Proxmox task ids to the manager user that started them (for the activity view, D144).
func (s *Store) AuditByUPID(upids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(upids) == 0 {
		return out, nil
	}
	if len(upids) > 1000 {
		upids = upids[:1000]
	}
	q := `SELECT upid, user FROM audit_log WHERE upid IN (?` + strings.Repeat(",?", len(upids)-1) + `)`
	args := make([]any, len(upids))
	for i, u := range upids {
		args[i] = u
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var upid, user string
		if err := rows.Scan(&upid, &user); err != nil {
			return nil, err
		}
		out[upid] = user
	}
	return out, rows.Err()
}

// PruneAudit deletes rows older than the retention.
func (s *Store) PruneAudit() error {
	_, err := s.db.Exec(`DELETE FROM audit_log WHERE ts < ?`, s.now().Add(-AuditRetention).Unix())
	return err
}
