package handlers

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxmox-manager/internal/history"
)

func trendTestParams(t *testing.T) reportParams {
	t.Helper()
	p, err := parseTrendParams(func(string) string { return "" }, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTrendCalendarMonths(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{
		time.Date(2026, 3, 31, 12, 0, 0, 0, loc), time.Date(2026, 10, 25, 12, 0, 0, 0, loc), time.Date(2026, 11, 1, 0, 0, 0, 0, loc),
	} {
		p, err := parseTrendParams(func(k string) string { return map[string]string{"months": "3", "from": "invalid", "to": "invalid"}[k] }, date)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(date.Year(), date.Month()-2, 1, 0, 0, 0, 0, loc)
		if !p.From.Equal(start) || !p.To.Equal(date) || len(trendMonths(p.From, p.To)) != 3 {
			t.Fatalf("calendar: %+v", p)
		}
	}
	p := trendTestParams(t)
	if !reflect.DeepEqual(trendMonths(p.From, p.To), []string{"2026-06", "2026-07", "2026-08", "2026-09", "2026-10", "2026-11"}) {
		t.Fatal("default months")
	}
	// Timestamp near UTC midnight belongs to November locally, including the DST offset.
	p.From = time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	p.To = time.Date(2026, 11, 19, 0, 0, 0, 0, loc)
	ts := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC).Unix()
	avg := trendSeries(t, []map[string]interface{}{{"time": ts, "cpu": .5}})
	rows := mergeTrendMonths(nil, avg, rrdSeries{}, "", p)
	if rows[0].CPUAvg != nil || rows[1].CPUAvg == nil {
		t.Fatalf("local bucket: %+v", rows)
	}
}

func trendSeries(t *testing.T, points []map[string]interface{}) rrdSeries {
	t.Helper()
	raw, err := json.Marshal(points)
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseRRD(raw, vmUseFields...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTrendStoredAndRRD(t *testing.T) {
	p := trendTestParams(t)
	p.From = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ts := func(day string) int64 {
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Unix()
	}
	avg := trendSeries(t, []map[string]interface{}{
		{"time": ts("2026-10-01"), "cpu": .1, "mem": 20, "maxmem": 100, "netin": 2, "netout": 3, "diskread": 4, "diskwrite": 6},
		{"time": ts("2026-10-08"), "cpu": .3, "mem": 40, "maxmem": 100, "netin": 2, "netout": 3, "diskread": 4, "diskwrite": 6},
		// Stored wins at the first stored day's local midnight.
		{"time": ts("2026-10-15"), "cpu": 1, "mem": 100, "maxmem": 100, "netin": 9999, "netout": 9999},
	})
	mx := trendSeries(t, []map[string]interface{}{
		{"time": ts("2026-10-01"), "cpu": .4, "mem": 60, "maxmem": 100},
		{"time": ts("2026-10-15"), "cpu": 1, "mem": 100, "maxmem": 100},
	})
	stored := []history.VMUseDay{
		{Day: "2026-10-15", Points: 2, StepS: 1800, CPUAvg: 50, CPUMax: 55, MemAvg: 60, MemMax: 65, MemBytes: 100, NetIn: 10, NetOut: 20, DiskRead: 40, DiskWrite: 60},
		{Day: "2026-10-16", Points: 6, StepS: 1800, CPUAvg: 70, CPUMax: 75, MemAvg: 80, MemMax: 85, MemBytes: 200, NetIn: 20, NetOut: 30, DiskRead: 50, DiskWrite: 70},
		{Day: "2026-10-17", Points: 0, StepS: 1800, CPUAvg: -1, CPUMax: -1, MemAvg: -1, MemMax: -1},
	}
	m := mergeTrendMonths(stored, avg, mx, "2026-10-15", p)[0]
	if m.Source != "mixed" || *m.Days != 16 || *m.DaysOff != 1 || !m.data {
		t.Fatalf("merge: %+v", m)
	}
	near(t, *m.CPUAvg, (10*336+30*336+50*2+70*6)/(336.0*2+8))
	near(t, *m.MemAvg, (20*336+40*336+60*2+80*6)/(336.0*2+8))
	near(t, *m.CPUMax, 75)
	near(t, *m.MemMax, 85)
	near(t, m.peakMem, 170)
	near(t, *m.NetBytes, 5*14*86400+80)
	near(t, *m.DiskBytes, 10*14*86400+220)
	near(t, m.netSeconds, 14*86400+8*1800)
	// Only stored data: exact point-weighted means; off rows do not dilute them.
	m = mergeTrendMonths(stored, rrdSeries{}, rrdSeries{}, "2026-10-15", p)[0]
	near(t, *m.CPUAvg, 65)
	near(t, *m.MemAvg, 75)
	if m.Source != "stored" || *m.Days != 2 || *m.DaysOff != 1 {
		t.Fatalf("stored: %+v", m)
	}
	// Unknown values remain null. A measured zero remains zero.
	empty := mergeTrendMonths(nil, rrdSeries{}, rrdSeries{}, "", p)[0]
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"days", "days_off", "cpu_avg", "cpu_max", "mem_avg", "mem_max", "net_bytes", "disk_bytes"} {
		if !strings.Contains(string(b), `"`+field+`":null`) {
			t.Fatalf("unknown %s: %s", field, b)
		}
	}
	avg = trendSeries(t, []map[string]interface{}{{"time": ts("2026-10-01"), "cpu": 0}})
	m = mergeTrendMonths(nil, avg, rrdSeries{}, "", p)[0]
	if m.CPUAvg == nil || *m.CPUAvg != 0 || m.MemAvg != nil || m.CPUMax != nil || m.NetBytes != nil || m.DiskBytes != nil || m.DaysOff != nil || m.Source != "rrd" {
		t.Fatalf("rrd unknown: %+v", m)
	}
	// Six-hour then weekly steps retain their own duration, including first point outside period.
	avg = trendSeries(t, []map[string]interface{}{
		{"time": ts("2026-09-24"), "cpu": .9},
		{"time": ts("2026-10-01"), "cpu": .2},
		{"time": ts("2026-10-01") + 21600, "cpu": .6},
	})
	m = mergeTrendMonths(nil, avg, rrdSeries{}, "", p)[0]
	near(t, *m.CPUAvg, (20*604800+60*21600)/(604800.0+21600))
	near(t, *m.Days, 7.25)
	// Missing memory does not contribute a zero to its weighted average.
	stored[0].MemAvg = -1
	m = mergeTrendMonths(stored, rrdSeries{}, rrdSeries{}, "2026-10-15", p)[0]
	near(t, *m.MemAvg, 80)
	// An off day supplies no metric zeros, even though its persisted counters are zero.
	m = mergeTrendMonths(stored[2:], rrdSeries{}, rrdSeries{}, "2026-10-15", p)[0]
	if m.CPUAvg != nil || m.NetBytes != nil || m.DiskBytes != nil || *m.Days != 0 || *m.DaysOff != 1 {
		t.Fatalf("off metrics: %+v", m)
	}
	// Each metric keeps its own validity: absent CPU must not erase known RAM or network.
	avg = trendSeries(t, []map[string]interface{}{{"time": ts("2026-10-01"), "mem": 20, "maxmem": 100, "netin": 2, "netout": 3}})
	m = mergeTrendMonths(nil, avg, rrdSeries{}, "", p)[0]
	if m.CPUAvg != nil || !m.data {
		t.Fatalf("independent metrics: %+v", m)
	}
	near(t, *m.MemAvg, 20)
	// A measured peak is evidence of data even when the AVERAGE consolidation is missing.
	m = mergeTrendMonths(nil, rrdSeries{}, mx, "", p)[0]
	if !m.data || m.CPUAvg != nil || m.CPUMax == nil {
		t.Fatalf("MAX evidence: %+v", m)
	}
	// A backwards archive seam and duplicate do not inflate duration.
	avg = trendSeries(t, []map[string]interface{}{
		{"time": ts("2026-10-01"), "cpu": .1},
		{"time": ts("2026-10-01") + 21600, "cpu": .1},
		{"time": ts("2026-10-01") + 10800, "cpu": 1},
		{"time": ts("2026-10-01") + 43200, "cpu": .1},
		{"time": ts("2026-10-01") + 43200, "cpu": 1},
	})
	m = mergeTrendMonths(nil, avg, rrdSeries{}, "", p)[0]
	near(t, *m.CPUAvg, 10)
	near(t, *m.Days, .75)
}

func trendEvidence(data bool, cpu, net, cpuMax, memMax float64, month string) trendMonth {
	return trendMonth{Month: month, data: data, CPUAvg: trendNumber(cpu), CPUMax: trendNumber(cpuMax), MemMax: trendNumber(memMax), NetBytes: trendNumber(net * 86400), netSeconds: 86400, peakMem: float64(16*gib) * memMax / 100}
}

func TestTrendClassification(t *testing.T) {
	th := defaultTrendThresholds()
	busy := func(m string) trendMonth { return trendEvidence(true, 30, 2048, 50, 60, m) }
	idle := func(m string) trendMonth { return trendEvidence(true, 1, 100, 5, 10, m) }
	off := func(m string) trendMonth { return trendEvidence(false, -1, -1, -1, -1, m) }
	small := func(m string) trendMonth { return trendEvidence(true, 4, 1024, 9, 24, m) }
	tests := []struct {
		name, status, state string
		months              []trendMonth
		mem, cpu            bool
		last                string
	}{
		{name: "deleted precedes off", status: "deleted", state: "deleted", months: []trendMonth{off("09"), off("10"), off("11")}},
		{name: "off long", state: "off_long", months: []trendMonth{busy("08"), off("09"), off("10"), off("11")}, last: "08"},
		{name: "off even without any evidence", state: "off_long", months: []trendMonth{off("09"), off("10"), off("11")}},
		{name: "abandoned precedes oversized", state: "abandoned", months: []trendMonth{busy("08"), idle("09"), idle("10"), idle("11")}, last: "08"},
		{name: "oversized both", state: "oversized", months: []trendMonth{small("09"), small("10"), small("11")}, mem: true, cpu: true, last: "11"},
		{name: "in use", state: "in_use", months: []trendMonth{busy("09"), busy("10"), busy("11")}, last: "11"},
		{name: "insufficient", state: "insufficient", months: []trendMonth{off("09"), off("10"), busy("11")}, last: "11"},
		{name: "short selection", state: "insufficient", months: []trendMonth{off("11")}},
		{name: "new small: two months are enough for oversized", state: "oversized", months: []trendMonth{off("09"), small("10"), small("11")}, mem: true, cpu: true, last: "11"},
		{name: "cpu active boundary", state: "in_use", months: []trendMonth{idle("09"), idle("10"), trendEvidence(true, 2, 100, 10, 25, "11")}, last: "11"},
		{name: "network active boundary", state: "in_use", months: []trendMonth{idle("09"), idle("10"), trendEvidence(true, 1, 1024, 10, 25, "11")}, last: "11"},
		{name: "every month per resource", state: "in_use", months: []trendMonth{trendEvidence(true, 5, 100, 5, 40, "09"), trendEvidence(true, 5, 100, 20, 10, "10"), small("11")}, last: "11"},
		{name: "only memory", state: "oversized", months: []trendMonth{trendEvidence(true, 5, 100, 20, 10, "09"), small("10"), small("11")}, mem: true, last: "11"},
		{name: "only cpu", state: "oversized", months: []trendMonth{trendEvidence(true, 5, 100, 5, 40, "09"), small("10"), small("11")}, cpu: true, last: "11"},
		{name: "unknown peaks never oversized", state: "in_use", months: []trendMonth{trendEvidence(true, 5, 100, -1, -1, "09"), small("10"), small("11")}, last: "11"},
		{name: "D122 idle with monthly peaks is not abandoned", state: "in_use", months: []trendMonth{busy("08"), trendEvidence(true, 1, 100, 30, 88, "09"), trendEvidence(true, 1, 100, 25, 88, "10"), trendEvidence(true, 1, 100, 32, 88, "11")}, last: "08"},
		{name: "D122 one peak month breaks abandonment", state: "in_use", months: []trendMonth{busy("08"), idle("09"), trendEvidence(true, 1, 100, 10, 10, "10"), idle("11")}, last: "08"},
		{name: "unknown network never abandoned", state: "in_use", months: []trendMonth{trendEvidence(true, 1, -1, 30, 40, "09"), idle("10"), idle("11")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			if status == "" {
				status = "running"
			}
			row := trendRow{Status: status, Cores: 8, MemBytes: 16 * gib, Months: tt.months}
			classifyTrend(&row, th)
			last := ""
			if row.LastActiveMonth != nil {
				last = *row.LastActiveMonth
			}
			if row.State != tt.state || row.OversizedMem != tt.mem || row.OversizedCPU != tt.cpu || last != tt.last || (row.Proposal != nil) != (tt.state == "oversized") {
				t.Fatalf("%+v last=%s", row, last)
			}
		})
	}
	// Exactly two months qualify when the abandonment threshold also permits two.
	th.AbandonMonths = 2
	row := trendRow{Status: "running", Cores: 8, MemBytes: 16 * gib, Months: []trendMonth{small("10"), small("11")}}
	classifyTrend(&row, th)
	if row.State != "oversized" {
		t.Fatalf("two months: %+v", row)
	}
	row.Months = row.Months[1:]
	th.AbandonMonths = 1
	classifyTrend(&row, th)
	if row.State != "insufficient" {
		t.Fatalf("one month: too early to judge: %+v", row)
	}
}

// D122: a VM with a passthrough device (GPU) is never judged oversized on CPU; RAM is still judged.
func TestTrendPCIVMCPUNotOversized(t *testing.T) {
	th := defaultTrendThresholds()
	lowCPUhighMem := func(m string) trendMonth { return trendEvidence(true, 0.2, 100, 4, 97, m) }
	row := trendRow{Status: "running", Cores: 64, MemBytes: 96 * gib, PCI: true, Months: []trendMonth{lowCPUhighMem("10"), lowCPUhighMem("11")}}
	classifyTrend(&row, th)
	if row.State == "oversized" || row.OversizedCPU || row.Proposal != nil {
		t.Fatalf("GPU VM proposed fewer cores: %+v", row)
	}
	small := func(m string) trendMonth { return trendEvidence(true, 4, 1024, 4, 10, m) }
	row = trendRow{Status: "running", Cores: 64, MemBytes: 96 * gib, PCI: true, Months: []trendMonth{small("10"), small("11")}}
	classifyTrend(&row, th)
	if row.State != "oversized" || !row.OversizedMem || row.OversizedCPU || row.Proposal == nil || row.Proposal.Cores != 64 {
		t.Fatalf("GPU VM: RAM must still be judged, cores untouched: %+v %+v", row, row.Proposal)
	}
	row.PCI = false
	classifyTrend(&row, th)
	if !row.OversizedCPU {
		t.Fatal("without PCI the same VM is CPU-oversized")
	}
}

func TestTrendProposalAndTotals(t *testing.T) {
	for _, tt := range []struct {
		name              string
		cores, mem        int64
		cpu, used, margin float64
		wantC, wantM      int64
	}{
		{"round up", 16, 32 * gib, 10, float64(3 * gib), 25, 2, 4 * gib},
		{"minimum", 16, 32 * gib, 0, 0, 25, 1, 2 * gib},
		{"clamp", 1, gib, 100, float64(10 * gib), 200, 1, gib},
		{"exact", 8, 16 * gib, 25, float64(4 * gib), 0, 2, 4 * gib},
		{"next byte", 8, 16 * gib, 25, float64(4*gib) + 1, 0, 2, 6 * gib},
		{"unknown resources", -1, -1, 25, 100, 25, -1, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := proposeTrend(tt.cores, tt.mem, tt.cpu, tt.used, true, true, tt.margin)
			if p.Cores != tt.wantC || p.MemBytes != tt.wantM {
				t.Fatalf("%+v", p)
			}
		})
	}
	p := proposeTrend(8, 16*gib, 1, 0, false, true, 25)
	if p.MemBytes != 16*gib {
		t.Fatal("unflagged memory changed")
	}
	totals := trendSum([]trendRow{
		{State: "abandoned", Cores: 8, MemBytes: 16 * gib}, {State: "off_long", Cores: 2, MemBytes: 4 * gib},
		{State: "oversized", Cores: 8, MemBytes: 16 * gib, Proposal: &trendProposal{Cores: 2, MemBytes: 4 * gib}},
		{State: "deleted", Cores: 99, MemBytes: 99 * gib}, {State: "insufficient"},
	})
	if totals.VMs != 5 || totals.Abandoned != 1 || totals.OffLong != 1 || totals.Oversized != 1 || totals.Deleted != 1 || totals.Insufficient != 1 || totals.ReclaimCores != 16 || totals.ReclaimMem != 32*gib {
		t.Fatalf("%+v", totals)
	}
	if trendNumber(math.NaN()) != nil || trendNumber(math.Inf(1)) != nil || trendNumber(-1) != nil {
		t.Fatal("invalid number")
	}
	unknown := trendGuestRow(resGuest{})
	if unknown.Cores != -1 || unknown.MemBytes != -1 {
		t.Fatalf("unknown allocations: %+v", unknown)
	}
	unknown.State = "off_long"
	totals = trendSum([]trendRow{unknown, {State: "abandoned", MemBytes: gib, Cores: 4}})
	if totals.ReclaimMem != -1 || totals.ReclaimCores != -1 {
		t.Fatalf("unknown savings: %+v", totals)
	}
	if trendResourceSum(math.MaxInt64, 1) != -1 {
		t.Fatal("resource sum overflow")
	}
}
