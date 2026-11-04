package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"proxmox-manager/internal/history"
)

func TestParseReportParams(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
	q := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	ok := map[string]string{"from": "2026-10-10", "to": "2026-10-31", "nodes": "n1,n2", "type": "qemu", "group": "rossi", "q": "Web", "inactive_cpu": "0.05"}
	p, err := parseReportParams(q(ok), now)
	if err != nil || p.TF != "month" || p.Kind != "qemu" || !p.Nodes["n2"] || p.Query != "web" || p.TH.InactiveCPU != 0.05 || p.Group != "rossi" {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := parseReportParams(q(map[string]string{"from": "2026-01-01"}), now); err != nil || p.TF != "year" || !p.To.Equal(now) {
		t.Fatalf("default to = now: %+v %v", p, err)
	}
	bad := []map[string]string{
		{}, {"from": "01/10/2026"}, {"from": "2026-10-01", "to": "garbage"}, {"from": "2024-01-01"}, {"from": "2026-11-04"}, {"from": "2026-11-03", "to": "2026-11-03"},
		{"from": "2026-10-01", "nodes": "n1,bad name"}, {"from": "2026-10-01", "nodes": "n1;rm"}, {"from": "2026-10-01", "type": "vm"},
		{"from": "2026-10-01", "group": "Ro Ssi"}, {"from": "2026-10-01", "q": strings.Repeat("a", 65)}, {"from": "2026-10-01", "q": "a\nb"},
		{"from": "2026-10-01", "inactive_cpu": "NaN"}, {"from": "2026-10-01", "inactive_cpu": "9"}, {"from": "2026-10-01", "oversized_days": "0"}, {"from": "2026-10-01", "inactive_net": "-1"},
	}
	for _, b := range bad {
		if _, err := parseReportParams(q(b), now); err == nil {
			t.Fatalf("accepted %v", b)
		}
	}
}

// fakeRpt is an in-memory Proxmox with RRD series ending now.
type fakeRpt struct {
	fakeGPU
	memStart, memEnd float64            // node memory % at the start/end of the period
	guestCPU         map[int][3]float64 // vmid -> cpu avg fraction, mem used fraction, net bytes/s
	tasks            []map[string]interface{}
}

const gib = int64(1) << 30

func (f fakeRpt) GetClusterResources(string) (json.RawMessage, error) {
	return json.RawMessage(`[
	 {"type":"node","node":"n1","status":"online","maxcpu":64,"maxmem":1099511627776},
	 {"type":"node","node":"n2","status":"online","maxcpu":32,"maxmem":549755813888},
	 {"type":"node","node":"n3","status":"offline","maxcpu":0,"maxmem":0},
	 {"type":"qemu","node":"n1","vmid":101,"name":"rossi-idle","tags":"cloudinit;gruppo-rossi","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":1},
	 {"type":"qemu","node":"n1","vmid":102,"name":"rossi-big","status":"running","maxcpu":16,"maxmem":68719476736,"maxdisk":1},
	 {"type":"qemu","node":"n1","vmid":103,"name":"bianchi-busy","tags":"Gruppo-Bianchi","status":"running","maxcpu":8,"maxmem":17179869184,"maxdisk":1},
	 {"type":"lxc","node":"n2","vmid":104,"name":"bianchi-ct","status":"stopped","maxcpu":2,"maxmem":2147483648,"maxdisk":1},
	 {"type":"qemu","node":"n2","vmid":105,"name":"tmpl","status":"stopped","template":1,"maxcpu":2,"maxmem":1,"maxdisk":1},
	 {"type":"storage","node":"n1","storage":"ceph","plugintype":"rbd","shared":1,"status":"available","maxdisk":1000000,"disk":400000},
	 {"type":"storage","node":"n2","storage":"ceph","plugintype":"rbd","shared":1,"status":"available","maxdisk":1000000,"disk":400000},
	 {"type":"storage","node":"n1","storage":"local","plugintype":"dir","shared":0,"status":"available","maxdisk":500,"disk":100},
	 {"type":"storage","node":"n3","storage":"local","plugintype":"dir","shared":0,"status":"unknown","maxdisk":500,"disk":100}]`), nil
}

func rrdJSON(tf string, fn func(i, n int) map[string]interface{}) json.RawMessage {
	step, n := int64(60), 1440
	switch tf {
	case "week":
		step, n = 1800, 336
	case "month":
		step, n = 1800, 1440
	case "year":
		step, n = 604800, 418
	}
	now := time.Now().Unix()
	pts := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		m := fn(i, n)
		m["time"] = now - (int64(n-1-i))*step
		pts = append(pts, m)
	}
	b, _ := json.Marshal(pts)
	return b
}

func (f fakeRpt) GetNodeRRDcf(node, tf, cf string) (json.RawMessage, error) {
	return rrdJSON(tf, func(i, n int) map[string]interface{} {
		frac := float64(i) / float64(n-1)
		return map[string]interface{}{"cpu": 0.3, "maxcpu": 64, "memused": (f.memStart + (f.memEnd-f.memStart)*frac) * 10, "memtotal": 1000.0,
			"iowait": 0.01, "pressurecpusome": 2.0, "pressureiosome": 1.0}
	}), nil
}

func (f fakeRpt) GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error) {
	g := f.guestCPU[vmid]
	return rrdJSON(tf, func(i, n int) map[string]interface{} {
		return map[string]interface{}{"cpu": g[0], "mem": g[1] * 1000, "maxmem": 1000.0, "netin": g[2], "netout": g[2]}
	}), nil
}

func (f fakeRpt) GetStorageRRD(node, storage, tf, cf string) (json.RawMessage, error) {
	return rrdJSON(tf, func(i, n int) map[string]interface{} {
		return map[string]interface{}{"total": 1000.0, "used": 300 + 500*float64(i)/float64(n-1)}
	}), nil
}

func (f fakeRpt) GetNodeTasks(node string, limit int) (json.RawMessage, error) {
	b, _ := json.Marshal(f.tasks)
	return b, nil
}

func newRpt() fakeRpt {
	now := time.Now().Unix()
	task := func(typ, status string, ago int64) map[string]interface{} {
		return map[string]interface{}{"type": typ, "status": status, "starttime": float64(now - ago), "upid": "UPID:secret:user@pam"}
	}
	return fakeRpt{memStart: 60, memEnd: 80,
		guestCPU: map[int][3]float64{101: {0.001, 0.30, 100}, 102: {0.40, 0.15, 90000}, 103: {0.50, 0.70, 80000}, 104: {0, 0, 0}},
		tasks: []map[string]interface{}{task("vzdump", "OK", 3600), task("vzdump", "ERROR: boom", 7200), task("qmstart", "OK", 400),
			task("qmstart", "WARNINGS: 2", 500), task("bad type!", "ERROR", 600), task("vzdump", "OK", 400*86400)}}
}

func yearParams() reportParams {
	now := time.Now()
	p, err := parseReportParams(func(k string) string {
		return map[string]string{"from": now.AddDate(0, 0, -200).Format("2006-01-02")}[k]
	}, now)
	if err != nil {
		panic(err)
	}
	return p
}

func TestVMsReportClassifiesAndKeepsP95FromLast28Days(t *testing.T) {
	p := yearParams()
	rep, err := buildVMsReport(newRpt(), "C", p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Meta.Timeframe != "year" || rep.Meta.P95Window != "last 28 days" || len(rep.Meta.Notes) == 0 {
		t.Fatalf("%+v", rep.Meta)
	}
	by := map[int]vmUsage{}
	for _, r := range rep.Rows {
		by[r.VMID] = r
	}
	if len(rep.Rows) != 4 || by[105].VMID != 0 {
		t.Fatalf("templates must be excluded: %d rows", len(rep.Rows))
	}
	if by[101].Flag != "inactive" || by[102].Flag != "oversized" || by[103].Flag != "" || by[104].Flag != "" {
		t.Fatalf("flags %q %q %q %q", by[101].Flag, by[102].Flag, by[103].Flag, by[104].Flag)
	}
	if by[101].CPUP95 == nil || by[102].MemP95 == nil || by[101].Group != "rossi" || by[104].Kind != "lxc" {
		t.Fatalf("%+v", by[101])
	}
	if rep.Totals.Inactive != 1 || rep.Totals.Oversized != 1 || rep.Totals.ReclaimMem != 8589934592 || rep.Totals.ReclaimCores != 4 || rep.Totals.Running != 3 {
		t.Fatalf("%+v", rep.Totals)
	}
	if len(by[102].Spark) == 0 || len(by[102].Spark) > sparkPoints {
		t.Fatalf("spark %d", len(by[102].Spark))
	}
}

func TestVMsReportFiltersAndThresholds(t *testing.T) {
	p := yearParams()
	p.Group = "bianchi"
	rep, _ := buildVMsReport(newRpt(), "C", p, time.Now())
	if len(rep.Rows) != 2 {
		t.Fatalf("group filter: %d", len(rep.Rows))
	}
	p = yearParams()
	p.Kind, p.Nodes = "qemu", map[string]bool{"n1": true}
	if rep, _ = buildVMsReport(newRpt(), "C", p, time.Now()); len(rep.Rows) != 3 {
		t.Fatalf("node+type filter: %d", len(rep.Rows))
	}
	p = yearParams()
	p.TH.InactiveCPU = 0 // nothing can be below 0
	if rep, _ = buildVMsReport(newRpt(), "C", p, time.Now()); rep.Totals.Inactive != 0 {
		t.Fatal("custom threshold ignored")
	}
}

func TestNodesReportSaturationAndForecast(t *testing.T) {
	f := newRpt()
	f.memStart, f.memEnd = 60, 80
	rep, err := buildNodesReport(f, "C", yearParams(), time.Now())
	if err != nil || len(rep.Rows) != 3 {
		t.Fatalf("%v %d", err, len(rep.Rows))
	}
	n1 := rep.Rows[0]
	if n1.Node != "n1" || !n1.Online || n1.AllocCores != 28 || n1.Mem.P95 == nil || n1.MemDaysTo85 == nil || n1.MemTrend.SlopePerDay <= 0 || n1.Saturated {
		t.Fatalf("%+v", n1)
	}
	if rep.Rows[2].Online || rep.Rows[2].CPU.N != 0 {
		t.Fatalf("offline node must have no data: %+v", rep.Rows[2])
	}
	f.memStart, f.memEnd = 88, 92
	rep, _ = buildNodesReport(f, "C", yearParams(), time.Now())
	if !rep.Rows[0].Saturated || rep.Total.Saturated != 2 {
		t.Fatalf("%+v", rep.Total)
	}
}

func TestStorageReportDeduplicatesSharedStorage(t *testing.T) {
	rep, err := buildStorageReport(newRpt(), "C", yearParams(), time.Now())
	if err != nil || len(rep.Rows) != 2 {
		t.Fatalf("%v %+v", err, rep.Rows)
	}
	var ceph storageUsage
	for _, r := range rep.Rows {
		if r.Storage == "ceph" {
			ceph = r
		}
	}
	if ceph.Total != 1000000 || ceph.UsedPct != 40 || !ceph.Trend.OK || ceph.DaysTo85 == nil || len(ceph.Series) == 0 {
		t.Fatalf("%+v", ceph)
	}
}

func TestReliabilityReportCountsAndPrivacy(t *testing.T) {
	rep, err := buildReliabilityReport(newRpt(), "C", yearParams(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// 2 online nodes see the same fake list: per node 5 tasks in the period (the 400-day-old one is outside the period)
	if rep.Totals.Tasks != 10 || rep.Totals.Failed != 4 || rep.Totals.Backups != 4 || rep.Totals.BackupsFailed != 2 || rep.Totals.FailPct != 40 {
		t.Fatalf("%+v", rep.Totals)
	}
	b, _ := json.Marshal(rep)
	for _, leak := range []string{"UPID", "secret", "user@pam", "bad type"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%q leaked: %s", leak, b)
		}
	}
	if rep.CoverageDays < 399 {
		t.Fatalf("coverage %v", rep.CoverageDays)
	}
}

func TestServiceReportEndToEnd(t *testing.T) {
	hist, err := history.New(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()
	for i := 0; i < 5; i++ {
		day := time.Now().AddDate(0, 0, -4+i)
		_ = hist.Save(history.Snapshot{Day: day.Format("2006-01-02"), Cluster: "C", TakenAt: day, GPUKnown: true,
			Nodes:  []history.NodeRow{{Node: "n1", Online: true, Cores: 64, MemBytes: 100 * gib}},
			Groups: []history.GroupRow{{Node: "n1", Group: "rossi", Kind: "qemu", VMs: 3 + i, Running: 2 + i, Cores: 12, MemBytes: 40 * gib}, {Node: "n1", Group: "altro", Kind: "qemu", VMs: 1, MemBytes: gib}, {Node: "n1", Group: "bianchi", Kind: "qemu", VMs: 1, Cores: 2, MemBytes: 10 * gib}},
			GPUs:   []history.GPURow{{Node: "n1", Model: "A100", Group: "rossi", Cards: 2}, {Node: "n1", Model: "A100", Group: "", Cards: map[bool]int{true: 0, false: 1}[i >= 2]}}})
	}
	f := newRpt()
	f.memStart, f.memEnd = 60, 84
	rep, err := buildServiceReport(f, hist, "C", yearParams(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.KPI.Nodes != 3 || rep.KPI.VMsTotal != 9 || rep.KPI.VMsRunning != 6 || rep.KPI.Groups != 2 || rep.KPI.GPUTotal != 2 || rep.KPI.GPUAssigned != 2 {
		t.Fatalf("%+v", rep.KPI)
	}
	if rep.KPI.MemAllocPct == nil || *rep.KPI.MemAllocPct != 51 || rep.KPI.StoragePct == nil || *rep.KPI.StoragePct != 40 || rep.KPI.StorageTotal != 1000000 {
		t.Fatalf("%+v", rep.KPI)
	}
	if len(rep.Series.CPU) == 0 || len(rep.Series.Mem) == 0 || len(rep.Series.Storage) == 0 || rep.Stats.Mem.N == 0 || rep.Stats.Mem.P95 != nil {
		t.Fatalf("series/stats %+v", rep.Stats)
	}
	if rep.Inventory.VMsFirst != 5 || rep.Inventory.VMsLast != 9 || rep.Inventory.SpanDays != 4 || len(rep.Inventory.Days) != 5 {
		t.Fatalf("%+v", rep.Inventory)
	}
	if rep.GroupCount != 2 || len(rep.TopGroups) != 2 || rep.TopGroups[0].Group != "rossi" || rep.GPUFullSince == "" {
		t.Fatalf("groups %+v since %q", rep.TopGroups, rep.GPUFullSince)
	}
	codes := map[string]bool{}
	for _, r := range rep.Condition.Reasons {
		codes[r.Code] = true
	}
	if rep.Condition.Level == "ok" || !codes["gpu_full"] || !codes["ram_exhaustion"] || !codes["availability"] {
		t.Fatalf("condition %+v", rep.Condition)
	}
	b, _ := json.Marshal(rep)
	for _, leak := range []string{"rossi-idle", "bianchi-busy", "UPID"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%q leaked into the manager data", leak)
		}
	}
}

func TestGPUReportAndStreak(t *testing.T) {
	d := func(day string, tot, as int) history.DayTotals {
		return history.DayTotals{Day: day, GPUKnown: true, GPUTotal: tot, GPUAssigned: as}
	}
	if s := gpuStreak([]history.DayTotals{d("1", 4, 4), d("2", 4, 3), d("3", 4, 4), d("4", 4, 4)}); s != "3" {
		t.Fatalf("%q", s)
	}
	if gpuStreak([]history.DayTotals{d("1", 4, 4), d("2", 4, 3)}) != "" || gpuStreak(nil) != "" {
		t.Fatal("last day not full / empty")
	}
	unknown := d("3", 4, 4)
	unknown.GPUKnown = false
	if gpuStreak([]history.DayTotals{d("1", 4, 4), unknown}) != "" {
		t.Fatal("unknown day breaks the streak")
	}
}

func TestReportHandlerRejectsBadRequests(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/reports/{model}", (&Handler{}).GetReport)
	for path, code := range map[string]int{
		"/api/reports/nope?from=2026-10-01":            http.StatusNotFound,
		"/api/reports/vms":                             http.StatusBadRequest,
		"/api/reports/vms?from=2020-01-01":             http.StatusBadRequest,
		"/api/reports/vms?from=2026-10-01&nodes=a%3Bb": http.StatusBadRequest,
		"/api/reports/service?from=2026-10-01&to=x":    http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != code {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	_ = fmt.Sprint()
}

func TestSharedCacheIsBounded(t *testing.T) {
	for i := 0; i < maxCacheEntries+50; i++ {
		_, _ = cephCached(fmt.Sprintf("bound-test-%d", i), time.Minute, func() (interface{}, error) { return i, nil })
	}
	cephCache.Lock()
	n := len(cephCache.m)
	_, recent := cephCache.m[fmt.Sprintf("bound-test-%d", maxCacheEntries+49)]
	_, oldest := cephCache.m["bound-test-0"]
	cephCache.Unlock()
	if n > maxCacheEntries || !recent || oldest {
		t.Fatalf("size %d recent %v oldest %v", n, recent, oldest)
	}
}

func TestServiceReportGroupRuleChangeAndSharedVMs(t *testing.T) {
	hist, err := history.New(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()
	for i := 0; i < 4; i++ {
		day := time.Now().AddDate(0, 0, -3+i)
		s := history.Snapshot{Day: day.Format("2006-01-02"), Cluster: "C", TakenAt: day,
			Nodes:  []history.NodeRow{{Node: "n1", Online: true, Cores: 64, MemBytes: 100 * gib}},
			Groups: []history.GroupRow{{Node: "n1", Group: "rossi", Kind: "qemu", VMs: 2, Running: 2, Cores: 4, MemBytes: 20 * gib}}}
		if i >= 2 { // from the third day: tags, one VM shared between rossi and bianchi, 3 VMs in the cluster
			s.GroupRule, s.HasTotals, s.TotalVMs, s.TotalRunning, s.SharedVMs = history.RuleTags, true, 3, 3, 1
			s.Groups = []history.GroupRow{
				{Node: "n1", Group: "bianchi", Kind: "qemu", VMs: 1, Running: 1, SharedVMs: 1, CoresMilli: 1500, MemBytes: 5 * gib},
				{Node: "n1", Group: "rossi", Kind: "qemu", VMs: 3, Running: 3, SharedVMs: 1, CoresMilli: 4500, MemBytes: 25 * gib}}
		}
		if err := hist.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := buildServiceReport(newRpt(), hist, "C", yearParams(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantChange := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if rep.Inventory.RuleChange != wantChange || rep.Inventory.GroupRule != "tags" || rep.Inventory.SharedVMs != 1 {
		t.Fatalf("%+v", rep.Inventory)
	}
	noted := false
	for _, n := range rep.Meta.Notes {
		noted = noted || n == "group_rule_changed"
	}
	if !noted || rep.KPI.VMsTotal != 3 { // 3 from the cluster total, not 1+3 from the group rows
		t.Fatalf("notes %v vms %d", rep.Meta.Notes, rep.KPI.VMsTotal)
	}
	if len(rep.TopGroups) != 2 || rep.TopGroups[0].Group != "rossi" || rep.TopGroups[0].Shared != 1 || rep.TopGroups[1].Cores != 2 {
		t.Fatalf("%+v", rep.TopGroups)
	}
}
