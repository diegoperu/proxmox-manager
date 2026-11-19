package handlers

import (
	"math"
	"time"

	"proxmox-manager/internal/history"
)

type trendThresholds struct {
	AbandonCPU    float64 `json:"abandon_cpu"`
	AbandonNet    float64 `json:"abandon_net"`
	AbandonMonths int     `json:"abandon_months"`
	OverMem       float64 `json:"over_mem"`
	OverCPU       float64 `json:"over_cpu"`
	Margin        float64 `json:"margin"`
}

func defaultTrendThresholds() trendThresholds {
	return trendThresholds{AbandonCPU: 2, AbandonNet: 1024, AbandonMonths: 3, OverMem: 25, OverCPU: 10, Margin: 25}
}

// Calendar arithmetic retains local midnight across DST and short months.
func trendMonthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func trendMonths(from, to time.Time) []string {
	out := []string{}
	for t := trendMonthStart(from); !t.After(to); t = t.AddDate(0, 1, 0) {
		out = append(out, t.Format("2006-01"))
	}
	return out
}

type trendMonth struct {
	Month               string   `json:"month"`
	Source              string   `json:"source"`
	Days                *float64 `json:"days"`
	DaysOff             *float64 `json:"days_off"`
	CPUAvg              *float64 `json:"cpu_avg"`
	CPUMax              *float64 `json:"cpu_max"`
	MemAvg              *float64 `json:"mem_avg"`
	MemMax              *float64 `json:"mem_max"`
	NetBytes            *float64 `json:"net_bytes"`
	DiskBytes           *float64 `json:"disk_bytes"`
	netSeconds, peakMem float64
	data                bool
}

type trendProposal struct {
	Cores    int64 `json:"cores"`
	MemBytes int64 `json:"mem_bytes"`
}

type trendRow struct {
	VMID            int            `json:"vmid"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	Node            string         `json:"node"`
	Group           string         `json:"group"`
	Status          string         `json:"status"`
	Cores           int64          `json:"cores"`
	MemBytes        int64          `json:"mem_bytes"`
	State           string         `json:"state"`
	OversizedMem    bool           `json:"oversized_mem"`
	OversizedCPU    bool           `json:"oversized_cpu"`
	LastActiveMonth *string        `json:"last_active_month"`
	Proposal        *trendProposal `json:"proposal"`
	Months          []trendMonth   `json:"months"`
}

func trendNumber(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	return &v
}

func trendAdd(dst **float64, v float64) {
	if trendNumber(v) == nil {
		return
	}
	if *dst == nil {
		*dst = trendNumber(v)
	} else {
		**dst += v
	}
}

func trendMax(dst **float64, v float64) {
	if trendNumber(v) != nil && (*dst == nil || v > **dst) {
		*dst = trendNumber(v)
	}
}

type trendMonthAccumulator struct {
	month                                trendMonth
	cpuSum, cpuWeight, memSum, memWeight float64
	stored, rrd                          bool
}

func (a *trendMonthAccumulator) average(cpu, mem, weight float64) {
	if trendNumber(cpu) != nil {
		a.cpuSum += cpu * weight
		a.cpuWeight += weight
	}
	if trendNumber(mem) != nil {
		a.memSum += mem * weight
		a.memWeight += weight
	}
}

func (a *trendMonthAccumulator) storedDay(r history.VMUseDay) {
	a.stored = true
	if a.month.Days == nil {
		a.month.Days = trendNumber(0)
		a.month.DaysOff = trendNumber(0)
	}
	if r.Points == 0 {
		*a.month.DaysOff++
		return
	}
	*a.month.Days++
	a.month.data = true
	seconds := float64(r.Points) * float64(r.StepS)
	a.average(r.CPUAvg, r.MemAvg, float64(r.Points))
	trendMax(&a.month.CPUMax, r.CPUMax)
	trendMax(&a.month.MemMax, r.MemMax)
	if r.MemMax >= 0 {
		a.month.peakMem = math.Max(a.month.peakMem, r.MemMax/100*float64(r.MemBytes))
	}
	trendAdd(&a.month.NetBytes, float64(r.NetIn)+float64(r.NetOut))
	a.month.netSeconds += seconds
	trendAdd(&a.month.DiskBytes, float64(r.DiskRead)+float64(r.DiskWrite))
}

func trendValue(s rrdSeries, col string, i int) float64 {
	if i >= len(s.Cols[col]) {
		return math.NaN()
	}
	v := s.Cols[col][i]
	// A single guest cannot reasonably exceed an int64 byte capacity/rate. This also
	// bounds multiplication and sums when malformed remote metrics are received.
	if v < 0 || v > float64(math.MaxInt64) || math.IsInf(v, 0) {
		return math.NaN()
	}
	return v
}

// Each point belongs to its timestamp's month. Its step follows the mixed year resolution,
// measured before clipping; backwards/duplicate timestamps never add coverage twice.
func trendRRDStep(s rrdSeries, i int, previous int64) int64 {
	if previous >= 0 && s.T[i] > previous {
		return min(604800, s.T[i]-previous)
	}
	if i == 0 && len(s.T) > 1 && s.T[1] > s.T[0] {
		return min(604800, s.T[1]-s.T[0])
	}
	if s.Step > 0 {
		return min(604800, s.Step)
	}
	return 21600
}

// mergeTrendMonths is pure. Stored rows weight averages by points; year samples weight by
// duration in equivalent 30-minute points. MAX is independent of AVERAGE, never a monthly p95.
func mergeTrendMonths(rows []history.VMUseDay, avg, mx rrdSeries, storedFrom string, p reportParams) []trendMonth {
	months := trendMonths(p.From, p.To)
	acc := make(map[string]*trendMonthAccumulator, len(months))
	for _, m := range months {
		acc[m] = &trendMonthAccumulator{month: trendMonth{Month: m}}
	}
	for _, r := range rows {
		if len(r.Day) != 10 || r.Day < p.From.Format("2006-01-02") || r.Day > p.To.Format("2006-01-02") {
			continue
		}
		if a := acc[r.Day[:7]]; a != nil {
			a.storedDay(r)
		}
	}
	accept := func(ts int64) *trendMonthAccumulator {
		t := time.Unix(ts, 0).In(p.From.Location())
		if t.Before(p.From) || t.After(p.To) || (storedFrom != "" && t.Format("2006-01-02") >= storedFrom) {
			return nil
		}
		return acc[t.Format("2006-01")]
	}
	last := int64(-1)
	for i, ts := range avg.T {
		if ts <= last {
			continue
		}
		seconds := float64(trendRRDStep(avg, i, last))
		last = ts
		a := accept(ts)
		if a == nil {
			continue
		}
		cpu := trendValue(avg, "cpu", i)
		mu, capacity := trendValue(avg, "mem", i), trendValue(avg, "maxmem", i)
		mem := math.NaN()
		if capacity > 0 {
			mem = clampPct(mu / capacity * 100)
		}
		ni, no := trendValue(avg, "netin", i), trendValue(avg, "netout", i)
		dr, dw := trendValue(avg, "diskread", i), trendValue(avg, "diskwrite", i)
		if math.IsNaN(cpu) && math.IsNaN(mem) && (math.IsNaN(ni) || math.IsNaN(no)) && (math.IsNaN(dr) || math.IsNaN(dw)) {
			continue
		}
		a.rrd, a.month.data = true, true
		trendAdd(&a.month.Days, seconds/86400)
		a.average(clampPct(cpu*100), mem, seconds/vmUseStepS)
		if !math.IsNaN(ni) && !math.IsNaN(no) {
			trendAdd(&a.month.NetBytes, (ni+no)*seconds)
			a.month.netSeconds += seconds
		}
		if !math.IsNaN(dr) && !math.IsNaN(dw) {
			trendAdd(&a.month.DiskBytes, (dr+dw)*seconds)
		}
	}
	last = -1
	for i, ts := range mx.T {
		if ts <= last {
			continue
		}
		last = ts
		a := accept(ts)
		if a == nil {
			continue
		}
		cpu := trendValue(mx, "cpu", i)
		if !math.IsNaN(cpu) {
			a.rrd, a.month.data = true, true
			trendMax(&a.month.CPUMax, clampPct(cpu*100))
		}
		mu, capacity := trendValue(mx, "mem", i), trendValue(mx, "maxmem", i)
		if !math.IsNaN(mu) && capacity > 0 {
			a.rrd, a.month.data = true, true
			trendMax(&a.month.MemMax, clampPct(mu/capacity*100))
			a.month.peakMem = math.Max(a.month.peakMem, mu)
		}
	}
	out := make([]trendMonth, 0, len(months))
	for _, m := range months {
		a := acc[m]
		if a.cpuWeight > 0 {
			a.month.CPUAvg = trendNumber(a.cpuSum / a.cpuWeight)
		}
		if a.memWeight > 0 {
			a.month.MemAvg = trendNumber(a.memSum / a.memWeight)
		}
		switch {
		case a.stored && a.rrd:
			a.month.Source = "mixed"
		case a.stored:
			a.month.Source = "stored"
		case a.rrd:
			a.month.Source = "rrd"
		}
		out = append(out, a.month)
	}
	return out
}

func classifyTrend(row *trendRow, th trendThresholds) {
	row.State, row.OversizedMem, row.OversizedCPU = "", false, false
	row.LastActiveMonth, row.Proposal = nil, nil
	dataMonths := 0
	memSmall, cpuSmall := true, true
	var peakMem, peakCPU float64
	active := make([]bool, len(row.Months))
	for i, m := range row.Months {
		active[i] = m.data && ((m.CPUAvg != nil && *m.CPUAvg >= th.AbandonCPU) || (m.NetBytes != nil && m.netSeconds > 0 && *m.NetBytes/m.netSeconds >= th.AbandonNet))
		if active[i] {
			month := m.Month
			row.LastActiveMonth = &month
		}
		if !m.data {
			continue
		}
		dataMonths++
		memSmall = memSmall && m.MemMax != nil && *m.MemMax < th.OverMem
		cpuSmall = cpuSmall && m.CPUMax != nil && *m.CPUMax < th.OverCPU
		peakMem = math.Max(peakMem, m.peakMem)
		if m.CPUMax != nil {
			peakCPU = math.Max(peakCPU, *m.CPUMax)
		}
	}
	if row.Status == "deleted" {
		row.State = "deleted"
		return
	}
	window := len(row.Months) - th.AbandonMonths
	off, abandoned := window >= 0, window >= 0
	for i := max(0, window); i < len(row.Months); i++ {
		m := row.Months[i]
		off = off && !m.data
		abandoned = abandoned && m.data && !active[i] && m.CPUAvg != nil && m.NetBytes != nil && m.netSeconds > 0
	}
	switch {
	case off:
		row.State = "off_long"
	case abandoned:
		row.State = "abandoned"
	// abandon_months is what "abandoned" needs; two months with data are enough to call a VM oversized
	// (a new VM sized far beyond its use is worth flagging early). Below two months nothing can be judged.
	case dataMonths >= 2 && (memSmall || cpuSmall):
		row.State = "oversized"
		row.OversizedMem, row.OversizedCPU = memSmall, cpuSmall
		row.Proposal = proposeTrend(row.Cores, row.MemBytes, peakCPU, peakMem, memSmall, cpuSmall, th.Margin)
	case dataMonths < 2:
		row.State = "insufficient"
	default:
		row.State = "in_use"
	}
}

func proposeTrend(cores, mem int64, peakCPU, peakMem float64, memSmall, cpuSmall bool, margin float64) *trendProposal {
	proposal := &trendProposal{Cores: cores, MemBytes: mem}
	factor := 1 + margin/100
	if memSmall && mem > 0 {
		const unit = int64(2) << 30
		target := math.Max(float64(unit), math.Ceil(math.Ceil(peakMem*factor)/float64(unit))*float64(unit))
		if target < float64(mem) {
			proposal.MemBytes = int64(target)
		}
	}
	if cpuSmall && cores > 0 {
		target := math.Max(1, math.Ceil(peakCPU*float64(cores)/100*factor))
		if target < float64(cores) {
			proposal.Cores = int64(target)
		}
	}
	return proposal
}

// This manager type cannot marshal guest identities or technical rows (D30).
type vmTrendSummary struct {
	Abandoned    int   `json:"abandoned"`
	OffLong      int   `json:"off_long"`
	Oversized    int   `json:"oversized"`
	ReclaimMem   int64 `json:"reclaimable_mem_bytes"`
	ReclaimCores int64 `json:"reclaimable_cores"`
}

type trendTotals struct {
	VMs          int `json:"vms"`
	Deleted      int `json:"deleted"`
	Insufficient int `json:"insufficient"`
	vmTrendSummary
}

func trendSum(rows []trendRow) trendTotals {
	t := trendTotals{VMs: len(rows)}
	for _, r := range rows {
		switch r.State {
		case "deleted":
			t.Deleted++
		case "off_long":
			t.OffLong++
		case "abandoned":
			t.Abandoned++
		case "oversized":
			t.Oversized++
		case "insufficient":
			t.Insufficient++
		}
		if r.State == "off_long" || r.State == "abandoned" {
			t.ReclaimMem = trendResourceSum(t.ReclaimMem, r.MemBytes)
			t.ReclaimCores = trendResourceSum(t.ReclaimCores, r.Cores)
		}
		if r.Proposal != nil {
			t.ReclaimMem = trendResourceSum(t.ReclaimMem, trendSavings(r.MemBytes, r.Proposal.MemBytes))
			t.ReclaimCores = trendResourceSum(t.ReclaimCores, trendSavings(r.Cores, r.Proposal.Cores))
		}
	}
	return t
}

func trendSavings(current, proposed int64) int64 {
	if current < 0 || proposed < 0 {
		return -1
	}
	return max(0, current-proposed)
}

func trendResourceSum(total, value int64) int64 {
	if total < 0 || value < 0 || value > math.MaxInt64-total {
		return -1
	}
	return total + value
}
