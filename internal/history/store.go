// Package history keeps the daily inventory snapshot (decision D28, 2026-11-03): only counts and assignments per
// node and group (VMs, allocated resources, GPUs). No IP, no VM names, no guest data, no metrics (those stay in
// Proxmox RRD). Rows older than Retention are deleted.
package history

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Retention is how long snapshots are kept.
const Retention = 5 * 365 * 24 * time.Hour

var (
	dayPattern   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	labelPattern = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,64}$`)
)

type NodeRow struct {
	Node     string
	Online   bool
	Cores    int
	MemBytes int64
}

// GroupRow aggregates the VMs/containers of one group on one node; Kind is "qemu" or "lxc".
type GroupRow struct {
	Node      string
	Group     string
	Kind      string
	VMs       int
	Running   int
	Cores     int
	MemBytes  int64
	DiskBytes int64
}

// GPURow: Cards cards of one Model on one node used by Group ("" = free).
type GPURow struct {
	Node  string
	Model string
	Group string
	Cards int
}

// Snapshot is one cluster on one day. GPUKnown is false when the GPU inventory could not be read: no GPU rows are
// then stored, so a missing value is never read as "zero GPUs".
type Snapshot struct {
	Day      string // YYYY-MM-DD, server local time
	Cluster  string // cluster label
	TakenAt  time.Time
	GPUKnown bool
	Nodes    []NodeRow
	Groups   []GroupRow
	GPUs     []GPURow
}

type Store struct{ db *sql.DB }

// New opens the database at path (same file as the API cache: WAL, busy timeout, one connection).
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_timeout=5000&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS inv_meta (
		day TEXT NOT NULL, cluster TEXT NOT NULL, taken_at INTEGER NOT NULL, gpu_known INTEGER NOT NULL,
		PRIMARY KEY (day, cluster));
	CREATE TABLE IF NOT EXISTS inv_node (
		day TEXT NOT NULL, cluster TEXT NOT NULL, node TEXT NOT NULL, online INTEGER NOT NULL, cores INTEGER NOT NULL, mem_bytes INTEGER NOT NULL,
		PRIMARY KEY (day, cluster, node));
	CREATE TABLE IF NOT EXISTS inv_group (
		day TEXT NOT NULL, cluster TEXT NOT NULL, node TEXT NOT NULL, grp TEXT NOT NULL, kind TEXT NOT NULL,
		vms INTEGER NOT NULL, running INTEGER NOT NULL, cores INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, disk_bytes INTEGER NOT NULL,
		PRIMARY KEY (day, cluster, node, grp, kind));
	CREATE TABLE IF NOT EXISTS inv_gpu (
		day TEXT NOT NULL, cluster TEXT NOT NULL, node TEXT NOT NULL, model TEXT NOT NULL, grp TEXT NOT NULL, cards INTEGER NOT NULL,
		PRIMARY KEY (day, cluster, node, model, grp));
	`)
	return err
}

func validate(snap Snapshot) error {
	if !dayPattern.MatchString(snap.Day) {
		return errors.New("history: invalid day")
	}
	if !labelPattern.MatchString(snap.Cluster) {
		return errors.New("history: invalid cluster label")
	}
	return nil
}

// Has reports whether a snapshot exists for the day and cluster.
func (s *Store) Has(day, cluster string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM inv_meta WHERE day=? AND cluster=?`, day, cluster).Scan(&n)
	return n > 0, err
}

// Save replaces the snapshot of (day, cluster) in one transaction.
func (s *Store) Save(snap Snapshot) error {
	if err := validate(snap); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"inv_meta", "inv_node", "inv_group", "inv_gpu"} {
		if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE day=? AND cluster=?`, t), snap.Day, snap.Cluster); err != nil {
			return err
		}
	}
	known := 0
	if snap.GPUKnown {
		known = 1
	}
	if _, err := tx.Exec(`INSERT INTO inv_meta(day,cluster,taken_at,gpu_known) VALUES(?,?,?,?)`, snap.Day, snap.Cluster, snap.TakenAt.Unix(), known); err != nil {
		return err
	}
	for _, n := range snap.Nodes {
		on := 0
		if n.Online {
			on = 1
		}
		if _, err := tx.Exec(`INSERT INTO inv_node(day,cluster,node,online,cores,mem_bytes) VALUES(?,?,?,?,?,?)`, snap.Day, snap.Cluster, n.Node, on, n.Cores, n.MemBytes); err != nil {
			return err
		}
	}
	for _, g := range snap.Groups {
		if _, err := tx.Exec(`INSERT INTO inv_group(day,cluster,node,grp,kind,vms,running,cores,mem_bytes,disk_bytes) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			snap.Day, snap.Cluster, g.Node, g.Group, g.Kind, g.VMs, g.Running, g.Cores, g.MemBytes, g.DiskBytes); err != nil {
			return err
		}
	}
	for _, g := range snap.GPUs {
		if _, err := tx.Exec(`INSERT INTO inv_gpu(day,cluster,node,model,grp,cards) VALUES(?,?,?,?,?,?)`, snap.Day, snap.Cluster, g.Node, g.Model, g.Group, g.Cards); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load returns the snapshot of (day, cluster), or ok=false if there is none.
func (s *Store) Load(day, cluster string) (Snapshot, bool, error) {
	snap := Snapshot{Day: day, Cluster: cluster}
	var taken int64
	var known int
	err := s.db.QueryRow(`SELECT taken_at, gpu_known FROM inv_meta WHERE day=? AND cluster=?`, day, cluster).Scan(&taken, &known)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, err
	}
	snap.TakenAt, snap.GPUKnown = time.Unix(taken, 0), known == 1
	rows, err := s.db.Query(`SELECT node,online,cores,mem_bytes FROM inv_node WHERE day=? AND cluster=? ORDER BY node`, day, cluster)
	if err != nil {
		return snap, false, err
	}
	for rows.Next() {
		var n NodeRow
		var on int
		if err := rows.Scan(&n.Node, &on, &n.Cores, &n.MemBytes); err != nil {
			rows.Close()
			return snap, false, err
		}
		n.Online = on == 1
		snap.Nodes = append(snap.Nodes, n)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT node,grp,kind,vms,running,cores,mem_bytes,disk_bytes FROM inv_group WHERE day=? AND cluster=? ORDER BY node,grp,kind`, day, cluster)
	if err != nil {
		return snap, false, err
	}
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.Node, &g.Group, &g.Kind, &g.VMs, &g.Running, &g.Cores, &g.MemBytes, &g.DiskBytes); err != nil {
			rows.Close()
			return snap, false, err
		}
		snap.Groups = append(snap.Groups, g)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT node,model,grp,cards FROM inv_gpu WHERE day=? AND cluster=? ORDER BY node,model,grp`, day, cluster)
	if err != nil {
		return snap, false, err
	}
	for rows.Next() {
		var g GPURow
		if err := rows.Scan(&g.Node, &g.Model, &g.Group, &g.Cards); err != nil {
			rows.Close()
			return snap, false, err
		}
		snap.GPUs = append(snap.GPUs, g)
	}
	rows.Close()
	return snap, true, nil
}

// Prune deletes every snapshot older than Retention before now.
func (s *Store) Prune(now time.Time) error {
	cutoff := now.Add(-Retention).Format("2006-01-02")
	for _, t := range []string{"inv_meta", "inv_node", "inv_group", "inv_gpu"} {
		if _, err := s.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE day < ?`, t), cutoff); err != nil {
			return err
		}
	}
	return nil
}
