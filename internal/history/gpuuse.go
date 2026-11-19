package history

import (
	"errors"
	"math"
	"regexp"
	"time"
)

// GPU usage (ROADMAP 4, D107-D113): one row per cluster, day and VM with the daily aggregate of the
// nvidia-smi samples taken by the server. Numbers only: no process names, no IP, no VM names (the
// technical report joins the name read from Proxmox when it is generated). Kept GPUUseRetention.

// GPUUseRetention is how long the daily GPU usage rows are kept (D109 A: 13 months).
const GPUUseRetention = 396 * 24 * time.Hour

var (
	gpuNodePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	gpuGroupPattern = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	gpuModelPattern = regexp.MustCompile(`^[\x20-\x7e]{0,80}$`)
)

// GPUUseDay is the aggregate of one VM's samples on one day. Percentages are 0-100.
// Expected counts the samples attempted while the VM was running; Valid those that returned numbers.
// Off counts the cycles in which the VM was stopped (the card was assigned but could not be used).
type GPUUseDay struct {
	Day         string
	Cluster     string
	VMID        int
	Node        string
	Group       string // first group of the VM (D36), "altro" when none
	Model       string // card model as Proxmox names it (not the guest's text)
	Cards       int
	Expected    int
	Valid       int
	Off         int
	NoDriver    int
	DriverError int
	NoData      int     // agent missing, timeout, unparsable output
	BusyMinutes int     // valid samples at work (D113) x sample interval
	UtilAvg     float64 // over valid samples
	UtilP95     float64
	UtilMax     float64
	MemAvgPct   float64
	MemMaxPct   float64
	TempMax     float64 // -1 = unknown
	PowerAvg    float64 // -1 = unknown
}

func (s *Store) migrateGPUUse() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS gpu_use_day (
		day TEXT NOT NULL, cluster TEXT NOT NULL, vmid INTEGER NOT NULL,
		node TEXT NOT NULL, grp TEXT NOT NULL, model TEXT NOT NULL, cards INTEGER NOT NULL,
		expected INTEGER NOT NULL, valid INTEGER NOT NULL, off INTEGER NOT NULL,
		no_driver INTEGER NOT NULL, driver_error INTEGER NOT NULL, no_data INTEGER NOT NULL,
		busy_minutes INTEGER NOT NULL,
		util_avg REAL NOT NULL, util_p95 REAL NOT NULL, util_max REAL NOT NULL,
		mem_avg_pct REAL NOT NULL, mem_max_pct REAL NOT NULL,
		temp_max REAL NOT NULL, power_avg REAL NOT NULL,
		PRIMARY KEY (day, cluster, vmid));
	`)
	return err
}

func finitePct(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100 }

// ValidGPUUse checks a row before it is written (keys and numbers within bounds).
func ValidGPUUse(r GPUUseDay) error {
	switch {
	case !dayPattern.MatchString(r.Day):
		return errors.New("history: invalid day")
	case !labelPattern.MatchString(r.Cluster):
		return errors.New("history: invalid cluster label")
	case r.VMID <= 0 || r.VMID > 999999999:
		return errors.New("history: invalid vmid")
	case !gpuNodePattern.MatchString(r.Node):
		return errors.New("history: invalid node")
	case !gpuGroupPattern.MatchString(r.Group):
		return errors.New("history: invalid group")
	case !gpuModelPattern.MatchString(r.Model):
		return errors.New("history: invalid model")
	case r.Cards < 0 || r.Cards > 64 || r.Expected < 0 || r.Valid < 0 || r.Valid > r.Expected || r.Off < 0 ||
		r.NoDriver < 0 || r.DriverError < 0 || r.NoData < 0 || r.BusyMinutes < 0 || r.BusyMinutes > 24*60:
		return errors.New("history: invalid counts")
	case !finitePct(r.UtilAvg) || !finitePct(r.UtilP95) || !finitePct(r.UtilMax) || !finitePct(r.MemAvgPct) || !finitePct(r.MemMaxPct):
		return errors.New("history: invalid percentage")
	case math.IsNaN(r.TempMax) || math.IsInf(r.TempMax, 0) || r.TempMax < -1 || r.TempMax > 150 ||
		math.IsNaN(r.PowerAvg) || math.IsInf(r.PowerAvg, 0) || r.PowerAvg < -1 || r.PowerAvg > 2000:
		return errors.New("history: invalid temperature or power")
	}
	return nil
}

// UpsertGPUUse writes (insert or replace) the given daily rows in one transaction. A row that does not
// validate aborts the whole write: the caller keeps its accumulators and retries at the next flush.
func (s *Store) UpsertGPUUse(rows []GPUUseDay) error {
	for _, r := range rows {
		if err := ValidGPUUse(r); err != nil {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	st, err := tx.Prepare(`INSERT INTO gpu_use_day (day, cluster, vmid, node, grp, model, cards, expected, valid, off,
		no_driver, driver_error, no_data, busy_minutes, util_avg, util_p95, util_max, mem_avg_pct, mem_max_pct, temp_max, power_avg)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(day, cluster, vmid) DO UPDATE SET node=excluded.node, grp=excluded.grp, model=excluded.model,
		cards=excluded.cards, expected=excluded.expected, valid=excluded.valid, off=excluded.off, no_driver=excluded.no_driver,
		driver_error=excluded.driver_error, no_data=excluded.no_data, busy_minutes=excluded.busy_minutes,
		util_avg=excluded.util_avg, util_p95=excluded.util_p95, util_max=excluded.util_max,
		mem_avg_pct=excluded.mem_avg_pct, mem_max_pct=excluded.mem_max_pct, temp_max=excluded.temp_max, power_avg=excluded.power_avg`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		if _, err := st.Exec(r.Day, r.Cluster, r.VMID, r.Node, r.Group, r.Model, r.Cards, r.Expected, r.Valid, r.Off,
			r.NoDriver, r.DriverError, r.NoData, r.BusyMinutes, r.UtilAvg, r.UtilP95, r.UtilMax, r.MemAvgPct, r.MemMaxPct,
			r.TempMax, r.PowerAvg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GPUUseDays returns the rows of cluster between from and to (inclusive, YYYY-MM-DD), by day then VMID.
func (s *Store) GPUUseDays(cluster, from, to string) ([]GPUUseDay, error) {
	if !dayPattern.MatchString(from) || !dayPattern.MatchString(to) || !labelPattern.MatchString(cluster) {
		return nil, errors.New("history: invalid query")
	}
	rows, err := s.db.Query(`SELECT day, cluster, vmid, node, grp, model, cards, expected, valid, off, no_driver, driver_error,
		no_data, busy_minutes, util_avg, util_p95, util_max, mem_avg_pct, mem_max_pct, temp_max, power_avg
		FROM gpu_use_day WHERE cluster=? AND day>=? AND day<=? ORDER BY day, vmid`, cluster, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GPUUseDay{}
	for rows.Next() {
		var r GPUUseDay
		if err := rows.Scan(&r.Day, &r.Cluster, &r.VMID, &r.Node, &r.Group, &r.Model, &r.Cards, &r.Expected, &r.Valid, &r.Off,
			&r.NoDriver, &r.DriverError, &r.NoData, &r.BusyMinutes, &r.UtilAvg, &r.UtilP95, &r.UtilMax, &r.MemAvgPct,
			&r.MemMaxPct, &r.TempMax, &r.PowerAvg); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneGPUUse deletes the rows older than GPUUseRetention before now.
func (s *Store) PruneGPUUse(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM gpu_use_day WHERE day < ?`, now.Add(-GPUUseRetention).Format("2006-01-02"))
	return err
}
