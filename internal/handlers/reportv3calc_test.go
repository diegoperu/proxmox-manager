package handlers

import (
	"fmt"
	"math"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

func TestV3GrowthCalendarPartialAndCompleteComparisons(t *testing.T) {
	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	rows := []trendRow{
		{Cores: 4, MemBytes: 8 * gib, Months: []trendMonth{{Month: "2026-01", data: true}, {Month: "2026-02", data: true}, {Month: "2026-03", data: true}}},
		{Cores: 8, MemBytes: 16 * gib, Months: []trendMonth{{Month: "2026-03", data: true}, {Month: "2026-04", Source: "stored"}}},
	}
	nodes := []rrdSeries{{T: []int64{time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix()}, Step: 1800,
		Cols: map[string][]float64{"memused": {50}, "memtotal": {100}}}}
	months, vms, mem := calculateV3Growth(rows, nodes, from, to)
	if len(months) != 4 || !months[0].Partial || months[1].Partial || months[2].Partial || !months[3].Partial {
		t.Fatalf("calendar months: %+v", months)
	}
	if *months[1].VMsActive != 1 || *months[2].VMsActive != 2 || *months[2].CoresAssigned != 12 || *months[2].MemAssignedBytes != 24*gib || *months[3].VMsActive != 0 {
		t.Fatalf("monthly totals: %+v", months)
	}
	v3AssertPct(t, vms, 100)
	v3AssertPct(t, mem, 200)
	v3AssertPct(t, months[1].NodeMemUsedPct, 50)
	if months[0].NodeMemUsedPct != nil {
		t.Fatal("unknown node memory became zero")
	}
	_, vms, mem = calculateV3Growth(rows, nil, from, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	if vms != nil || mem != nil {
		t.Fatal("one complete month must not produce growth")
	}
	_, vms, mem = calculateV3Growth(rows, nil, from, from.AddDate(0, 0, 1))
	if vms != nil || mem != nil {
		t.Fatal("no complete months must not produce growth")
	}
}

func TestV3GrowthUnknownAndNegativeResources(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 2, 0).Add(-time.Second)
	rows := []trendRow{{Cores: -1, MemBytes: -1, Months: []trendMonth{{Month: "2026-01", data: true}, {Month: "2026-02"}}}}
	months, vg, mg := calculateV3Growth(rows, nil, from, to)
	if months[0].CoresAssigned != nil || months[0].MemAssignedBytes != nil || months[1].VMsActive != nil || vg != nil || mg != nil {
		t.Fatalf("unknown was priced/countable: %+v %v %v", months, vg, mg)
	}
	if !v3CompleteMonth("2026-02", from, to) {
		t.Fatal("inclusive month-end must be complete")
	}
	rows = []trendRow{{Cores: 1, MemBytes: gib, Months: []trendMonth{{Month: "2026-01", data: true}, {Month: "2026-02", Source: "stored"}}}}
	_, vg, mg = calculateV3Growth(rows, nil, from, to)
	v3AssertPct(t, vg, -100)
	v3AssertPct(t, mg, -100)
}

func TestV3ConcentrationRankBandsAndFirstGroup(t *testing.T) {
	guests := []resGuest{}
	for i := 1; i <= 21; i++ {
		guests = append(guests, resGuest{Mem: int64(i), Groups: []string{fmt.Sprintf("g%02d", i), "ignored-shared-group"}})
	}
	guests = append(guests, resGuest{Mem: 9, Groups: []string{otherGroup}})
	r := calculateV3Concentration(guests)
	if r.Groups != 21 {
		t.Fatalf("groups %d", r.Groups)
	}
	v3AssertPct(t, r.Top5Pct, round2(95.0/240*100))
	v3AssertPct(t, r.Next15Pct, round2(135.0/240*100))
	v3AssertPct(t, r.RestPct, round2(1.0/240*100))
	v3AssertPct(t, r.NoGroupPct, round2(9.0/240*100))
	guests[0].Mem = 0
	if calculateV3Concentration(guests).Top5Pct != nil {
		t.Fatal("missing RAM produced a percentage")
	}
	if calculateV3Concentration(nil).NoGroupPct != nil {
		t.Fatal("empty denominator")
	}
}

func v3SizingMonth(month string, cpu, peakCPU, mem, peakMem float64) trendMonth {
	return trendMonth{Month: month, Source: "stored", data: true, CPUAvg: trendNumber(cpu), CPUMax: trendNumber(peakCPU),
		MemMax: trendNumber(mem), peakMem: peakMem, NetBytes: trendNumber(100), netSeconds: 86400}
}

func TestV3SizingStatesReclaimAndSixMonthWindow(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	rows := make([]trendRow, 5)
	for i := range rows {
		rows[i] = trendRow{Status: "running", Cores: 4, MemBytes: 16 * gib}
		for _, month := range trendMonths(from, to) {
			m := v3SizingMonth(month, 20, 80, 90, 14*float64(gib))
			switch i {
			case 1:
				m = v3SizingMonth(month, 2, 2, 10, 2*float64(gib))
			case 2:
				m = trendMonth{Month: month, Source: "stored"}
			case 3:
				m = v3SizingMonth(month, .1, 1, 10, float64(gib))
			case 4:
				if month != "2026-08" {
					m = trendMonth{Month: month, Source: "stored"}
				}
			}
			// Old high peaks must not prevent six-month oversized classification.
			if i == 1 && month < "2026-03" {
				m = v3SizingMonth(month, 90, 100, 100, 16*float64(gib))
			}
			rows[i].Months = append(rows[i].Months, m)
		}
	}
	r := calculateV3Sizing(rows, from, to, defaultTrendThresholds())
	if r.Counts.InUse != 1 || r.Counts.Oversized != 1 || r.Counts.OffLong != 1 || r.Counts.Abandoned != 1 || r.Counts.Insufficient != 1 {
		t.Fatalf("states %+v", r)
	}
	if *r.ReclaimableOversized.MemBytes != 12*gib || *r.ReclaimableOversized.Cores != 3 || *r.ReclaimableOffLong.MemBytes != 16*gib || *r.ReclaimableOffLong.Cores != 4 {
		t.Fatalf("reclaim %+v %+v", r.ReclaimableOversized, r.ReclaimableOffLong)
	}
	if r.CPUBands == nil || r.CPUBands.LT5 != 2 || r.CPUBands.From5To25 != 1 {
		t.Fatalf("bands %+v", r.CPUBands)
	}
}

func TestV3SizingCPUBandEdgesDefaultsAndUnknown(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	rows := []trendRow{}
	for _, cpu := range []float64{0, 4.99, 5, 24.99, 25, 49.99, 50, 100} {
		rows = append(rows, trendRow{Status: "stopped", Months: []trendMonth{v3SizingMonth("2026-10", cpu, cpu, 50, float64(gib))}})
	}
	r := calculateV3Sizing(rows, from, to, defaultTrendThresholds())
	if r.CPUBands == nil || *r.CPUBands != (v3CPUBands{2, 2, 2, 2}) {
		t.Fatalf("edges %+v", r.CPUBands)
	}
	rows = []trendRow{{Status: "running", Months: []trendMonth{{Month: "2026-10", data: true}}}}
	if calculateV3Sizing(rows, from, to, defaultTrendThresholds()).CPUBands != nil {
		t.Fatal("unknown CPU was bucketed")
	}
	rows = append(rows, trendRow{Status: "running", Months: []trendMonth{v3SizingMonth("2026-10", 20, 40, 50, float64(gib))}})
	if calculateV3Sizing(rows, from, to, defaultTrendThresholds()).CPUBands != nil {
		t.Fatal("known CPU concealed a second guest's unknown CPU")
	}
	rows = []trendRow{{Status: "stopped", Months: []trendMonth{{Month: "2026-10", Source: "stored"}}}}
	if bands := calculateV3Sizing(rows, from, to, defaultTrendThresholds()).CPUBands; bands == nil || *bands != (v3CPUBands{}) {
		t.Fatal("observed all-off month should have known zero CPU bands")
	}
	custom := &config.ReportSettings{Defaults: config.ReportDefaults{AbandonMonths: 5, OverMem: 20, OverCPU: 8, Margin: 40}}
	th := v3TrendThresholds(custom)
	if th.AbandonMonths != 5 || th.OverMem != 20 || th.OverCPU != 8 || th.Margin != 40 || th.AbandonCPU != 2 || v3TrendThresholds(nil) != defaultTrendThresholds() {
		t.Fatalf("defaults %+v", th)
	}
}

func TestV3RunningHoursScaleAndInstanceIsolation(t *testing.T) {
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 29)
	rows := []history.VMUseDay{
		{Day: "2026-10-20", VMID: 999777, Inst: "old", Kind: "qemu", Points: 48},
		{Day: "2026-10-20", VMID: 999777, Inst: "new", Kind: "qemu", Points: 4},
		{Day: "2026-10-21", VMID: 999777, Inst: "new", Kind: "qemu", Points: 0},
	}
	h := calculateV3Hours(rows, 999777, "new", "qemu", from, to, 2)
	if days := calculateV3DaysUsed(rows, from, to); days != 2 {
		t.Fatalf("distinct stored days %d", days)
	}
	if h == nil || math.Abs(*h-30) > .001 {
		t.Fatalf("hours %v", h)
	}
	// A guest without rows in a window the cluster has history for did not run: 0, never another instance's hours.
	for _, inst := range []string{"absent", ""} {
		if h := calculateV3Hours(rows, 999777, inst, "qemu", from, to, 2); h == nil || *h != 0 {
			t.Fatalf("instance %q: want 0 hours, got %v", inst, h)
		}
	}
	if calculateV3Hours(rows, 999777, "new", "qemu", from, to, 0) != nil {
		t.Fatal("no stored history at all must be unknown")
	}
}
