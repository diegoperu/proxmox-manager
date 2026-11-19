package handlers

import (
	"math"
	"sort"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// These response types deliberately contain only aggregates, calendar labels and fixed
// codes. Never embed technical report rows, inventory, settings or identity-keyed maps.
type serviceV3 struct {
	Growth        []v3GrowthMonth `json:"growth"`
	VMGrowthPct   *float64        `json:"vm_growth_pct"`
	MemGrowthPct  *float64        `json:"mem_growth_pct"`
	Concentration v3Concentration `json:"concentration"`
	Sizing        v3Sizing        `json:"sizing"`
	GPUClasses    []v3GPUClass    `json:"gpu_classes"`
	Availability  v3Availability  `json:"availability"`
	Backups       v3Backups       `json:"backups"`
	Economic      *v3Economic     `json:"economic"`
}

type v3GrowthMonth struct {
	Month            string   `json:"month"`
	Partial          bool     `json:"partial"`
	VMsActive        *int64   `json:"vms_active"`
	MemAssignedBytes *int64   `json:"mem_assigned_bytes"`
	CoresAssigned    *int64   `json:"cores_assigned"`
	NodeMemUsedPct   *float64 `json:"node_mem_used_pct"`
}

func v3Int(v int64) *int64 {
	if v < 0 {
		return nil
	}
	return &v
}

func v3Pct(n, d float64) *float64 {
	if d <= 0 || math.IsNaN(n) || math.IsInf(n, 0) || math.IsNaN(d) || math.IsInf(d, 0) {
		return nil
	}
	v := round2(n / d * 100)
	return &v
}

// The API's To is inclusive to the second. Calendar boundaries use an exclusive end.
func v3PeriodEnd(to time.Time) time.Time { return to.Truncate(time.Second).Add(time.Second) }

func v3CompleteMonth(month string, from, to time.Time) bool {
	start, err := time.ParseInLocation("2006-01", month, from.Location())
	return err == nil && !from.After(start) && !v3PeriodEnd(to).Before(start.AddDate(0, 1, 0))
}

// Growth reuses the trend's stored-first/RRD-before-history monthly evidence. As in
// the technical trend, assigned resources are the instance's latest known allocation;
// RRD does not reconstruct historical core assignments. A month without evidence is unknown.
func calculateV3Growth(rows []trendRow, nodes []rrdSeries, from, to time.Time) ([]v3GrowthMonth, *float64, *float64) {
	out := []v3GrowthMonth{}
	for _, month := range trendMonths(from, to) {
		r := v3GrowthMonth{Month: month, Partial: !v3CompleteMonth(month, from, to)}
		var active, mem, cores int64
		known := len(rows) == 0
		for _, row := range rows {
			for _, m := range row.Months {
				if m.Month != month {
					continue
				}
				known = known || m.Source != "" || m.data
				if m.data {
					active++
					mem = trendResourceSum(mem, row.MemBytes)
					cores = trendResourceSum(cores, row.Cores)
				}
			}
		}
		if known {
			r.VMsActive, r.MemAssignedBytes, r.CoresAssigned = v3Int(active), v3Int(mem), v3Int(cores)
		}
		var used, capacity float64
		for _, s := range nodes {
			last := int64(-1)
			for i, ts := range s.T {
				if ts <= last {
					continue
				}
				step := float64(trendRRDStep(s, i, last))
				last = ts
				t := time.Unix(ts, 0).In(from.Location())
				if t.Before(from) || t.After(to) || t.Format("2006-01") != month {
					continue
				}
				u, total := trendValue(s, "memused", i), trendValue(s, "memtotal", i)
				if !math.IsNaN(u) && total > 0 && !math.IsNaN(total) {
					used += u * step
					capacity += total * step
				}
			}
		}
		r.NodeMemUsedPct = v3Pct(used, capacity)
		out = append(out, r)
	}
	var complete []v3GrowthMonth
	for _, r := range out {
		if !r.Partial {
			complete = append(complete, r)
		}
	}
	if len(complete) < 2 {
		return out, nil, nil
	}
	first, last := complete[0], complete[len(complete)-1]
	growth := func(a, b *int64) *float64 {
		if a == nil || b == nil || *a <= 0 {
			return nil
		}
		return v3Pct(float64(*b)-float64(*a), float64(*a))
	}
	return out, growth(first.VMsActive, last.VMsActive), growth(first.MemAssignedBytes, last.MemAssignedBytes)
}

type v3Concentration struct {
	Groups     int      `json:"groups"`
	Top5Pct    *float64 `json:"top5_pct"`
	Next15Pct  *float64 `json:"next15_pct"`
	RestPct    *float64 `json:"rest_pct"`
	NoGroupPct *float64 `json:"nogroup_pct"`
}

func calculateV3Concentration(guests []resGuest) v3Concentration {
	groups := map[string]float64{}
	var total, noGroup float64
	known := true
	for _, g := range guests {
		known = known && g.Mem > 0
		mem := float64(max(0, g.Mem))
		total += mem
		group := firstGroup(g.Groups)
		if group == otherGroup {
			noGroup += mem
		} else {
			groups[group] += mem
		}
	}
	r := v3Concentration{Groups: len(groups)}
	if !known {
		return r
	}
	amounts := make([]float64, 0, len(groups))
	for _, v := range groups {
		amounts = append(amounts, v)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(amounts)))
	var top, next, rest float64
	for i, v := range amounts {
		switch {
		case i < 5:
			top += v
		case i < 20:
			next += v
		default:
			rest += v
		}
	}
	r.Top5Pct, r.Next15Pct, r.RestPct, r.NoGroupPct = v3Pct(top, total), v3Pct(next, total), v3Pct(rest, total), v3Pct(noGroup, total)
	return r
}

type v3Reclaim struct {
	MemBytes *int64 `json:"mem_bytes"`
	Cores    *int64 `json:"cores"`
}

type v3CPUBands struct {
	LT5        int `json:"lt5"`
	From5To25  int `json:"5_25"`
	From25To50 int `json:"25_50"`
	GT50       int `json:"gt50"`
}

type v3SizingCounts struct {
	InUse        int `json:"in_use"`
	Oversized    int `json:"oversized"`
	OffLong      int `json:"off_long"`
	Insufficient int `json:"insufficient"`
	Abandoned    int `json:"abandoned"`
}

type v3Sizing struct {
	Counts               v3SizingCounts `json:"counts"`
	CPUBands             *v3CPUBands    `json:"cpu_bands"`
	ReclaimableOversized v3Reclaim      `json:"reclaimable_oversized"`
	ReclaimableOffLong   v3Reclaim      `json:"reclaimable_off_long"`
}

func v3TrendThresholds(settings *config.ReportSettings) trendThresholds {
	t := defaultTrendThresholds()
	if settings == nil {
		return t
	}
	d := settings.Defaults
	for _, f := range []struct {
		dst   *float64
		value float64
	}{
		{&t.AbandonCPU, d.AbandonCPU}, {&t.AbandonNet, d.AbandonNet}, {&t.AbandonPeak, d.AbandonPeak},
		{&t.OverMem, d.OverMem}, {&t.OverCPU, d.OverCPU}, {&t.Margin, d.Margin},
	} {
		if f.value != 0 {
			*f.dst = f.value
		}
	}
	if d.AbandonMonths != 0 {
		t.AbandonMonths = d.AbandonMonths
	}
	return t
}

func calculateV3Sizing(rows []trendRow, from, to time.Time, th trendThresholds) v3Sizing {
	r := v3Sizing{}
	months := trendMonths(from, to)
	start := months[max(0, len(months)-6)]
	lastComplete := ""
	for _, month := range months {
		if v3CompleteMonth(month, from, to) {
			lastComplete = month
		}
	}
	var overMem, overCores, offMem, offCores int64
	bandKnown := len(rows) == 0 && lastComplete != ""
	bandUnknown := false
	bands := v3CPUBands{}
	for _, original := range rows {
		row := original
		row.Months = nil
		for _, m := range original.Months {
			if m.Month >= start {
				row.Months = append(row.Months, m)
			}
			// Running in the month, even if it has since stopped. Unknown CPU is not a zero band.
			if m.Month == lastComplete {
				bandKnown = bandKnown || m.Source != "" || m.data
				bandUnknown = bandUnknown || (m.data && m.CPUAvg == nil)
			}
			if m.Month == lastComplete && m.data && m.CPUAvg != nil {
				switch cpu := *m.CPUAvg; {
				case cpu < 5:
					bands.LT5++
				case cpu < 25:
					bands.From5To25++
				case cpu < 50:
					bands.From25To50++
				default:
					bands.GT50++
				}
			}
		}
		if original.Status == "deleted" {
			continue // historical running hours count in CPU bands, not current sizing
		}
		classifyTrend(&row, th)
		switch row.State {
		case "in_use":
			r.Counts.InUse++
		case "oversized":
			r.Counts.Oversized++
			if row.Proposal != nil {
				overMem = trendResourceSum(overMem, trendSavings(row.MemBytes, row.Proposal.MemBytes))
				overCores = trendResourceSum(overCores, trendSavings(row.Cores, row.Proposal.Cores))
			}
		case "off_long":
			r.Counts.OffLong++
			offMem = trendResourceSum(offMem, row.MemBytes)
			offCores = trendResourceSum(offCores, row.Cores)
		case "abandoned":
			r.Counts.Abandoned++
		default:
			r.Counts.Insufficient++
		}
	}
	if bandKnown && !bandUnknown {
		r.CPUBands = &bands
	}
	r.ReclaimableOversized = v3Reclaim{v3Int(overMem), v3Int(overCores)}
	r.ReclaimableOffLong = v3Reclaim{v3Int(offMem), v3Int(offCores)}
	return r
}

// VM history is daily at 30-minute resolution. Missing instance history stays unknown;
// an observed off day is a known zero. Never attribute a reused VMID's old hours to a new VM.
func calculateV3DaysUsed(rows []history.VMUseDay, from, to time.Time) int {
	days := map[string]bool{}
	for _, row := range rows {
		if row.Day >= from.Format("2006-01-02") && row.Day <= to.Format("2006-01-02") {
			days[row.Day] = true
		}
	}
	return len(days)
}

func calculateV3Hours(rows []history.VMUseDay, vmid int, inst, kind string, from, to time.Time, daysUsed int) *float64 {
	// No stored day for the cluster at all: the running time is unknown.
	if daysUsed <= 0 {
		return nil
	}
	// The daily job writes a row for every guest that had data, so a guest without rows in a window the
	// cluster has history for did not run in it: 0 hours, not unknown (one unknown guest would otherwise
	// blank the whole market estimate).
	var hours float64
	for _, row := range rows {
		if inst != "" && row.VMID == vmid && row.Inst == inst && row.Kind == kind && row.Day >= from.Format("2006-01-02") && row.Day <= to.Format("2006-01-02") {
			hours += float64(row.Points) * .5
		}
	}
	return trendNumber(hours * 30 / float64(daysUsed))
}
