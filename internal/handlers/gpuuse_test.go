package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

type fakeGPUGuest struct {
	out     string
	code    int
	execErr error
	delay   time.Duration
}

type fakeGPUAPI struct {
	mu          sync.Mutex
	res         []map[string]interface{}
	guests      map[int]*fakeGPUGuest
	probeOutput string
	helper      bool
	pid         int
	results     map[int]guestResult
	runs        map[int]int
	probes      map[int]int
	attempts    map[int]int
	inFlight    int
	maxInFlight int
}

func newFakeGPUAPI() *fakeGPUAPI {
	return &fakeGPUAPI{guests: map[int]*fakeGPUGuest{}, results: map[int]guestResult{}, runs: map[int]int{},
		probes: map[int]int{}, attempts: map[int]int{}}
}

func (f *fakeGPUAPI) addVM(vmid int, status, name, tags string, g *fakeGPUGuest) {
	f.res = append(f.res, map[string]interface{}{"type": "qemu", "vmid": float64(vmid), "node": "pve1", "status": status,
		"name": name, "tags": tags})
	f.guests[vmid] = g
}

func (f *fakeGPUAPI) GetClusterResources(string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(f.res)
}

func (f *fakeGPUAPI) AgentExec(node string, vmid int, args []string) (json.RawMessage, error) {
	f.mu.Lock()
	g := f.guests[vmid]
	f.attempts[vmid]++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	if g.delay > 0 {
		time.Sleep(g.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
	if g.execErr != nil {
		return nil, g.execErr
	}
	f.pid++
	if len(args) >= 3 && strings.Contains(args[2], `[ -x "$1" ]`) {
		f.probes[vmid]++
		out := "INLINE\n"
		if f.probeOutput != "" {
			out = f.probeOutput
		}
		if f.helper {
			out = "HELPER\n"
		}
		f.results[f.pid] = guestResult{Out: out}
	} else {
		if len(args) >= 3 && args[0] == "/bin/sh" && args[2] != gpuUseScriptSrc {
			return nil, errors.New("unexpected script")
		}
		if len(args) > 4 {
			return nil, errors.New("gpuuse.sh must get no arguments")
		}
		f.runs[vmid]++
		f.results[f.pid] = guestResult{Out: g.out, ExitCode: g.code}
	}
	return json.RawMessage(fmt.Sprintf(`{"pid":%d}`, f.pid)), nil
}

func (f *fakeGPUAPI) AgentExecStatus(node string, vmid, pid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.results[pid]
	b, _ := json.Marshal(map[string]interface{}{"exited": 1, "exitcode": r.ExitCode, "out-data": r.Out})
	return b, nil
}

func (f *fakeGPUAPI) count(m map[int]int, vmid int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[vmid]
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

const (
	gpuLineBusy  = "00000000:01:00.0, NVIDIA A100-PCIE-40GB, 85, 30000, 40960, 65, 210.5"
	gpuLineQuiet = "00000000:01:00.0, NVIDIA A100-PCIE-40GB, 0, 3, 40960, 35, 40"
)

func gpuTestInventory(vmids ...int) GPUInventory {
	n := GPUNode{Node: "pve1", Online: true}
	for _, id := range vmids {
		n.Cards = append(n.Cards, GPUCard{Addr: fmt.Sprintf("0000:%02x:00", id%256), Name: "NVIDIA Corporation GA100 [A100 PCIe 40GB]",
			Status: "assigned", Uses: []GPUUse{{VMID: id, Status: "running"}}})
	}
	return GPUInventory{Nodes: []GPUNode{n}}
}

func newTestCollector(t *testing.T, f *fakeGPUAPI, inv GPUInventory) (*gpuUseCollector, *testClock, *history.Store) {
	t.Helper()
	old := guestPollInterval
	guestPollInterval = time.Millisecond
	t.Cleanup(func() { guestPollInterval = old })
	store, err := history.New(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clk := &testClock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.Local)}
	g := newGPUUseCollector()
	g.now = clk.now
	g.store = store
	g.newClient = func(config.ClusterConfig) gpuUseAPI { return f }
	g.inventory = func(int, config.ClusterConfig) (GPUInventory, error) { return inv, nil }
	return g, clk, store
}

var testClusters = []config.ClusterConfig{{Label: "CALCOLO", URL: "https://pve.example:8006"}}

func liveState(g *gpuUseCollector) map[int]GPUUseLive {
	out := map[int]GPUUseLive{}
	for _, v := range g.snapshotLive("CALCOLO") {
		out[v.VMID] = v
	}
	return out
}

func TestGPUUseStates(t *testing.T) {
	f := newFakeGPUAPI()
	f.addVM(101, "running", "rossi-gpu-1", "gruppo-rossi", &fakeGPUGuest{out: gpuLineBusy + "\n"})
	f.addVM(102, "running", "rossi-gpu-2", "gruppo-rossi", &fakeGPUGuest{out: gpuLineQuiet + "\n"})
	f.addVM(103, "stopped", "bianchi-gpu", "", &fakeGPUGuest{})
	f.addVM(104, "running", "nodriver", "", &fakeGPUGuest{code: 3})
	f.addVM(105, "running", "mismatch", "", &fakeGPUGuest{code: 4})
	f.addVM(106, "running", "noagent", "", &fakeGPUGuest{execErr: errors.New("No QEMU guest agent configured")})
	f.addVM(107, "running", "hostile", "", &fakeGPUGuest{out: "<script>alert(1)</script>\n" + strings.Repeat("x,", 50) + "\n"})
	f.addVM(108, "running", "no-gpu-vm", "", &fakeGPUGuest{out: gpuLineBusy})
	g, _, _ := newTestCollector(t, f, gpuTestInventory(101, 102, 103, 104, 105, 106, 107))
	g.cycle(testClusters)

	live := liveState(g)
	want := map[int]string{101: gpuStBusy, 102: gpuStQuiet, 103: gpuStVMOff, 104: gpuStNoDriver, 105: gpuStDriverError,
		106: gpuStNoData, 107: gpuStNoData}
	if len(live) != len(want) {
		t.Fatalf("live has %d VMs, want %d (VM without a card must not be read): %+v", len(live), len(want), live)
	}
	for id, st := range want {
		if live[id].State != st {
			t.Errorf("VM %d: state %q, want %q", id, live[id].State, st)
		}
	}
	if f.count(f.attempts, 103) != 0 || f.count(f.attempts, 108) != 0 {
		t.Error("a stopped VM or a VM without a card was asked")
	}
	if c := live[101].Cards; len(c) != 1 || *c[0].Util != 85 || c[0].Name != "NVIDIA A100-PCIE-40GB" {
		t.Errorf("cards of 101: %+v", c)
	}
	if live[107].Rejected != 2 || live[107].Cards == nil {
		t.Errorf("hostile output: %+v", live[107])
	}
	if live[101].Model != "NVIDIA Corporation GA100 [A100 PCIe 40GB]" || live[101].Stale {
		t.Errorf("live meta: %+v", live[101])
	}
	// the JSON the page gets never has null card lists
	b, _ := json.Marshal(g.snapshotLive("CALCOLO"))
	if strings.Contains(string(b), `"cards":null`) {
		t.Errorf("null cards in %s", b)
	}
}

func TestGPUUseBackoffAndProbeCache(t *testing.T) {
	f := newFakeGPUAPI()
	f.addVM(101, "running", "ok", "", &fakeGPUGuest{out: gpuLineBusy})
	f.addVM(106, "running", "noagent", "", &fakeGPUGuest{execErr: errors.New("not running")})
	g, clk, _ := newTestCollector(t, f, gpuTestInventory(101, 106))
	for i := 0; i < 3; i++ {
		g.cycle(testClusters)
		clk.add(gpuUseEvery)
	}
	if n := f.count(f.attempts, 106); n != 3 {
		t.Fatalf("failing VM asked %d times in 3 cycles, want 3", n)
	}
	g.cycle(testClusters)
	if n := f.count(f.attempts, 106); n != 3 {
		t.Fatalf("paused VM was asked again (%d)", n)
	}
	if st := liveState(g)[106].State; st != gpuStPaused {
		t.Fatalf("paused VM state %q", st)
	}
	if p, r := f.count(f.probes, 101), f.count(f.runs, 101); p != 1 || r != 4 {
		t.Fatalf("helper probe must be cached: probes=%d runs=%d", p, r)
	}
	clk.add(gpuUseSkipFor)
	g.cycle(testClusters)
	if n := f.count(f.attempts, 106); n != 4 {
		t.Fatalf("after the pause the VM must be asked again (%d)", n)
	}
	if p := f.count(f.probes, 101); p != 2 {
		t.Fatalf("probe not refreshed after %s (%d)", gpuUseProbeTTL, p)
	}
	// a VM that answers again leaves the failure count
	f.mu.Lock()
	f.guests[106].execErr = nil
	f.guests[106].out = gpuLineQuiet
	f.mu.Unlock()
	// it failed once more after the first pause: paused again for a full period, then asked
	clk.add(gpuUseEvery)
	g.cycle(testClusters)
	if n := f.count(f.attempts, 106); n != 4 {
		t.Fatalf("a VM that failed right after its pause must be paused again (%d)", n)
	}
	clk.add(gpuUseSkipFor)
	g.cycle(testClusters)
	g.mu.Lock()
	_, still := g.fail["0|106"]
	g.mu.Unlock()
	if still || liveState(g)[106].State != gpuStQuiet {
		t.Fatal("recovered VM still marked as failing")
	}
}

func TestGPUUseHelperMode(t *testing.T) {
	f := newFakeGPUAPI()
	f.helper = true
	f.addVM(101, "running", "ok", "", &fakeGPUGuest{out: gpuLineBusy})
	g, _, _ := newTestCollector(t, f, gpuTestInventory(101))
	g.cycle(testClusters)
	if st := liveState(g)[101].State; st != gpuStBusy || f.count(f.runs, 101) != 1 {
		t.Fatalf("helper run: state %q runs %d", st, f.count(f.runs, 101))
	}
}

func TestGPUUseConcurrencyBound(t *testing.T) {
	f := newFakeGPUAPI()
	ids := []int{}
	for id := 201; id <= 212; id++ {
		f.addVM(id, "running", fmt.Sprintf("vm-%d", id), "", &fakeGPUGuest{out: gpuLineBusy, delay: 20 * time.Millisecond})
		ids = append(ids, id)
	}
	g, _, _ := newTestCollector(t, f, gpuTestInventory(ids...))
	g.cycle(testClusters)
	if f.maxInFlight > gpuUseWorkers || f.maxInFlight < 2 {
		t.Fatalf("max concurrent agent calls %d, want 2..%d", f.maxInFlight, gpuUseWorkers)
	}
	if len(liveState(g)) != len(ids) {
		t.Fatal("not every VM was sampled")
	}
}

func TestGPUUseNoOverlapAndSharedLock(t *testing.T) {
	f := newFakeGPUAPI()
	f.addVM(101, "running", "ok", "", &fakeGPUGuest{out: gpuLineBusy})
	g, _, _ := newTestCollector(t, f, gpuTestInventory(101))
	g.running.Store(true)
	g.cycle(testClusters)
	if f.count(f.attempts, 101) != 0 {
		t.Fatal("a cycle ran while another was in progress")
	}
	g.running.Store(false)
	if !dmesgAcquire("0|pve1|101") {
		t.Fatal("lock busy")
	}
	g.cycle(testClusters)
	dmesgRelease("0|pve1|101")
	if f.count(f.attempts, 101) != 0 || liveState(g)[101].State != gpuStNoData {
		t.Fatal("the VM was read while another guest command held its lock")
	}
}

func TestGPUUseStaleLive(t *testing.T) {
	f := newFakeGPUAPI()
	f.addVM(101, "running", "ok", "", &fakeGPUGuest{out: gpuLineBusy})
	g, clk, _ := newTestCollector(t, f, gpuTestInventory(101))
	g.cycle(testClusters)
	clk.add(gpuUseStaleAfter + time.Second)
	if !liveState(g)[101].Stale {
		t.Fatal("old sample not marked stale")
	}
}

func TestGPUUseAggregateAndDayRollover(t *testing.T) {
	f := newFakeGPUAPI()
	f.addVM(101, "running", "rossi-gpu-1", "gruppo-rossi", &fakeGPUGuest{out: gpuLineBusy})
	f.addVM(103, "stopped", "bianchi-gpu", "", &fakeGPUGuest{})
	g, clk, store := newTestCollector(t, f, gpuTestInventory(101, 103))
	g.cycle(testClusters) // busy
	clk.add(gpuUseEvery)
	g.cycle(testClusters) // busy
	f.mu.Lock()
	f.guests[101].out = gpuLineQuiet
	f.mu.Unlock()
	clk.add(gpuUseEvery)
	g.cycle(testClusters) // quiet
	// next day: the cycle flushes the finished day and drops it from memory
	clk.add(14 * time.Hour)
	g.cycle(testClusters)

	rows, err := store.GPUUseDays("CALCOLO", "2026-11-19", "2026-11-19")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	memBusy := 30000.0 / 40960 * 100
	memQuiet := 3.0 / 40960 * 100
	if r.VMID != 101 || r.Group != "rossi" || r.Node != "pve1" || r.Cards != 1 || r.Expected != 3 || r.Valid != 3 ||
		r.BusyMinutes != 10 || !near(r.UtilAvg, 170.0/3) || r.UtilMax != 85 || r.UtilP95 != 85 ||
		!near(r.MemAvgPct, (2*memBusy+memQuiet)/3) || !near(r.MemMaxPct, memBusy) || r.TempMax != 65 ||
		!near(r.PowerAvg, (210.5*2+40)/3) {
		t.Fatalf("aggregate of 101: %+v", r)
	}
	if off := rows[1]; off.VMID != 103 || off.Off != 3 || off.Expected != 0 || off.Group != "altro" || off.TempMax != -1 || off.PowerAvg != -1 {
		t.Fatalf("aggregate of the stopped VM: %+v", off)
	}
	g.mu.Lock()
	for k := range g.acc {
		if k.day == "2026-11-19" {
			t.Errorf("finished day still in memory: %+v", k)
		}
	}
	g.mu.Unlock()
	if next, _ := store.GPUUseDays("CALCOLO", "2026-11-20", "2026-11-20"); len(next) != 2 {
		t.Fatalf("new day not written: %+v", next)
	}
}

func TestGPUTargetsDedupAndTemplates(t *testing.T) {
	inv := GPUInventory{Nodes: []GPUNode{{Node: "pve1", Cards: []GPUCard{
		{Name: "A2 è", Uses: []GPUUse{{VMID: 1}, {VMID: 1}}},
		{Name: "A2", Uses: []GPUUse{{VMID: 1}}},
		{Name: "A2", Uses: []GPUUse{{VMID: 2}}},
		{Name: "A2", Uses: []GPUUse{{VMID: 3}}},
	}}}}
	res, _ := json.Marshal([]map[string]interface{}{
		{"type": "qemu", "vmid": 1.0, "node": "pve1", "status": "running", "name": "a"},
		{"type": "qemu", "vmid": 2.0, "node": "pve1", "status": "stopped", "name": "ubuntu-template", "template": 1.0},
		{"type": "qemu", "vmid": 3.0, "node": "bad node", "status": "running", "name": "c"},
	})
	ts, err := gpuTargets(inv, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].vmid != 1 || ts[0].cards != 2 || ts[0].model != "A2" {
		t.Fatalf("targets: %+v", ts)
	}
}

func TestSampleStateReserved(t *testing.T) {
	zero, nine, ten, used, total := 0.0, 9.0, 10.0, 10.0, 100.0
	for _, tc := range []struct {
		name  string
		cards []gpuCardUse
		want  string
	}{
		{"busy utilization", []gpuCardUse{{Util: &ten}}, gpuStBusy},
		{"reserved", []gpuCardUse{{Util: &nine, MemUsedMiB: &used, MemTotalMiB: &total}}, gpuStReserved},
		{"busy takes priority", []gpuCardUse{{Util: &zero, MemUsedMiB: &used, MemTotalMiB: &total}, {Util: &ten}}, gpuStBusy},
		{"quiet", []gpuCardUse{{Util: &zero}}, gpuStQuiet},
		{"unknown utilization memory fallback", []gpuCardUse{{MemUsedMiB: &used, MemTotalMiB: &total}}, gpuStBusy},
		{"other card utilization known", []gpuCardUse{{MemUsedMiB: &used, MemTotalMiB: &total}, {Util: &zero}}, gpuStReserved},
		{"nothing known", []gpuCardUse{{}}, gpuStNoData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sampleState(tc.cards); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestGPUUseReservedAccumulator(t *testing.T) {
	g := newGPUUseCollector()
	target := gpuUseTarget{vmid: 101, node: "n1", group: "altro", cards: 1}
	zero, ten, total := 0.0, 10.0, 100.0
	cards := []gpuCardUse{{Util: &zero, MemUsedMiB: &ten, MemTotalMiB: &total}}
	g.record("C", "2026-11-19", target, sampleState(cards), cards)
	cards[0].Util = &ten
	g.record("C", "2026-11-19", target, sampleState(cards), cards)
	k := gpuUseAccKey{"C", "2026-11-19", 101, target.inst}
	row := g.acc[k].row(k)
	if row.Valid != 2 || row.Reserved != 1 || row.BusyMinutes != 5 {
		t.Fatalf("%+v", row)
	}
}

func TestGPUUseSELinuxProbeCachedAndBackoff(t *testing.T) {
	f := newFakeGPUAPI()
	f.probeOutput = "INLINE\nsystem_u:system_r:virt_qemu_ga_t:s0\x00\n"
	f.addVM(101, "running", "vm", "", &fakeGPUGuest{})
	g, clk, _ := newTestCollector(t, f, gpuTestInventory(101))
	for i := 0; i < 4; i++ {
		g.cycle(testClusters)
		clk.add(gpuUseEvery)
	}
	if f.count(f.probes, 101) != 1 || f.count(f.runs, 101) != 0 || liveState(g)[101].State != gpuStPaused {
		t.Fatalf("probes=%d runs=%d state=%s", f.count(f.probes, 101), f.count(f.runs, 101), liveState(g)[101].State)
	}
	clk.add(gpuUseProbeTTL)
	g.cycle(testClusters)
	if f.count(f.probes, 101) != 2 {
		t.Fatalf("probe not refreshed")
	}
}
