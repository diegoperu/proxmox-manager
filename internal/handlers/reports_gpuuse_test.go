package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/history"
)

func gpuReportParams() reportParams {
	p := yearParams()
	p.From = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p.To = time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	return p
}

func gpuDays(p reportParams, vmid int) []history.GPUUseDay {
	rows := make([]history.GPUUseDay, p.GPUIdle.Days)
	for i := range rows {
		rows[i] = history.GPUUseDay{Day: p.To.AddDate(0, 0, -i).Format("2006-01-02"), Cluster: "C", VMID: vmid,
			Node: "n1", Group: "altro", Model: "A100", Cards: 2, Expected: 10, Valid: 10, TempMax: -1, PowerAvg: -1}
	}
	return rows
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGPUUsageAggregation(t *testing.T) {
	p := gpuReportParams()
	// Unsorted input: metadata must come from the latest day, not the last row.
	rows := []history.GPUUseDay{
		{Day: "2026-10-31", VMID: 101, Node: "new", Group: "new", Model: "new", Cards: 3, Expected: 30, Valid: 30,
			UtilAvg: 40, UtilP95: 60, UtilMax: 80, MemAvgPct: 20, MemMaxPct: 40, BusyMinutes: 90, TempMax: 65, PowerAvg: -1},
		{Day: "2026-10-30", VMID: 101, Node: "old", Group: "old", Model: "old", Cards: 1, Expected: 20, Valid: 10,
			Off: 3, NoDriver: 2, DriverError: 1, NoData: 7, UtilAvg: 80, UtilP95: 95, UtilMax: 100, MemAvgPct: 60, MemMaxPct: 90, BusyMinutes: 60, TempMax: 70, PowerAvg: 100},
		{Day: "2026-10-29", VMID: 101, Expected: 20, Valid: 20, UtilAvg: 10, MemAvgPct: 5, TempMax: -1, PowerAvg: 250},
		{Day: "2026-10-28", VMID: 101, Expected: 10, PowerAvg: 1000, TempMax: -1},
	}
	u := aggregateGPUUsage(rows, p)
	if !u.Known || len(u.Rows) != 1 {
		t.Fatalf("%+v", u)
	}
	r := u.Rows[0]
	if r.Node != "new" || r.Group != "new" || r.Model != "new" || r.Cards != 3 || r.Days != 4 || r.Expected != 80 || r.Valid != 60 || r.Off != 3 || r.NoDriver != 2 || r.DriverError != 1 || r.NoData != 7 {
		t.Fatalf("%+v", r)
	}
	near(t, r.Coverage, .75)
	near(t, r.UtilAvg, 2200.0/60)
	near(t, r.MemAvg, 1300.0/60)
	near(t, r.UtilP95Day, 95)
	near(t, r.UtilMax, 100)
	near(t, r.MemMax, 90)
	near(t, r.BusyHours, 2.5)
	if r.TempMax == nil || r.PowerAvg == nil {
		t.Fatalf("missing temperature/power: %+v", r)
	}
	near(t, *r.TempMax, 70)
	near(t, *r.PowerAvg, 200) // unknown rows and zero valid samples have no weight
	unknown := aggregateGPUUsage([]history.GPUUseDay{{Day: "2026-10-31", VMID: 102, TempMax: -1, PowerAvg: -1}}, p).Rows[0]
	if unknown.Coverage != 0 || unknown.UtilAvg != 0 || unknown.MemAvg != 0 || unknown.TempMax != nil || unknown.PowerAvg != nil || unknown.State != "insufficient" {
		t.Fatalf("unknown: %+v", unknown)
	}
	b, err := json.Marshal(unknown)
	if err != nil || !strings.Contains(string(b), `"temp_max":null`) || !strings.Contains(string(b), `"power_avg":null`) {
		t.Fatalf("%s %v", b, err)
	}
	zero := aggregateGPUUsage([]history.GPUUseDay{{Day: "2026-10-31", VMID: 102, Expected: 1, Valid: 1}}, p).Rows[0]
	if zero.TempMax == nil || *zero.TempMax != 0 || zero.PowerAvg == nil || *zero.PowerAvg != 0 {
		t.Fatalf("zero is known, not -1: %+v", zero)
	}
}

func TestGPUUsageClassification(t *testing.T) {
	p := gpuReportParams()
	for _, tc := range []struct {
		name, state string
		change      func(*history.GPUUseDay)
		short       bool
	}{
		{"idle", "idle", func(r *history.GPUUseDay) { r.UtilAvg, r.MemAvgPct = 4.9, 4.9 }, false},
		{"util at threshold", "in_use", func(r *history.GPUUseDay) { r.UtilAvg = 5 }, false},
		{"mem at threshold", "in_use", func(r *history.GPUUseDay) { r.MemAvgPct = 5 }, false},
		{"off before driver and insufficient", "vm_off", func(r *history.GPUUseDay) { r.Valid, r.Off, r.NoDriver, r.DriverError = 0, 11, 6, 6 }, true},
		{"off when expected zero", "vm_off", func(r *history.GPUUseDay) { r.Expected, r.Valid, r.Off = 0, 0, 10 }, true},
		{"driver before error and insufficient", "no_driver", func(r *history.GPUUseDay) { r.Valid, r.NoDriver, r.DriverError = 0, 6, 6 }, true},
		{"error before insufficient", "driver_error", func(r *history.GPUUseDay) { r.Valid, r.DriverError = 0, 6 }, true},
		{"low coverage never idle", "insufficient", func(r *history.GPUUseDay) { r.Valid, r.NoData = 4, 6 }, false},
		{"short never idle", "insufficient", func(r *history.GPUUseDay) {}, true},
		{"half coverage is enough", "idle", func(r *history.GPUUseDay) { r.Valid, r.NoDriver, r.DriverError, r.Off = 5, 5, 5, 10 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := gpuDays(p, 101)
			if tc.short {
				rows = rows[:1]
			}
			for i := range rows {
				tc.change(&rows[i])
			}
			u := aggregateGPUUsage(rows, p)
			if u.Rows[0].State != tc.state {
				t.Fatalf("%+v", u.Rows[0])
			}
			if tc.state == "idle" && u.IdleCount != 2 || tc.state == "vm_off" && u.VMOffCount != 2 || tc.state == "insufficient" && u.InsufficientCount != 2 {
				t.Fatalf("counts must count cards: %+v", u)
			}
		})
	}
}

func TestGPUUsageWindow(t *testing.T) {
	p := gpuReportParams()
	rows := gpuDays(p, 101) // October 18..31 inclusive
	old := rows[0]
	old.Day, old.UtilAvg, old.MemAvgPct, old.Off = "2026-10-17", 100, 100, 1000
	rows = append(rows, old)
	// Outside the report period: neither metrics nor classification may see this row.
	future := old
	future.Day = "2026-11-01"
	rows = append(rows, future)
	u := aggregateGPUUsage(rows, p)
	if r := u.Rows[0]; r.State != "idle" || r.Days != 15 || r.UtilAvg <= 5 {
		t.Fatalf("classification must use window, metrics whole period: %+v", r)
	}
	rows[13].UtilAvg = 100 // first day inside the window is included
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "in_use" {
		t.Fatalf("window start excluded: %+v", r)
	}
	rows[13] = rows[12] // 14 rows but only 13 distinct days
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "insufficient" {
		t.Fatalf("duplicate days count as coverage: %+v", r)
	}
	// Even a long period cannot qualify a VM with no recent data.
	if r := aggregateGPUUsage([]history.GPUUseDay{old}, p).Rows[0]; r.State != "insufficient" {
		t.Fatalf("stale data: %+v", r)
	}
	// Classification averages must use valid-sample weights too.
	rows = gpuDays(p, 101)
	for i := range rows {
		rows[i].Expected, rows[i].Valid = 100, 100
	}
	rows[0].Expected, rows[0].Valid, rows[0].UtilAvg = 1, 1, 100
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "idle" {
		t.Fatalf("window average was not weighted: %+v", r)
	}
	// A short report cannot borrow days before from, even if supplied by the caller.
	p.From = p.To.AddDate(0, 0, -6)
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.Days != 7 || r.State != "insufficient" {
		t.Fatalf("report start ignored: %+v", r)
	}
	p.GPUIdle.Days = 7
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "idle" {
		t.Fatalf("custom idle_days ignored: %+v", r)
	}
	p.GPUIdle.Util = 0
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "in_use" {
		t.Fatalf("custom idle_util ignored: %+v", r)
	}
	p.GPUIdle.Util, p.GPUIdle.Mem = 50, 0
	if r := aggregateGPUUsage(rows, p).Rows[0]; r.State != "in_use" {
		t.Fatalf("custom idle_mem ignored: %+v", r)
	}
}

func TestGPUIdleParams(t *testing.T) {
	now := gpuReportParams().To
	base := url.Values{"from": {"2026-10-01"}}
	p, err := parseReportParams(base.Get, now)
	if err != nil || p.GPUIdle != defaultGPUIdleThresholds() {
		t.Fatalf("defaults: %+v %v", p.GPUIdle, err)
	}
	for _, name := range []string{"idle_util", "idle_mem", "idle_days"} {
		for _, value := range []string{"garbage", "NaN", "Inf", "-1", "51", "9999999999999999999999"} {
			if name == "idle_days" && value == "51" {
				continue
			}
			q := url.Values{"from": {"2026-10-01"}, name: {value}}
			if _, err := parseReportParams(q.Get, now); err == nil {
				t.Fatalf("accepted %s=%s", name, value)
			}
			r := chi.NewRouter()
			r.Get("/api/reports/{model}", (&Handler{}).GetReport)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/api/reports/gpu?"+q.Encode(), nil))
			if w.Code != 400 || !strings.Contains(w.Body.String(), name) {
				t.Fatalf("%s=%s: %d %s", name, value, w.Code, w.Body.String())
			}
		}
		valid := []string{"0", "50", "3.25"}
		if name == "idle_days" {
			valid = []string{"7", "60", "15"}
		}
		for _, value := range valid {
			q := url.Values{"from": {"2026-10-01"}, name: {value}}
			other, err := parseReportParams(q.Get, now)
			if err != nil || other.key() == p.key() {
				t.Fatalf("valid value and cache key: %s=%s: %v", name, value, err)
			}
		}
	}
	for _, value := range []string{"0", "6", "61", "14.5", "14.0"} {
		q := url.Values{"from": {"2026-10-01"}, "idle_days": {value}}
		if _, err := parseReportParams(q.Get, now); err == nil {
			t.Fatalf("accepted idle_days=%s", value)
		}
	}
}

func TestGPUUsageReports(t *testing.T) {
	hist, err := history.New(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()
	p := gpuReportParams()
	for _, store := range []*history.Store{nil, hist} {
		rep, err := buildGPUReport(newRpt(), store, "C", p, p.To)
		if err != nil || rep.Usage.Known || rep.Usage.Rows == nil || len(rep.Usage.Rows) != 0 {
			t.Fatalf("empty GPU history: %+v %v", rep.Usage, err)
		}
		svc, err := buildServiceReport(newRpt(), store, "C", p, p.To)
		if err != nil || svc.GPUUseKnown || svc.GPUUseByGroup == nil || len(svc.GPUUseByGroup) != 0 || svc.GPUIdle != 0 || svc.GPUVMOff != 0 || svc.GPUInsufficient != 0 {
			t.Fatalf("empty service history: %+v %v", svc, err)
		}
	}
	rows := gpuDays(p, 101)
	rows = append(rows, gpuDays(p, 999)...)
	off := gpuDays(p, 102)
	for i := range off {
		off[i].Expected, off[i].Valid, off[i].Off, off[i].Cards = 0, 0, 10, 3
		off[i].Group = "rossi"
	}
	rows = append(rows, off...)
	short := gpuDays(p, 103)[:1]
	short[0].Cards = 1
	rows = append(rows, short...)
	if err := hist.UpsertGPUUse(rows); err != nil {
		t.Fatal(err)
	}
	rep, err := buildGPUReport(newRpt(), hist, "C", p, p.To)
	if err != nil || !rep.Usage.Known || len(rep.Usage.Rows) != 4 {
		t.Fatalf("usage: %+v %v", rep.Usage, err)
	}
	if rep.Usage.Rows[0].Name != "rossi-idle" || rep.Usage.Rows[3].VMID != 999 || rep.Usage.Rows[3].Name != "" || rep.Usage.IdleCount != 4 || rep.Usage.VMOffCount != 3 || rep.Usage.InsufficientCount != 1 {
		t.Fatalf("names/counts: %+v", rep.Usage)
	}
	b, err := json.Marshal(rep)
	if err != nil || !strings.Contains(string(b), `"usage":{"known":true`) || !strings.Contains(string(b), `"name":"rossi-idle"`) {
		t.Fatalf("technical JSON: %s %v", b, err)
	}
	for _, tc := range []struct {
		group, query, kind, node string
		want                     int
	}{{"rossi", "", "all", "", 1}, {"", "rossi-idle", "all", "", 1}, {"", "", "lxc", "", 0}, {"", "", "all", "n2", 0}} {
		filtered := p
		filtered.Group, filtered.Query, filtered.Kind = tc.group, tc.query, tc.kind
		if tc.node != "" {
			filtered.Nodes = map[string]bool{tc.node: true}
		}
		r, err := buildGPUReport(newRpt(), hist, "C", filtered, p.To)
		if err != nil || len(r.Usage.Rows) != tc.want || r.Usage.Known != (tc.want > 0) {
			t.Fatalf("filter %+v: %+v %v", tc, r.Usage, err)
		}
	}
	svc, err := buildServiceReport(newRpt(), hist, "C", p, p.To)
	if err != nil || !svc.GPUUseKnown || svc.GPUIdle != 4 || svc.GPUVMOff != 3 || svc.GPUInsufficient != 1 || len(svc.GPUUseByGroup) != 2 {
		t.Fatalf("manager usage: %+v %v", svc, err)
	}
	b, err = json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{`"vmid"`, `"name"`, `"node"`, `"n1"`, "rossi-idle", "bianchi-busy", "A100"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%s leaked into manager data: %s", leak, b)
		}
	}
	// A storage failure must not be presented as an empty, successful history.
	if err := hist.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := buildGPUReport(newRpt(), hist, "C", p, p.To); err == nil {
		t.Fatal("GPU report ignored history error")
	}
	if _, err := buildServiceReport(newRpt(), hist, "C", p, p.To); err == nil {
		t.Fatal("service report ignored history error")
	}
}

func TestGPUUsageGroupWeightsAndTail(t *testing.T) {
	rows := []gpuUsageRow{
		{Group: "altro", Cards: 2, Valid: 10, UtilAvg: 80, BusyHours: 20},
		{Group: "altro", Cards: 3, Valid: 30, UtilAvg: 40, BusyHours: 30},
	}
	for i := 0; i < 11; i++ {
		rows = append(rows, gpuUsageRow{Group: fmt.Sprintf("g%d", i), Cards: 1, Valid: 10, UtilAvg: float64(i), BusyHours: float64(i)})
	}
	groups := gpuUsageGroups(rows)
	if len(groups) != 11 || groups[0].Group != "altro" || groups[0].Cards != 5 || groups[0].BusyHours != 50 || groups[0].UtilAvg != 50 {
		t.Fatalf("weights/order: %+v", groups)
	}
	tail := groups[10]
	if !tail.Others || tail.Group != "others" || tail.Cards != 2 || tail.BusyHours != 1 || tail.UtilAvg != .5 {
		t.Fatalf("tail: %+v", tail)
	}
	groups = gpuUsageGroups([]gpuUsageRow{{Group: "altro", Cards: 2}, {Group: "others", Cards: 1}})
	if len(groups) != 2 || groups[0].UtilAvg != 0 || groups[1].Others {
		t.Fatalf("zero weight and real others group: %+v", groups)
	}
}
