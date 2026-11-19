package history

import (
	"database/sql"
	"errors"
	"math"
	"regexp"
	"time"
)

// Daily VM usage (ROADMAP 21, D115-D121): one row per cluster, day, VMID and instance with the day's
// aggregates computed from Proxmox's RRD (30-minute points: exact average, exact peak from the MAX
// consolidation, p95 of the 30-minute averages). VMIDs are reused after a VM is deleted, so the key carries
// an instance id (D121: the VM's smbios1 uuid; LXC: a new instance after a gap or a rename). The VM name is
// stored (D119 A), the IP never. Kept VMUseRetention.

// VMUseRetention is how long the daily VM usage rows are kept (D119: 13 months).
const VMUseRetention = 396 * 24 * time.Hour

var (
	// InstPattern: "u:<uuid>" for QEMU VMs, "l:<YYYY-MM-DD>" for LXC (first day of the instance).
	InstPattern   = regexp.MustCompile(`^(u:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|l:[0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	vmNamePattern = regexp.MustCompile(`^[\x20-\x7e]{0,64}$`)
)

// VMUseDay is one VM-instance-day. Percentages are 0-100 of the allocated resource; -1 = unknown (no
// sample: the VM was off or not readable). Byte counters are totals over the covered part of the day.
type VMUseDay struct {
	Day       string
	Cluster   string
	VMID      int
	Inst      string
	Kind      string // qemu | lxc
	Name      string
	Group     string
	Node      string
	Cores     int
	MemBytes  int64
	DiskBytes int64
	Points    int // 30-minute points with data (48 = the VM ran all day)
	StepS     int // RRD step used (1800)
	CPUAvg    float64
	CPUP95    float64
	CPUMax    float64
	MemAvg    float64
	MemP95    float64
	MemMax    float64
	NetIn     int64
	NetOut    int64
	DiskRead  int64
	DiskWrite int64
}

func (s *Store) migrateVMUse() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS vm_use_day (
		day TEXT NOT NULL, cluster TEXT NOT NULL, vmid INTEGER NOT NULL, inst TEXT NOT NULL,
		kind TEXT NOT NULL, name TEXT NOT NULL, grp TEXT NOT NULL, node TEXT NOT NULL,
		cores INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, disk_bytes INTEGER NOT NULL,
		points INTEGER NOT NULL, step_s INTEGER NOT NULL,
		cpu_avg REAL NOT NULL, cpu_p95 REAL NOT NULL, cpu_max REAL NOT NULL,
		mem_avg REAL NOT NULL, mem_p95 REAL NOT NULL, mem_max REAL NOT NULL,
		net_in INTEGER NOT NULL, net_out INTEGER NOT NULL, disk_read INTEGER NOT NULL, disk_write INTEGER NOT NULL,
		PRIMARY KEY (day, cluster, vmid, inst));
	CREATE INDEX IF NOT EXISTS vm_use_day_vm ON vm_use_day (cluster, vmid, day);
	CREATE TABLE IF NOT EXISTS vm_use_meta (
		day TEXT NOT NULL, cluster TEXT NOT NULL, taken_at INTEGER NOT NULL, vms INTEGER NOT NULL,
		PRIMARY KEY (day, cluster));
	`)
	return err
}

func pctOrUnknown(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && (v == -1 || (v >= 0 && v <= 100))
}

// ValidVMUse checks a row before it is written.
func ValidVMUse(r VMUseDay) error {
	switch {
	case !dayPattern.MatchString(r.Day):
		return errors.New("history: invalid day")
	case !labelPattern.MatchString(r.Cluster):
		return errors.New("history: invalid cluster label")
	case r.VMID <= 0 || r.VMID > 999999999:
		return errors.New("history: invalid vmid")
	case !InstPattern.MatchString(r.Inst):
		return errors.New("history: invalid instance")
	case r.Kind != "qemu" && r.Kind != "lxc":
		return errors.New("history: invalid kind")
	case !vmNamePattern.MatchString(r.Name):
		return errors.New("history: invalid name")
	case !gpuGroupPattern.MatchString(r.Group):
		return errors.New("history: invalid group")
	case !gpuNodePattern.MatchString(r.Node):
		return errors.New("history: invalid node")
	case r.Cores < 0 || r.Cores > 4096 || r.MemBytes < 0 || r.DiskBytes < 0 || r.Points < 0 || r.Points > 48*31 || r.StepS <= 0 || r.StepS > 86400:
		return errors.New("history: invalid counts")
	case !pctOrUnknown(r.CPUAvg) || !pctOrUnknown(r.CPUP95) || !pctOrUnknown(r.CPUMax) ||
		!pctOrUnknown(r.MemAvg) || !pctOrUnknown(r.MemP95) || !pctOrUnknown(r.MemMax):
		return errors.New("history: invalid percentage")
	case r.NetIn < 0 || r.NetOut < 0 || r.DiskRead < 0 || r.DiskWrite < 0:
		return errors.New("history: invalid byte counter")
	}
	return nil
}

// SaveVMUseDay replaces the rows of (day, cluster) and marks the day as taken, in one transaction:
// a day is either complete or absent, so a failed collection is simply retried.
func (s *Store) SaveVMUseDay(day, cluster string, rows []VMUseDay, takenAt time.Time) error {
	if !dayPattern.MatchString(day) || !labelPattern.MatchString(cluster) {
		return errors.New("history: invalid day or cluster")
	}
	for _, r := range rows {
		if r.Day != day || r.Cluster != cluster {
			return errors.New("history: row of another day or cluster")
		}
		if err := ValidVMUse(r); err != nil {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(`DELETE FROM vm_use_day WHERE day=? AND cluster=?`, day, cluster); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO vm_use_day (day, cluster, vmid, inst, kind, name, grp, node, cores, mem_bytes, disk_bytes,
		points, step_s, cpu_avg, cpu_p95, cpu_max, mem_avg, mem_p95, mem_max, net_in, net_out, disk_read, disk_write)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(r.Day, r.Cluster, r.VMID, r.Inst, r.Kind, r.Name, r.Group, r.Node, r.Cores, r.MemBytes, r.DiskBytes,
			r.Points, r.StepS, r.CPUAvg, r.CPUP95, r.CPUMax, r.MemAvg, r.MemP95, r.MemMax, r.NetIn, r.NetOut, r.DiskRead, r.DiskWrite); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO vm_use_meta (day, cluster, taken_at, vms) VALUES (?,?,?,?)`,
		day, cluster, takenAt.Unix(), len(rows)); err != nil {
		return err
	}
	return tx.Commit()
}

// HasVMUseDay reports whether the day was already collected for the cluster.
func (s *Store) HasVMUseDay(day, cluster string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM vm_use_meta WHERE day=? AND cluster=?`, day, cluster).Scan(&n)
	return n > 0, err
}

// VMUseLastBefore returns the latest row of (cluster, vmid, kind) strictly before day, if any:
// the collector uses it to keep an LXC instance across days (D121).
func (s *Store) VMUseLastBefore(cluster string, vmid int, kind, day string) (VMUseDay, bool, error) {
	var r VMUseDay
	err := s.db.QueryRow(`SELECT day, inst, name FROM vm_use_day WHERE cluster=? AND vmid=? AND kind=? AND day<? ORDER BY day DESC LIMIT 1`,
		cluster, vmid, kind, day).Scan(&r.Day, &r.Inst, &r.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// VMUseDays returns the rows of cluster between from and to (inclusive), by day, VMID, instance.
func (s *Store) VMUseDays(cluster, from, to string) ([]VMUseDay, error) {
	if !dayPattern.MatchString(from) || !dayPattern.MatchString(to) || !labelPattern.MatchString(cluster) {
		return nil, errors.New("history: invalid query")
	}
	rows, err := s.db.Query(`SELECT day, cluster, vmid, inst, kind, name, grp, node, cores, mem_bytes, disk_bytes, points, step_s,
		cpu_avg, cpu_p95, cpu_max, mem_avg, mem_p95, mem_max, net_in, net_out, disk_read, disk_write
		FROM vm_use_day WHERE cluster=? AND day>=? AND day<=? ORDER BY day, vmid, inst`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VMUseDay{}
	for rows.Next() {
		var r VMUseDay
		if err := rows.Scan(&r.Day, &r.Cluster, &r.VMID, &r.Inst, &r.Kind, &r.Name, &r.Group, &r.Node, &r.Cores, &r.MemBytes,
			&r.DiskBytes, &r.Points, &r.StepS, &r.CPUAvg, &r.CPUP95, &r.CPUMax, &r.MemAvg, &r.MemP95, &r.MemMax, &r.NetIn,
			&r.NetOut, &r.DiskRead, &r.DiskWrite); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VMUseFirstDay returns the first collected day of the cluster ("" if none): reports say from when
// the stored data exists and use the RRD before it.
func (s *Store) VMUseFirstDay(cluster string) (string, error) {
	var d sql.NullString
	err := s.db.QueryRow(`SELECT MIN(day) FROM vm_use_meta WHERE cluster=?`, cluster).Scan(&d)
	return d.String, err
}

// PruneVMUse deletes rows and day marks older than VMUseRetention before now.
func (s *Store) PruneVMUse(now time.Time) error {
	cutoff := now.Add(-VMUseRetention).Format("2006-01-02")
	if _, err := s.db.Exec(`DELETE FROM vm_use_day WHERE day < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM vm_use_meta WHERE day < ?`, cutoff)
	return err
}
