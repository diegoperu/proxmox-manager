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
//
// A VM that belongs to several groups (D37/D42) is counted whole in VMs/Running of each group (and in SharedVMs), while
// its resources are split: bytes with the remainder to the first group, cores in thousandths (CoresMilli). Cores is the
// whole-core view of CoresMilli. Summing VMs over groups can exceed the cluster total: use Snapshot.TotalVMs.
type GroupRow struct {
	Node       string
	Group      string
	Kind       string
	VMs        int
	Running    int
	SharedVMs  int
	Cores      int
	CoresMilli int // 0 = derive from Cores
	MemBytes   int64
	DiskBytes  int64
}

func (g GroupRow) milli() int64 {
	if g.CoresMilli != 0 {
		return int64(g.CoresMilli)
	}
	return int64(g.Cores) * 1000
}

// GPURow: Cards cards of one Model on one node used by Group ("" = free).
// CardsMilli is the share in thousandths (a card used by a VM of two groups counts 500 for each); 0 = derive from Cards.
type GPURow struct {
	Node       string
	Model      string
	Group      string
	Cards      int
	CardsMilli int
}

func (g GPURow) milli() int64 {
	if g.CardsMilli != 0 {
		return int64(g.CardsMilli)
	}
	return int64(g.Cards) * 1000
}

// GroupRule says how groups were derived: "name" (D27, prefix of the VM name; all snapshots before the tag rule) or
// "tags" (D35-D37, `gruppo-<name>` tags with fallback on the name). D41: old snapshots are never recomputed.
const (
	RuleName = "name"
	RuleTags = "tags"
)

// Snapshot is one cluster on one day. GPUKnown is false when the GPU inventory could not be read: no GPU rows are
// then stored, so a missing value is never read as "zero GPUs".
type Snapshot struct {
	Day      string // YYYY-MM-DD, server local time
	Cluster  string // cluster label
	TakenAt  time.Time
	GPUKnown bool
	// GroupRule is RuleName or RuleTags ("" is saved as RuleName).
	GroupRule string
	// Totals of the cluster (distinct VMs, templates excluded), valid when HasTotals: with shared VMs the sum over the
	// group rows would count them more than once.
	HasTotals    bool
	TotalVMs     int
	TotalRunning int
	SharedVMs    int // distinct VMs that belong to more than one group
	Nodes        []NodeRow
	Groups       []GroupRow
	GPUs         []GPURow
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
	if err != nil {
		return err
	}
	// Additive migration for the tag rule (D35-D42): old rows keep their meaning (rule "name", whole cores/cards).
	for _, c := range []struct{ table, col, ddl string }{
		{"inv_meta", "group_rule", "TEXT NOT NULL DEFAULT 'name'"},
		{"inv_meta", "totals_known", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_meta", "total_vms", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_meta", "total_running", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_meta", "shared_vms", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_group", "shared_vms", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_group", "cores_milli", "INTEGER NOT NULL DEFAULT 0"},
		{"inv_gpu", "cards_milli", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.addColumn(c.table, c.col, c.ddl); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`UPDATE inv_group SET cores_milli=cores*1000 WHERE cores_milli=0 AND cores<>0`); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE inv_gpu SET cards_milli=cards*1000 WHERE cards_milli=0 AND cards<>0`)
	return err
}

// addColumn adds a column if missing. table, col and ddl are constants of this package, never request data.
func (s *Store) addColumn(table, col, ddl string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == col {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, ddl))
	return err
}

// ValidDay reports whether s is a YYYY-MM-DD string.
func ValidDay(s string) bool { return dayPattern.MatchString(s) }

func validate(snap Snapshot) error {
	if !dayPattern.MatchString(snap.Day) {
		return errors.New("history: invalid day")
	}
	if !labelPattern.MatchString(snap.Cluster) {
		return errors.New("history: invalid cluster label")
	}
	if snap.GroupRule != "" && snap.GroupRule != RuleName && snap.GroupRule != RuleTags {
		return errors.New("history: invalid group rule")
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
	rule := snap.GroupRule
	if rule == "" {
		rule = RuleName
	}
	totals := 0
	if snap.HasTotals {
		totals = 1
	}
	if _, err := tx.Exec(`INSERT INTO inv_meta(day,cluster,taken_at,gpu_known,group_rule,totals_known,total_vms,total_running,shared_vms) VALUES(?,?,?,?,?,?,?,?,?)`,
		snap.Day, snap.Cluster, snap.TakenAt.Unix(), known, rule, totals, snap.TotalVMs, snap.TotalRunning, snap.SharedVMs); err != nil {
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
		m := g.milli()
		if _, err := tx.Exec(`INSERT INTO inv_group(day,cluster,node,grp,kind,vms,running,shared_vms,cores,cores_milli,mem_bytes,disk_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			snap.Day, snap.Cluster, g.Node, g.Group, g.Kind, g.VMs, g.Running, g.SharedVMs, m/1000, m, g.MemBytes, g.DiskBytes); err != nil {
			return err
		}
	}
	for _, g := range snap.GPUs {
		m := g.milli()
		if _, err := tx.Exec(`INSERT INTO inv_gpu(day,cluster,node,model,grp,cards,cards_milli) VALUES(?,?,?,?,?,?,?)`, snap.Day, snap.Cluster, g.Node, g.Model, g.Group, m/1000, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load returns the snapshot of (day, cluster), or ok=false if there is none.
func (s *Store) Load(day, cluster string) (Snapshot, bool, error) {
	snap := Snapshot{Day: day, Cluster: cluster}
	var taken int64
	var known, totals int
	err := s.db.QueryRow(`SELECT taken_at, gpu_known, group_rule, totals_known, total_vms, total_running, shared_vms FROM inv_meta WHERE day=? AND cluster=?`, day, cluster).
		Scan(&taken, &known, &snap.GroupRule, &totals, &snap.TotalVMs, &snap.TotalRunning, &snap.SharedVMs)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, err
	}
	snap.TakenAt, snap.GPUKnown, snap.HasTotals = time.Unix(taken, 0), known == 1, totals == 1
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
	rows, err = s.db.Query(`SELECT node,grp,kind,vms,running,shared_vms,cores_milli,mem_bytes,disk_bytes FROM inv_group WHERE day=? AND cluster=? ORDER BY node,grp,kind`, day, cluster)
	if err != nil {
		return snap, false, err
	}
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.Node, &g.Group, &g.Kind, &g.VMs, &g.Running, &g.SharedVMs, &g.CoresMilli, &g.MemBytes, &g.DiskBytes); err != nil {
			rows.Close()
			return snap, false, err
		}
		g.Cores = g.CoresMilli / 1000
		snap.Groups = append(snap.Groups, g)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT node,model,grp,cards_milli FROM inv_gpu WHERE day=? AND cluster=? ORDER BY node,model,grp`, day, cluster)
	if err != nil {
		return snap, false, err
	}
	for rows.Next() {
		var g GPURow
		if err := rows.Scan(&g.Node, &g.Model, &g.Group, &g.CardsMilli); err != nil {
			rows.Close()
			return snap, false, err
		}
		g.Cards = g.CardsMilli / 1000
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

// DayTotals are the per-day sums of a cluster's snapshot (for trends in the reports).
type DayTotals struct {
	Day         string
	VMs         int
	Running     int
	Groups      int // distinct groups, "altro" excluded
	AllocCores  int64
	AllocMem    int64
	NodeCores   int64
	NodeMem     int64
	GPUKnown    bool
	GPUTotal    int
	GPUAssigned int
	SharedVMs   int    // distinct VMs in more than one group (0 for snapshots before the tag rule)
	GroupRule   string // RuleName | RuleTags
}

// Days returns the daily totals of cluster between from and to (inclusive, YYYY-MM-DD), oldest first.
func (s *Store) Days(cluster, from, to string) ([]DayTotals, error) {
	if !dayPattern.MatchString(from) || !dayPattern.MatchString(to) || !labelPattern.MatchString(cluster) {
		return nil, errors.New("history: invalid arguments")
	}
	out := []DayTotals{}
	idx := map[string]int{}
	rows, err := s.db.Query(`SELECT day, gpu_known, group_rule, totals_known, total_vms, total_running, shared_vms FROM inv_meta WHERE cluster=? AND day>=? AND day<=? ORDER BY day`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	hasTotals := map[string]bool{}
	for rows.Next() {
		var d DayTotals
		var k, tk, tv, tr, sh int
		if err := rows.Scan(&d.Day, &k, &d.GroupRule, &tk, &tv, &tr, &sh); err != nil {
			rows.Close()
			return nil, err
		}
		d.GPUKnown = k == 1
		if tk == 1 { // cluster totals stored: the sum over groups would double count shared VMs
			hasTotals[d.Day] = true
			d.VMs, d.Running, d.SharedVMs = tv, tr, sh
		}
		idx[d.Day] = len(out)
		out = append(out, d)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT day, SUM(vms), SUM(running), SUM(cores_milli), SUM(mem_bytes), COUNT(DISTINCT CASE WHEN grp<>'altro' THEN grp END)
		FROM inv_group WHERE cluster=? AND day>=? AND day<=? GROUP BY day`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day string
		var vms, run, cores, mem, groups int64
		if err := rows.Scan(&day, &vms, &run, &cores, &mem, &groups); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[day]; ok {
			if !hasTotals[day] {
				out[i].VMs, out[i].Running = int(vms), int(run)
			}
			out[i].AllocCores, out[i].AllocMem, out[i].Groups = cores/1000, mem, int(groups)
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT day, SUM(online*cores), SUM(online*mem_bytes) FROM inv_node WHERE cluster=? AND day>=? AND day<=? GROUP BY day`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day string
		var cores, mem int64
		if err := rows.Scan(&day, &cores, &mem); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[day]; ok {
			out[i].NodeCores, out[i].NodeMem = cores, mem
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT day, SUM(cards_milli), SUM(CASE WHEN grp<>'' THEN cards_milli ELSE 0 END) FROM inv_gpu WHERE cluster=? AND day>=? AND day<=? GROUP BY day`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day string
		var total, assigned int64
		if err := rows.Scan(&day, &total, &assigned); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[day]; ok {
			out[i].GPUTotal, out[i].GPUAssigned = int((total+500)/1000), int((assigned+500)/1000)
		}
	}
	rows.Close()
	return out, nil
}
