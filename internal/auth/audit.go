package auth

import (
	"strings"
	"time"
	"unicode"
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
	if e.Time == 0 {
		e.Time = s.now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO audit_log (ts, user, ip, action, target, status, upid, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time, clean(e.User, 64), clean(e.IP, 64), clean(e.Action, 200), clean(e.Target, 300), e.Status, clean(e.UPID, 200), clean(e.Detail, 300))
	return err
}

// ListAudit returns rows newest first: before = id to page backwards (0 = newest), user = exact filter.
func (s *Store) ListAudit(limit int, before int64, user string) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, ts, user, ip, action, target, status, upid, detail FROM audit_log WHERE 1=1`
	args := []any{}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	if user != "" {
		q += ` AND user = ?`
		args = append(args, user)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
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
