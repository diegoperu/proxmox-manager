package handlers

import (
	"sort"

	"proxmox-manager/internal/history"
)

type gpuIdleThresholds struct {
	Util, Mem float64
	Days      int
}

func defaultGPUIdleThresholds() gpuIdleThresholds {
	return gpuIdleThresholds{Util: 5, Mem: 5, Days: 14}
}

type gpuUsageRow struct {
	VMID        int      `json:"vmid"`
	Inst        string   `json:"-"` // D121, server side only
	Name        string   `json:"name"`
	Node        string   `json:"node"`
	Group       string   `json:"group"`
	Model       string   `json:"model"`
	Cards       int      `json:"cards"`
	State       string   `json:"state"`
	Days        int      `json:"days"`
	Expected    int      `json:"expected"`
	Valid       int      `json:"valid"`
	Off         int      `json:"off"`
	NoDriver    int      `json:"no_driver"`
	DriverError int      `json:"driver_error"`
	NoData      int      `json:"no_data"`
	Reserved    int      `json:"reserved"`
	Coverage    float64  `json:"coverage"` // fraction, not percent
	UtilAvg     float64  `json:"util_avg"`
	UtilP95Day  float64  `json:"util_p95_day"` // highest daily p95, never a period percentile
	UtilMax     float64  `json:"util_max"`
	MemAvg      float64  `json:"mem_avg"`
	MemMax      float64  `json:"mem_max"`
	BusyHours   float64  `json:"busy_hours"`
	TempMax     *float64 `json:"temp_max"`
	PowerAvg    *float64 `json:"power_avg"`
}

type gpuUsageReport struct {
	Known             bool          `json:"known"`
	Rows              []gpuUsageRow `json:"rows"`
	IdleCount         int           `json:"idle_count"` // cards, not VMs
	ReservedCount     int           `json:"reserved_count"`
	VMOffCount        int           `json:"vm_off_count"`
	InsufficientCount int           `json:"insufficient_count"`
}

func (u *gpuUsageReport) add(row gpuUsageRow) {
	u.Rows = append(u.Rows, row)
	switch row.State {
	case "idle":
		u.IdleCount += row.Cards
	case "reserved":
		u.ReservedCount += row.Cards
	case "vm_off":
		u.VMOffCount += row.Cards
	case "insufficient":
		u.InsufficientCount += row.Cards
	}
}

type gpuUseAccumulator struct {
	row              gpuUsageRow
	latest           string
	days             map[string]bool
	util, mem, power float64
	powerValid       int
}

func (a *gpuUseAccumulator) add(r history.GPUUseDay) {
	a.row.Days++
	if a.days == nil {
		a.days = make(map[string]bool)
	}
	a.days[r.Day] = true
	a.row.VMID, a.row.Inst = r.VMID, r.Inst
	a.row.Expected += r.Expected
	a.row.Valid += r.Valid
	a.row.Off += r.Off
	a.row.NoDriver += r.NoDriver
	a.row.DriverError += r.DriverError
	a.row.NoData += r.NoData
	a.row.Reserved += r.Reserved
	a.row.BusyHours += float64(r.BusyMinutes) / 60
	a.util += r.UtilAvg * float64(r.Valid)
	a.mem += r.MemAvgPct * float64(r.Valid)
	a.row.UtilP95Day = max(a.row.UtilP95Day, r.UtilP95)
	a.row.UtilMax = max(a.row.UtilMax, r.UtilMax)
	a.row.MemMax = max(a.row.MemMax, r.MemMaxPct)
	if r.TempMax >= 0 && (a.row.TempMax == nil || r.TempMax > *a.row.TempMax) {
		t := r.TempMax
		a.row.TempMax = &t
	}
	if r.PowerAvg >= 0 {
		a.power += r.PowerAvg * float64(r.Valid)
		a.powerValid += r.Valid
	}
	if r.Day > a.latest {
		a.latest = r.Day
		a.row.Node, a.row.Group, a.row.Model, a.row.Cards = r.Node, r.Group, r.Model, r.Cards
	}
}

func (a *gpuUseAccumulator) result() gpuUsageRow {
	r := a.row
	if r.Expected > 0 {
		r.Coverage = float64(r.Valid) / float64(r.Expected)
	}
	if r.Valid > 0 {
		r.UtilAvg, r.MemAvg = a.util/float64(r.Valid), a.mem/float64(r.Valid)
	}
	if a.powerValid > 0 {
		p := a.power / float64(a.powerValid)
		r.PowerAvg = &p
	}
	return r
}

// aggregateGPUUsage has no I/O. Metrics cover the selected period; classification uses only the
// final idle_days calendar days, including to. Missing days cannot produce an idle classification.
func aggregateGPUUsage(rows []history.GPUUseDay, p reportParams) gpuUsageReport {
	type acc struct{ period, window gpuUseAccumulator }
	type key struct {
		vmid int
		inst string
	}
	byVM := make(map[key]*acc)
	from, to := p.From.Format("2006-01-02"), p.To.Format("2006-01-02")
	start := p.To.AddDate(0, 0, 1-p.GPUIdle.Days).Format("2006-01-02")
	// Rows written before instance ids existed (inst "") belong to the VM's only known instance; with
	// two or more instances of that VMID they cannot be attributed and stay apart.
	insts := map[int]map[string]bool{}
	for _, row := range rows {
		if row.Inst != "" {
			if insts[row.VMID] == nil {
				insts[row.VMID] = map[string]bool{}
			}
			insts[row.VMID][row.Inst] = true
		}
	}
	for _, row := range rows {
		if row.Day < from || row.Day > to {
			continue
		}
		if row.Inst == "" && len(insts[row.VMID]) == 1 {
			for only := range insts[row.VMID] {
				row.Inst = only
			}
		}
		k := key{row.VMID, row.Inst}
		a := byVM[k]
		if a == nil {
			a = &acc{}
			byVM[k] = a
		}
		a.period.add(row)
		if row.Day >= start {
			a.window.add(row)
		}
	}
	u := gpuUsageReport{Rows: []gpuUsageRow{}}
	for _, a := range byVM {
		r, w := a.period.result(), a.window.result()
		if (p.Nodes != nil && !p.Nodes[r.Node]) || (p.Group != "" && r.Group != p.Group) {
			continue
		}
		switch {
		case w.Off > w.Expected:
			r.State = "vm_off"
		case w.NoDriver > w.Expected/2:
			r.State = "no_driver"
		case w.DriverError > w.Expected/2:
			r.State = "driver_error"
		case w.Coverage < 0.5 || len(a.window.days) < p.GPUIdle.Days:
			r.State = "insufficient"
		case w.UtilAvg < p.GPUIdle.Util:
			if w.MemAvg >= p.GPUIdle.Mem {
				r.State = "reserved"
			} else {
				r.State = "idle"
			}
		default:
			r.State = "in_use"
		}
		u.add(r)
	}
	u.Known = len(u.Rows) > 0
	sort.Slice(u.Rows, func(i, j int) bool { return u.Rows[i].VMID < u.Rows[j].VMID })
	return u
}

func loadGPUUsage(hist *history.Store, cluster string, p reportParams) (gpuUsageReport, error) {
	if hist == nil {
		return aggregateGPUUsage(nil, p), nil
	}
	rows, err := hist.GPUUseDays(cluster, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
	if err != nil {
		return gpuUsageReport{}, err
	}
	return aggregateGPUUsage(rows, p), nil
}

// Manager data deliberately owns a separate type: no names, VMIDs, models or nodes can be marshalled.
type gpuGroupUsage struct {
	Group     string  `json:"group"`
	Others    bool    `json:"others,omitempty"` // distinguishes the tail bucket from a real group named "others"
	Cards     int     `json:"cards"`
	UtilAvg   float64 `json:"util_avg"`
	BusyHours float64 `json:"busy_hours"`
	valid     int
	util      float64
}

func gpuUsageGroups(rows []gpuUsageRow) []gpuGroupUsage {
	groups := make(map[string]*gpuGroupUsage)
	for _, r := range rows {
		g := groups[r.Group]
		if g == nil {
			g = &gpuGroupUsage{Group: r.Group}
			groups[r.Group] = g
		}
		g.Cards += r.Cards
		g.BusyHours += r.BusyHours
		g.util += r.UtilAvg * float64(r.Valid)
		g.valid += r.Valid
	}
	out := make([]gpuGroupUsage, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BusyHours != out[j].BusyHours {
			return out[i].BusyHours > out[j].BusyHours
		}
		return out[i].Group < out[j].Group
	})
	if len(out) > 10 {
		tail := gpuGroupUsage{Group: "others", Others: true}
		for _, g := range out[10:] {
			tail.Cards += g.Cards
			tail.BusyHours += g.BusyHours
			tail.valid += g.valid
			tail.util += g.util
		}
		out = append(out[:10], tail)
	}
	for i := range out {
		if out[i].valid > 0 {
			out[i].UtilAvg = out[i].util / float64(out[i].valid)
		}
	}
	return out
}
