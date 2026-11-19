package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"proxmox-manager/internal/history"
)

const trendOld = "u:11111111-1111-1111-1111-111111111111"
const trendNew = "u:22222222-2222-2222-2222-222222222222"

type fakeTrend struct {
	fakeRpt
	guests              []map[string]interface{}
	inst                map[int]string
	avg, mx             json.RawMessage
	failConfig, failRRD bool
	mu                  sync.Mutex
	configCalls         map[int]int
	rrdCalls            map[int]int
	active, peak        int
}

func (f *fakeTrend) GetClusterResources(string) (json.RawMessage, error) {
	return json.Marshal(f.guests)
}
func (f *fakeTrend) enter() func() {
	f.mu.Lock()
	f.active++
	f.peak = max(f.peak, f.active)
	f.mu.Unlock()
	// Allow concurrent workers to overlap so the concurrency assertion exercises the bound.
	time.Sleep(time.Millisecond)
	return func() { f.mu.Lock(); f.active--; f.mu.Unlock() }
}
func (f *fakeTrend) GetVMConfig(_ string, id int) (json.RawMessage, error) {
	defer f.enter()()
	f.mu.Lock()
	if f.configCalls == nil {
		f.configCalls = map[int]int{}
	}
	f.configCalls[id]++
	f.mu.Unlock()
	if f.failConfig {
		return nil, errors.New("config unavailable")
	}
	return json.Marshal(map[string]string{"smbios1": "uuid=" + strings.TrimPrefix(f.inst[id], "u:")})
}
func (f *fakeTrend) GetGuestRRDcf(_ string, _ string, id int, tf, cf string) (json.RawMessage, error) {
	defer f.enter()()
	f.mu.Lock()
	if f.rrdCalls == nil {
		f.rrdCalls = map[int]int{}
	}
	f.rrdCalls[id]++
	f.mu.Unlock()
	if tf != "year" || (cf != "AVERAGE" && cf != "MAX") {
		return nil, errors.New("unexpected RRD request")
	}
	if f.failRRD {
		return nil, errors.New("rrd unavailable")
	}
	raw := f.avg
	if cf == "MAX" {
		raw = f.mx
	}
	if raw == nil {
		raw = json.RawMessage(`[]`)
	}
	return raw, nil
}

func trendGuest(id int, name, kind string) map[string]interface{} {
	return map[string]interface{}{"type": kind, "node": "n1", "vmid": id, "name": name, "status": "running", "maxcpu": 8, "maxmem": 16 * gib}
}

func trendStored(day string, id int, inst, name, kind string) history.VMUseDay {
	return history.VMUseDay{Day: day, Cluster: "C", VMID: id, Inst: inst, Name: name, Kind: kind, Node: "n1", Group: "group", Cores: 8, MemBytes: 16 * gib,
		Points: 48, StepS: 1800, CPUAvg: 20, CPUP95: 30, CPUMax: 40, MemAvg: 30, MemP95: 40, MemMax: 50, NetIn: 10, NetOut: 20, DiskRead: 30, DiskWrite: 40}
}

func trendStore(t *testing.T, rows []history.VMUseDay) *history.Store {
	t.Helper()
	store, err := history.New(filepath.Join(t.TempDir(), "trend.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	days := map[string][]history.VMUseDay{}
	for _, r := range rows {
		days[r.Day] = append(days[r.Day], r)
	}
	for day, rows := range days {
		if err := store.SaveVMUseDay(day, "C", rows, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestTrendInstanceIdentity(t *testing.T) {
	p := trendTestParams(t)
	store := trendStore(t, []history.VMUseDay{
		trendStored("2026-10-01", 101, trendOld, "old-vm", "qemu"),
		trendStored("2026-11-18", 101, trendNew, "new-vm", "qemu"),
		trendStored("2026-10-01", 102, "l:2026-10-01", "old-container", "lxc"),
		trendStored("2026-11-18", 102, "l:2026-11-18", "new-container", "lxc"),
		trendStored("2026-11-18", 103, trendOld, "gone-vm", "qemu"),
	})
	f := &fakeTrend{guests: []map[string]interface{}{trendGuest(101, "new-vm", "qemu"), trendGuest(102, "new-container", "lxc")}, inst: map[int]string{101: trendNew}}
	rep, err := buildTrendReport(f, store, "C", p, p.To)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 5 || rep.Totals.Deleted != 3 || f.configCalls[101] != 1 || f.configCalls[102] != 0 {
		t.Fatalf("rows=%+v calls=%v", rep.Rows, f.configCalls)
	}
	for _, r := range rep.Rows {
		switch r.Name {
		case "old-vm", "old-container", "gone-vm":
			if r.Status != "deleted" || r.State != "deleted" {
				t.Fatalf("deleted: %+v", r)
			}
		case "new-vm", "new-container":
			if r.Status != "running" {
				t.Fatalf("current: %+v", r)
			}
		default:
			t.Fatalf("wrong name: %+v", r)
		}
	}
	if f.rrdCalls[101] != 2 || f.rrdCalls[102] != 2 || f.rrdCalls[103] != 0 {
		t.Fatalf("RRD only for current: %v", f.rrdCalls)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{trendOld, trendNew, "l:2026-", `"inst"`, `"instance"`, `"cpu_p95"`, `"mem_p95"`} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("leaked %q: %s", leak, b)
		}
	}
	if rep.Meta.StoredFrom == nil || *rep.Meta.StoredFrom != "2026-10-01" || !rep.Meta.CurrentPartial || len(rep.Meta.Months) != 6 {
		t.Fatalf("meta %+v", rep.Meta)
	}
	for _, note := range rep.Meta.Notes {
		if strings.Contains(note, " ") {
			t.Fatalf("sentence in notes: %q", note)
		}
	}
	// Searching for the old name must retain only the deleted instance.
	p.Query = "old-vm"
	rep, err = buildTrendReport(f, store, "C", p, p.To)
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Name != "old-vm" || rep.Rows[0].Status != "deleted" {
		t.Fatalf("old filter: %+v %v", rep, err)
	}
	// A current rename must not turn the same UUID into a deleted historical row.
	f.guests[0]["name"] = "renamed-vm"
	p.Query = "new-vm"
	rep, err = buildTrendReport(f, store, "C", p, p.To)
	if err != nil || len(rep.Rows) != 0 {
		t.Fatalf("rename filter: %+v %v", rep, err)
	}
	// Latest stored LXC identity remains available when it predates the selected window.
	p.From = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	p.Query = ""
	p.Kind = "lxc"
	rep, err = buildTrendReport(f, store, "C", p, p.To)
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Status != "running" || f.configCalls[102] != 0 {
		t.Fatalf("LXC latest: %+v %v", rep, err)
	}
}

func TestTrendLoaderMergeAndErrors(t *testing.T) {
	p := trendTestParams(t)
	p.From = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	r := trendStored("2026-10-15", 101, trendNew, "current-vm", "qemu")
	store := trendStore(t, []history.VMUseDay{r})
	ts := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).Unix()
	raw, err := json.Marshal([]map[string]interface{}{{"time": ts, "cpu": .1, "mem": float64(gib), "maxmem": float64(16 * gib), "netin": 2, "netout": 3, "diskread": 4, "diskwrite": 5}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTrend{guests: []map[string]interface{}{trendGuest(101, "current-vm", "qemu")}, inst: map[int]string{101: trendNew}, avg: raw, mx: raw}
	rep, err := buildTrendReport(f, store, "C", p, p.To)
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Months[0].Source != "mixed" || rep.Rows[0].Months[0].DiskBytes == nil {
		t.Fatalf("loader merge: %+v %v", rep, err)
	}
	f.failRRD = true
	if _, err := buildTrendReport(f, store, "C", p, p.To); err == nil {
		t.Fatal("failed RRD classified as off")
	}
	f.failRRD = false
	f.failConfig = true
	if _, err := buildTrendReport(f, store, "C", p, p.To); err == nil {
		t.Fatal("failed identity classified as deleted")
	}
	f.failConfig = false
	rep, err = buildTrendReport(f, nil, "C", p, p.To)
	if err != nil || rep.Meta.StoredFrom != nil || rep.Rows[0].Months[0].Source != "rrd" {
		t.Fatalf("no history: %+v %v", rep, err)
	}
	// All filters use the same conventions as the other reports.
	for _, change := range []func(*reportParams){
		func(p *reportParams) { p.Nodes = map[string]bool{"n2": true} }, func(p *reportParams) { p.Kind = "lxc" },
		func(p *reportParams) { p.Group = "missing" }, func(p *reportParams) { p.Query = "missing" },
	} {
		pp := p
		change(&pp)
		rep, err = buildTrendReport(f, store, "C", pp, p.To)
		if err != nil || len(rep.Rows) != 0 {
			t.Fatalf("filter: %+v %v", rep, err)
		}
	}
}

func TestTrendGuestCapAndWorkers(t *testing.T) {
	p := trendTestParams(t)
	f := &fakeTrend{inst: map[int]string{}}
	for i := 1; i <= maxReportGuests; i++ {
		f.guests = append(f.guests, trendGuest(i, fmt.Sprintf("vm-%d", i), "qemu"))
		f.inst[i] = trendNew
	}
	rep, err := buildTrendReport(f, nil, "C", p, p.To)
	if err != nil || len(rep.Rows) != 600 || f.peak > 8 || f.peak < 2 {
		t.Fatalf("bounds rows=%d peak=%d err=%v", len(rep.Rows), f.peak, err)
	}
	for _, n := range f.configCalls {
		if n != 1 {
			t.Fatalf("config repeated %d", n)
		}
	}
	f.guests = append(f.guests, trendGuest(601, "extra", "qemu"))
	before := len(f.configCalls)
	if _, err = buildTrendReport(f, nil, "C", p, p.To); err == nil || err.Error() != errTooManyGuests || len(f.configCalls) != before {
		t.Fatalf("current cap: %v", err)
	}
	// Deleted instances count against the same cap even when all share one VMID.
	stored := make([]history.VMUseDay, 601)
	for i := range stored {
		stored[i] = trendStored("2026-11-18", 101, fmt.Sprintf("u:%08x-1111-1111-1111-111111111111", i), "old", "qemu")
	}
	store := trendStore(t, stored)
	if _, err = buildTrendReport(&fakeTrend{}, store, "C", p, p.To); err == nil || err.Error() != errTooManyGuests {
		t.Fatalf("stored cap: %v", err)
	}
}

func TestTrendParamsAndHTTPValidation(t *testing.T) {
	now := trendTestParams(t).To
	base, err := parseTrendParams(url.Values{}.Get, now)
	if err != nil {
		t.Fatal(err)
	}
	if base.Months != 6 || base.Trend != defaultTrendThresholds() {
		t.Fatalf("defaults: %+v", base)
	}
	specs := []struct {
		name   string
		lo, hi int
		bad    []string
	}{
		{"months", 1, 12, []string{"0", "13", "1.0", "1.5"}},
		{"abandon_months", 1, 12, []string{"0", "13", "1.0", "1.5"}},
		{"abandon_cpu", 0, 50, []string{"-1", "50.01"}},
		{"abandon_net", 0, 1048576, []string{"-1", "1048577"}},
		{"over_mem", 1, 90, []string{"0", "90.01"}},
		{"over_cpu", 1, 90, []string{"0", "90.01"}},
		{"margin", 0, 200, []string{"-1", "201"}},
	}
	router := chi.NewRouter()
	router.Get("/api/reports/{model}", (&Handler{}).GetReport)
	for _, s := range specs {
		for _, v := range append(s.bad, "NaN", "Inf", "-Inf", "x", "99999999999999999999999999999999") {
			q := url.Values{s.name: {v}}
			if _, err := parseTrendParams(q.Get, now); err == nil {
				t.Fatalf("accepted %s=%s", s.name, v)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/api/reports/trend?"+q.Encode(), nil))
			if w.Code != 400 || !strings.Contains(w.Body.String(), s.name) {
				t.Fatalf("%s=%s: %d %s", s.name, v, w.Code, w.Body.String())
			}
		}
		for _, v := range []int{s.lo, s.hi} {
			p, err := parseTrendParams(url.Values{s.name: {fmt.Sprint(v)}}.Get, now)
			if err != nil || p.key() == base.key() {
				t.Fatalf("boundary %s=%d key or error: %v", s.name, v, err)
			}
		}
	}
	for _, q := range []url.Values{{"nodes": {"bad node"}}, {"type": {"invalid"}}, {"group": {"bad group"}}, {"q": {strings.Repeat("x", 65)}}} {
		if _, err := parseTrendParams(q.Get, now); err == nil {
			t.Fatalf("filter accepted %v", q)
		}
	}
	// Different date inputs are ignored and therefore share the same trend cache entry.
	p, err := parseTrendParams(url.Values{"from": {"bad"}, "to": {"bad"}}.Get, now)
	if err != nil || p.key() != base.key() {
		t.Fatalf("date precedence: %+v %v", p, err)
	}
}

func TestServiceTrendPrivacyAndPeriod(t *testing.T) {
	p := trendTestParams(t)
	p.From = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	stored := []history.VMUseDay{}
	for _, month := range []string{"09", "10", "11"} {
		r := trendStored("2026-"+month+"-01", 101, trendNew, "private-person-vm", "qemu")
		r.CPUAvg = 1
		r.CPUMax = 5
		r.MemMax = 10
		stored = append(stored, r)
	}
	store := trendStore(t, stored)
	f := &fakeTrend{fakeRpt: newRpt(), guests: []map[string]interface{}{trendGuest(101, "private-person-vm", "qemu")}, inst: map[int]string{101: trendNew}}
	rep, err := buildServiceReport(f, store, "C", p, p.To)
	if err != nil {
		t.Fatal(err)
	}
	if rep.VMTrend.Abandoned != 1 || rep.VMTrend.ReclaimCores != 8 || rep.VMTrend.ReclaimMem != 16*gib {
		t.Fatalf("manager summary: %+v", rep.VMTrend)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"private-person-vm", trendNew, `"vmid"`, `"node"`, `"name"`, `"inst"`, `"n1"`} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("manager identity %s: %s", leak, b)
		}
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var summary map[string]interface{}
	if err := json.Unmarshal(raw["vm_trend"], &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary) != 5 {
		t.Fatalf("manager fields: %v", summary)
	}
	pp := serviceTrendParams(p)
	if pp.Months != 3 || !reflect.DeepEqual(trendMonths(pp.From, pp.To), []string{"2026-09", "2026-10", "2026-11"}) {
		t.Fatalf("service period: %+v", pp)
	}
	p.From = time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	pp = serviceTrendParams(p)
	if pp.Months != 12 || pp.From.Format("2006-01-02") != "2025-12-01" {
		t.Fatalf("service cap: %+v", pp)
	}
}
