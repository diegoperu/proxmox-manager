package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// fakePVE is a small in-memory Proxmox for the write path: per-VM config with a digest that changes on
// every write, and Proxmox's own digest check on PUT.
type fakePVE struct {
	mu       sync.Mutex
	status   map[int]string
	cfg      map[int]map[string]interface{}
	ver      map[int]int
	writes   []url.Values
	failCfg  map[int]bool
	forbid   bool
	onWrite  func(vmid int) // runs before the digest check (simulates someone else editing the VM)
	pciJSON  string
	mapsJSON string
}

func newFakePVE() *fakePVE {
	return &fakePVE{
		status: map[int]string{100: "stopped", 101: "stopped", 102: "running", 103: "stopped"},
		cfg: map[int]map[string]interface{}{
			100: {"name": "target"},
			101: {"name": "other"},
			102: {"name": "busy-direct", "hostpci0": "0000:02:00,rombar=0"},
			103: {"name": "tpl", "template": 1},
		},
		ver:     map[int]int{},
		failCfg: map[int]bool{},
		pciJSON: `[{"id":"0000:01:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"A100"},
		 {"id":"0000:02:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"A100"},
		 {"id":"0000:03:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"A100"},
		 {"id":"0000:06:00.0","class":"0x020000","vendor":"0x8086","vendor_name":"Intel","device_name":"X710"}]`,
		mapsJSON: `[{"id":"g1","map":["node=n1,path=0000:01:00"]},{"id":"g2","map":["node=n1,path=0000:02:00"]},
		 {"id":"elsewhere","map":["node=n2,path=0000:01:00"]},{"id":"grp","map":["node=n1,path=0000:01:00","node=n1,path=0000:03:00"]},
		 {"id":"nic","map":["node=n1,path=0000:06:00"]}]`,
	}
}

func (f *fakePVE) digest(vmid int) string {
	return "d" + strconv.Itoa(vmid) + "v" + strconv.Itoa(f.ver[vmid])
}

func (f *fakePVE) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"n1","status":"online"}]`), nil
}
func (f *fakePVE) GetNodePCI(string) (json.RawMessage, error) { return json.RawMessage(f.pciJSON), nil }
func (f *fakePVE) GetPCIMappings() (json.RawMessage, error)   { return json.RawMessage(f.mapsJSON), nil }
func (f *fakePVE) GetVMs(node string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var list []map[string]interface{}
	for id, st := range f.status {
		list = append(list, map[string]interface{}{"vmid": id, "status": st, "name": f.cfg[id]["name"]})
	}
	b, _ := json.Marshal(list)
	return b, nil
}
func (f *fakePVE) GetVMConfig(node string, vmid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if node != "n1" || f.cfg[vmid] == nil {
		return nil, fmt.Errorf("proxmox 500 on /nodes/%s/qemu/%d/config: Configuration file does not exist", node, vmid)
	}
	if f.failCfg[vmid] {
		return nil, errors.New("proxmox 500: timeout")
	}
	c := map[string]interface{}{"digest": f.digest(vmid)}
	for k, v := range f.cfg[vmid] {
		c[k] = v
	}
	b, _ := json.Marshal(c)
	return b, nil
}
func (f *fakePVE) GetVMStatus(node string, vmid int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfg[vmid] == nil {
		return nil, errors.New("proxmox 500: VM does not exist")
	}
	return json.RawMessage(`{"status":"` + f.status[vmid] + `"}`), nil
}
func (f *fakePVE) VMSetConfig(node string, vmid int, p url.Values) (json.RawMessage, error) {
	if f.onWrite != nil {
		f.onWrite(vmid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forbid {
		return nil, errors.New("proxmox 403: Permission check failed")
	}
	if p.Get("digest") != f.digest(vmid) {
		return nil, errors.New("proxmox 500: detected modified configuration - file changed by other user? Try again")
	}
	f.writes = append(f.writes, p)
	for k, v := range p {
		switch k {
		case "digest":
		case "delete":
			delete(f.cfg[vmid], v[0])
		default:
			f.cfg[vmid][k] = v[0]
		}
	}
	f.ver[vmid]++
	return json.RawMessage(`null`), nil
}

func plan(t *testing.T, f *fakePVE, vmid int, mapping string) (GPUPlan, *gpuErr) {
	t.Helper()
	return planGPUAssign(f, "n1", vmid, mapping, func(string) string { return "pcie=1" }, time.Now())
}

func TestAssignGPUWritesMappingWithDigestAndOptions(t *testing.T) {
	f := newFakePVE()
	p, e := plan(t, f, 100, "g1")
	if e != nil || p.Slot != "hostpci0" || p.Options != "pcie=1" || p.Addr != "0000:01:00" {
		t.Fatalf("plan %+v %v", p, e)
	}
	if _, e := assignGPU(f, 0, "n1", 100, "g1", p.Slot, p.Options, func(string) string { return "pcie=1" }); e != nil {
		t.Fatal(e)
	}
	if len(f.writes) != 1 || f.writes[0].Get("hostpci0") != "mapping=g1,pcie=1" || f.writes[0].Get("digest") != "d100v0" {
		t.Fatalf("writes %v", f.writes)
	}
	// second time: the card is now this VM's
	if _, e := plan(t, f, 100, "g1"); e == nil || e.Reason != "already" {
		t.Fatalf("got %v", e)
	}
	if _, e := plan(t, f, 101, "g1"); e == nil || e.Reason != "busy" {
		t.Fatalf("got %v", e)
	}
}

func TestAssignGPURefusals(t *testing.T) {
	cases := []struct {
		vmid    int
		mapping string
		prep    func(*fakePVE)
		reason  string
	}{
		{102, "g1", nil, "running"},
		{103, "g1", nil, "template"},
		{100, "g1", func(f *fakePVE) { f.cfg[100]["lock"] = "backup" }, "locked"},
		{100, "elsewhere", nil, "other_node"},
		{100, "grp", nil, "group"},
		{100, "nic", nil, "not_gpu"},
		{100, "missing", nil, "no_mapping"},
		{100, "g2", func(f *fakePVE) { f.status[102] = "stopped" }, "busy"}, // used by a direct line on VM 102
		{100, "g1", func(f *fakePVE) { f.failCfg[101] = true }, "cannot_check"},
		{999, "g1", nil, "not_found"},
		{100, "g1", func(f *fakePVE) {
			for i := 0; i < 16; i++ {
				f.cfg[100]["hostpci"+strconv.Itoa(i)] = "0000:06:00"
			}
		}, "no_slot"},
	}
	for _, c := range cases {
		f := newFakePVE()
		if c.prep != nil {
			c.prep(f)
		}
		_, e := plan(t, f, c.vmid, c.mapping)
		if e == nil || e.Reason != c.reason {
			t.Fatalf("vm %d mapping %s: want %s, got %+v", c.vmid, c.mapping, c.reason, e)
		}
		if len(f.writes) != 0 {
			t.Fatalf("a refusal must not write: %v", f.writes)
		}
	}
}

func TestAssignGPUPlanChangedAndDigestRace(t *testing.T) {
	f := newFakePVE()
	if _, e := assignGPU(f, 0, "n1", 100, "g1", "hostpci3", "", nil); e == nil || e.Reason != "plan_changed" || len(f.writes) != 0 {
		t.Fatalf("got %v writes %v", e, f.writes)
	}
	// someone edits the VM in Proxmox between our read and our PUT: Proxmox rejects on the digest
	f = newFakePVE()
	f.onWrite = func(vmid int) { f.mu.Lock(); f.ver[vmid]++; f.mu.Unlock() }
	if _, e := assignGPU(f, 0, "n1", 100, "g1", "hostpci0", "", nil); e == nil || e.Reason != "changed" {
		t.Fatalf("got %v", e)
	}
	f = newFakePVE()
	f.forbid = true
	if _, e := assignGPU(f, 0, "n1", 100, "g1", "hostpci0", "", nil); e == nil || e.Reason != "forbidden" || e.Code != 403 {
		t.Fatalf("got %v", e)
	}
}

func TestAssignGPUConcurrentSameCardOnlyOneWins(t *testing.T) {
	for round := 0; round < 20; round++ {
		f := newFakePVE()
		var wg sync.WaitGroup
		res := make([]*gpuErr, 2)
		for i, vmid := range []int{100, 101} {
			wg.Add(1)
			go func(i, vmid int) {
				defer wg.Done()
				_, res[i] = assignGPU(f, 7, "n1", vmid, "g1", "hostpci0", "", nil)
			}(i, vmid)
		}
		wg.Wait()
		ok := 0
		for _, e := range res {
			if e == nil {
				ok++
			} else if e.Reason != "busy" {
				t.Fatalf("unexpected refusal %v", e)
			}
		}
		if ok != 1 || len(f.writes) != 1 {
			t.Fatalf("round %d: %d successes, writes %v", round, ok, f.writes)
		}
	}
}

func TestUnassignGPU(t *testing.T) {
	f := newFakePVE()
	f.status[102] = "stopped"
	if e := unassignGPU(f, 0, "n1", 102, "hostpci0"); e == nil || e.Reason != "direct" || len(f.writes) != 0 {
		t.Fatalf("direct must be refused: %v", e)
	}
	f.cfg[100]["hostpci2"] = "mapping=g1,pcie=1"
	if e := unassignGPU(f, 0, "n1", 100, "hostpci5"); e == nil || e.Reason != "no_slot" {
		t.Fatalf("got %v", e)
	}
	if e := unassignGPU(f, 0, "n1", 100, "hostpci2"); e != nil {
		t.Fatal(e)
	}
	if len(f.writes) != 1 || f.writes[0].Get("delete") != "hostpci2" || f.writes[0].Get("digest") != "d100v0" || f.cfg[100]["hostpci2"] != nil {
		t.Fatalf("writes %v cfg %v", f.writes, f.cfg[100])
	}
	f.status[100] = "running"
	if e := unassignGPU(f, 0, "n1", 100, "hostpci2"); e == nil || e.Reason != "running" {
		t.Fatalf("got %v", e)
	}
}

func TestHostpciOptionsAndPick(t *testing.T) {
	cases := map[string]string{
		"0000:01:00":                            "",
		"0000:01:00,rombar=0":                   "rombar=0",
		"mapping=g,x-vga=1,pcie=1":              "pcie=1,x-vga=1",
		"0000:01:00,romfile=evil.rom,pcie=1":    "pcie=1",
		"0000:01:00,pcie=yes,rombar=0,rombar=1": "rombar=0",
		"0000:01:00,mdev=nvidia-1,legacy-igd=1": "",
	}
	for in, want := range cases {
		if got := normalizeHostpciOptions(in); got != want {
			t.Fatalf("%q -> %q want %q", in, got, want)
		}
	}
	inv := GPUInventory{Nodes: []GPUNode{{Cards: []GPUCard{
		{Name: "A100", Uses: []GPUUse{{Options: ""}, {Options: "rombar=0"}, {Options: ""}}},
		{Name: "L4", Uses: []GPUUse{{Options: "pcie=1"}}},
	}}}}
	if pickGPUOptions(inv, "A100") != "" || pickGPUOptions(inv, "L4") != "pcie=1" || pickGPUOptions(inv, "T4") != "" {
		t.Fatal("pick")
	}
}

// Input validation happens before any Proxmox client is built: a bad request never reaches Proxmox.
func TestGPUHandlersRejectBadInput(t *testing.T) {
	r := chi.NewRouter()
	h := &Handler{}
	r.Get("/api/nodes/{node}/qemu/{vmid}/gpu/plan", h.PlanGPU)
	r.Post("/api/nodes/{node}/qemu/{vmid}/gpu", h.AssignGPU)
	r.Delete("/api/nodes/{node}/qemu/{vmid}/gpu/{slot}", h.UnassignGPU)
	cases := []struct{ method, path, body string }{
		{"GET", "/api/nodes/n1/qemu/100/gpu/plan?mapping=bad%20name", ""},
		{"GET", "/api/nodes/n1/qemu/0100/gpu/plan?mapping=g1", ""},
		{"GET", "/api/nodes/n%3B1/qemu/100/gpu/plan?mapping=g1", ""},
		{"POST", "/api/nodes/n1/qemu/100/gpu", `{"mapping":"g1","slot":"hostpci16","options":""}`},
		{"POST", "/api/nodes/n1/qemu/100/gpu", `{"mapping":"g1","slot":"hostpci0","options":"romfile=x"}`},
		{"POST", "/api/nodes/n1/qemu/100/gpu", `{"mapping":"g1","slot":"hostpci0","options":"rombar=0,pcie=1"}`}, // not canonical
		{"POST", "/api/nodes/n1/qemu/100/gpu", `{"mapping":"g1,x-vga=1","slot":"hostpci0","options":""}`},
		{"POST", "/api/nodes/n1/qemu/100/gpu", `not json`},
		{"DELETE", "/api/nodes/n1/qemu/100/gpu/net0", ""},
		{"DELETE", "/api/nodes/n1/qemu/100/gpu/hostpci0;rm", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s %s: got %d %s", c.method, c.path, c.body, rec.Code, rec.Body.String())
		}
	}
}

func TestUnassignOnlyGPUMappings(t *testing.T) {
	f := newFakePVE()
	f.mapsJSON = `[{"id":"g1","map":["node=n1,path=0000:01:00"]},{"id":"nic","map":["node=n1,path=0000:06:00"]},
	 {"id":"gone-nv","map":["node=n2,path=0000:01:00,id=10de:20b0"]},{"id":"gone-other","map":["node=n2,path=0000:01:00,id=8086:1572"]},
	 {"id":"gone-noid","map":["node=n2,path=0000:01:00"]}]`
	f.cfg[100]["hostpci0"] = "mapping=nic"
	f.cfg[100]["hostpci1"] = "mapping=gone-other"
	f.cfg[100]["hostpci2"] = "mapping=gone-noid"
	f.cfg[100]["hostpci3"] = "mapping=gone-nv"
	f.cfg[100]["hostpci4"] = "mapping=g1"
	f.cfg[100]["hostpci5"] = "mapping=unknown"
	for _, slot := range []string{"hostpci0", "hostpci1", "hostpci2", "hostpci5"} {
		if e := unassignGPU(f, 0, "n1", 100, slot); e == nil || e.Reason != "not_gpu" {
			t.Fatalf("%s: want not_gpu, got %v", slot, e)
		}
	}
	if len(f.writes) != 0 {
		t.Fatalf("no write expected: %v", f.writes)
	}
	for _, slot := range []string{"hostpci3", "hostpci4"} { // NVIDIA entry elsewhere (to fix), NVIDIA card here
		if e := unassignGPU(f, 0, "n1", 100, slot); e != nil {
			t.Fatalf("%s: %v", slot, e)
		}
	}
	if len(f.writes) != 2 {
		t.Fatalf("writes %v", f.writes)
	}
}

func TestGPULockIsStableAndBounded(t *testing.T) {
	if gpuLock(1, "n1") != gpuLock(1, "n1") {
		t.Fatal("same key must give the same lock")
	}
	seen := map[*sync.Mutex]bool{}
	for i := 0; i < 5000; i++ {
		seen[gpuLock(i, "node-"+strconv.Itoa(i))] = true
	}
	if len(seen) > len(gpuLocks) {
		t.Fatalf("%d locks", len(seen))
	}
}
