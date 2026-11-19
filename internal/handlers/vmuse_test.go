package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// fakeVMUse serves /cluster/resources, VM configs and month RRDs (30-minute points over 30 days).
type fakeVMUse struct {
	mu      sync.Mutex
	now     time.Time
	guests  []map[string]interface{}
	uuid    map[int]string
	onFrom  map[int]time.Time // first instant with data (zero = always)
	offFrom map[int]time.Time // first instant without data again (zero = never)
	cpu     map[int]float64   // fraction of the allocated vCPUs
	fail    map[int]bool
	rrdHits int
}

func (f *fakeVMUse) GetClusterResources(string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(f.guests)
}

func (f *fakeVMUse) GetVMConfig(node string, vmid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := map[string]interface{}{"meta": "creation-qemu=9.2.0,ctime=1700000000"}
	if u := f.uuid[vmid]; u != "" {
		cfg["smbios1"] = "uuid=" + u
	}
	return json.Marshal(cfg)
}

func (f *fakeVMUse) GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rrdHits++
	if f.fail[vmid] {
		return nil, errors.New("rrd unavailable")
	}
	if tf != "month" {
		return nil, errors.New("unexpected timeframe " + tf)
	}
	end := f.now.Unix() / 1800 * 1800
	pts := []map[string]interface{}{}
	for t := end - 30*86400; t < end; t += 1800 {
		p := map[string]interface{}{"time": float64(t)}
		on := (f.onFrom[vmid].IsZero() || t >= f.onFrom[vmid].Unix()) && (f.offFrom[vmid].IsZero() || t < f.offFrom[vmid].Unix())
		if on {
			c := f.cpu[vmid]
			mem := 2.0 * (1 << 30)
			if cf == "MAX" {
				c, mem = math.Min(1, c*2), 3.0*(1<<30)
			}
			p["cpu"], p["mem"], p["maxmem"] = c, mem, 4.0*(1<<30)
			p["netin"], p["netout"], p["diskread"], p["diskwrite"] = 1000.0, 500.0, 200.0, 100.0
		}
		pts = append(pts, p)
	}
	return json.Marshal(pts)
}

func vmGuest(vmid int, kind, name string) map[string]interface{} {
	return map[string]interface{}{"type": kind, "vmid": float64(vmid), "node": "pve1", "name": name, "status": "running",
		"maxcpu": 4.0, "maxmem": float64(4 << 30), "maxdisk": float64(32 << 30), "tags": "gruppo-rossi"}
}

func newVMUseTest(t *testing.T) (*vmUseCollector, *fakeVMUse, *history.Store, *time.Time) {
	t.Helper()
	store, err := history.New(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	f := &fakeVMUse{now: now, uuid: map[int]string{}, onFrom: map[int]time.Time{}, offFrom: map[int]time.Time{},
		cpu: map[int]float64{}, fail: map[int]bool{}}
	v := &vmUseCollector{now: func() time.Time { return now }, newClient: func(config.ClusterConfig) vmUseAPI { return f }}
	return v, f, store, &now
}

var vmUseCluster = config.ClusterConfig{Label: "CALCOLO", URL: "https://pve.example:8006"}

func rowsByVM(t *testing.T, s *history.Store, from, to string) map[int][]history.VMUseDay {
	t.Helper()
	rows, err := s.VMUseDays("CALCOLO", from, to)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int][]history.VMUseDay{}
	for _, r := range rows {
		out[r.VMID] = append(out[r.VMID], r)
	}
	return out
}

func TestVMUseBackfillAndCases(t *testing.T) {
	v, f, store, now := newVMUseTest(t)
	day := func(back int) time.Time {
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.Local).AddDate(0, 0, -back)
	}
	f.guests = []map[string]interface{}{
		vmGuest(101, "qemu", "rossi-vm-1"), // always on
		vmGuest(102, "qemu", "rossi-vm-2"), // never had data in the window
		vmGuest(103, "qemu", "rossi-vm-3"), // created 3 days ago
		vmGuest(105, "qemu", "rossi-vm-5"), // off since yesterday
		vmGuest(104, "lxc", "rossi-ct"),    // container: instance by continuity
		{"type": "qemu", "vmid": 900.0, "node": "pve1", "name": "ubuntu-template", "template": 1.0},
	}
	f.uuid[101], f.uuid[102], f.uuid[103], f.uuid[105] = "11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "55555555-5555-5555-5555-555555555555"
	f.cpu[101], f.cpu[103], f.cpu[105], f.cpu[104] = 0.25, 0.5, 0.1, 0.02
	f.onFrom[102] = now.Add(time.Hour) // never on in the window
	f.onFrom[103] = day(3)
	f.offFrom[105] = day(1)

	if n := v.collectCluster(store, vmUseCluster); n != vmUseBackfillDays {
		t.Fatalf("saved %d days, want %d", n, vmUseBackfillDays)
	}
	first, last := day(vmUseBackfillDays).Format("2006-01-02"), day(1).Format("2006-01-02")
	got := rowsByVM(t, store, first, last)
	if len(got[101]) != vmUseBackfillDays {
		t.Fatalf("101: %d rows", len(got[101]))
	}
	r := got[101][len(got[101])-1]
	if r.Inst != "u:11111111-1111-1111-1111-111111111111" || r.Points != 48 || r.CPUAvg != 25 || r.CPUP95 != 25 || r.CPUMax != 50 ||
		r.MemAvg != 50 || r.MemMax != 75 || r.NetIn != 48*1800*1000 || r.DiskWrite != 48*1800*100 || r.Group != "rossi" || r.Cores != 4 {
		t.Fatalf("101 aggregate: %+v", r)
	}
	if len(got[102]) != 0 {
		t.Fatalf("a VM with no data must not get invented 'off' days: %+v", got[102])
	}
	if len(got[103]) != 3 || got[103][0].Day != day(3).Format("2006-01-02") {
		t.Fatalf("103 (created 3 days ago): %+v", got[103])
	}
	if y := got[105][len(got[105])-1]; y.Day != last || y.Points != 0 || y.CPUAvg != -1 || y.MemMax != -1 {
		t.Fatalf("105 off yesterday must be a row without data: %+v", y)
	}
	ct := got[104]
	if len(ct) != vmUseBackfillDays || ct[0].Inst != "l:"+first || ct[len(ct)-1].Inst != ct[0].Inst {
		t.Fatalf("LXC instance must stay the same across consecutive days: first %+v last %+v", ct[0], ct[len(ct)-1])
	}
	if _, ok := got[900]; ok {
		t.Fatal("template stored")
	}
	// second run: nothing missing, no RRD read
	hits := f.rrdHits
	if n := v.collectCluster(store, vmUseCluster); n != 0 || f.rrdHits != hits {
		t.Fatalf("second run saved %d days and read %d RRDs", n, f.rrdHits-hits)
	}
}

func TestVMUseFailedReadIsRetried(t *testing.T) {
	v, f, store, now := newVMUseTest(t)
	f.guests = []map[string]interface{}{vmGuest(101, "qemu", "a"), vmGuest(106, "qemu", "b")}
	f.uuid[101], f.uuid[106] = "11111111-1111-1111-1111-111111111111", "66666666-6666-6666-6666-666666666666"
	f.cpu[101], f.cpu[106] = 0.2, 0.2
	f.fail[106] = true
	// only the oldest day (about to leave the RRD window) is saved without the failed VM
	if n := v.collectCluster(store, vmUseCluster); n != 1 {
		t.Fatalf("with a failed read only the oldest day may be saved, got %d", n)
	}
	f.fail[106] = false
	if n := v.collectCluster(store, vmUseCluster); n != vmUseBackfillDays-1 {
		t.Fatalf("retry saved %d days", n)
	}
	y, m, d := now.Date()
	yesterday := time.Date(y, m, d, 0, 0, 0, 0, time.Local).AddDate(0, 0, -1).Format("2006-01-02")
	if got := rowsByVM(t, store, yesterday, yesterday); len(got[106]) != 1 {
		t.Fatal("the VM that failed first was not stored on retry")
	}
}

func TestVMUseReusedVMIDAndRenamedLXC(t *testing.T) {
	v, f, store, now := newVMUseTest(t)
	f.guests = []map[string]interface{}{vmGuest(101, "qemu", "new-vm"), vmGuest(104, "lxc", "renamed-ct")}
	f.uuid[101] = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	f.cpu[101], f.cpu[104] = 0.2, 0.2
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	before := today.AddDate(0, 0, -vmUseBackfillDays-1).Format("2006-01-02")
	// history already holds the deleted VM 101 (other uuid) and the LXC 104 with its old name
	old := []history.VMUseDay{
		{Day: before, Cluster: "CALCOLO", VMID: 101, Inst: "u:bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Kind: "qemu", Name: "old-vm", Group: "altro",
			Node: "pve1", StepS: 1800, Points: 48, CPUAvg: 1, CPUP95: 1, CPUMax: 1, MemAvg: 1, MemP95: 1, MemMax: 1},
		{Day: before, Cluster: "CALCOLO", VMID: 104, Inst: "l:" + before, Kind: "lxc", Name: "old-ct", Group: "altro",
			Node: "pve1", StepS: 1800, Points: 48, CPUAvg: 1, CPUP95: 1, CPUMax: 1, MemAvg: 1, MemP95: 1, MemMax: 1},
	}
	if err := store.SaveVMUseDay(before, "CALCOLO", old, *now); err != nil {
		t.Fatal(err)
	}
	v.collectCluster(store, vmUseCluster)
	first := today.AddDate(0, 0, -vmUseBackfillDays).Format("2006-01-02")
	got := rowsByVM(t, store, before, today.Format("2006-01-02"))
	insts := map[string]bool{}
	for _, r := range got[101] {
		insts[r.Inst] = true
	}
	if len(insts) != 2 || !insts["u:aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"] {
		t.Fatalf("a reused VMID must keep two instances apart: %v", insts)
	}
	for _, r := range got[104] {
		if r.Day == first && r.Inst != "l:"+first {
			t.Fatalf("a renamed LXC after a gap must start a new instance: %+v", r)
		}
	}
}

func TestVMInstance(t *testing.T) {
	for in, want := range map[string]string{
		"uuid=35EF9008-6BA3-42C9-808B-904183FBEF23":                "u:35ef9008-6ba3-42c9-808b-904183fbef23",
		"manufacturer=x,uuid=35ef9008-6ba3-42c9-808b-904183fbef23": "u:35ef9008-6ba3-42c9-808b-904183fbef23",
		"uuid=not-a-uuid": "", "": "", "uuid=35ef9008-6ba3-42c9-808b-904183fbef23x": "",
	} {
		if got := vmInstance(map[string]interface{}{"smbios1": in}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if vmInstance(map[string]interface{}{"meta": "ctime=1"}) != "" {
		t.Error("ctime must not be used")
	}
	_ = fmt.Sprintf
}
