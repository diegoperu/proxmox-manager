package handlers

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"proxmox-manager/internal/balance"
)

type fakeBalance struct {
	mu        sync.Mutex
	calls     []string
	resources string
	haErr     error
	noCeph    bool
	failCfg   map[int]bool
	prechecks map[int]string
	rrdCPU    float64           // constant cpu fraction in the month RRD
	models    map[string]string // node -> CPU flags ("" = unknown; default: the same flags everywhere)
	hostCPU   map[int]bool      // QEMU VMs with cpu: host
}

func (f *fakeBalance) GetNodeStatus(n string) (json.RawMessage, error) {
	f.rec("nodestatus@" + n)
	fl := "fpu vme sse sse2 avx avx2"
	if v, ok := f.models[n]; ok {
		fl = v
	}
	b, _ := json.Marshal(map[string]interface{}{"cpuinfo": map[string]string{"model": "x", "flags": fl}})
	return b, nil
}

func (f *fakeBalance) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}
func (f *fakeBalance) GetClusterResources(string) (json.RawMessage, error) {
	f.rec("resources")
	return json.RawMessage(f.resources), nil
}
func (f *fakeBalance) rrd() json.RawMessage {
	var pts []string
	for i := 0; i < 100; i++ {
		pts = append(pts, `{"time":`+itoaT(1700000000+i*1800)+`,"cpu":`+ftoaT(f.rrdCPU)+`}`)
	}
	return json.RawMessage("[" + strings.Join(pts, ",") + "]")
}
func itoaT(n int) string { b, _ := json.Marshal(n); return string(b) }
func ftoaT(x float64) string {
	b, _ := json.Marshal(x)
	return string(b)
}
func (f *fakeBalance) GetNodeRRDcf(n, tf, cf string) (json.RawMessage, error) {
	f.rec("noderrd@" + n)
	return f.rrd(), nil
}
func (f *fakeBalance) GetGuestRRDcf(n, k string, id int, tf, cf string) (json.RawMessage, error) {
	f.rec("guestrrd")
	return f.rrd(), nil
}
func (f *fakeBalance) GetVMConfig(n string, id int) (json.RawMessage, error) {
	f.rec("cfg")
	if f.failCfg[id] {
		return nil, errors.New("boom")
	}
	if id == 2 {
		return json.RawMessage(`{"hostpci0":"0000:01:00","name":"gpu"}`), nil
	}
	if f.hostCPU[id] {
		return json.RawMessage(`{"name":"x","cpu":"host,flags=+aes"}`), nil
	}
	return json.RawMessage(`{"name":"x"}`), nil
}
func (f *fakeBalance) GetContainerConfig(n string, id int) (json.RawMessage, error) {
	f.rec("cfg")
	return json.RawMessage(`{"rootfs":"local-lvm:vm-9-disk-0,size=8G","mp0":"ceph:vm-9-disk-1,mp=/d"}`), nil
}
func (f *fakeBalance) GetMigratePrecheck(n string, id int) (json.RawMessage, error) {
	f.rec("precheck")
	if s, ok := f.prechecks[id]; ok {
		return json.RawMessage(s), nil
	}
	return json.RawMessage(`{"allowed_nodes":["n1","n2","n3","bad name"],"local_disks":[],"local_resources":[]}`), nil
}
func (f *fakeBalance) GetHAResources() (json.RawMessage, error) {
	if f.haErr != nil {
		return nil, f.haErr
	}
	return json.RawMessage(`[{"sid":"vm:4"}]`), nil
}
func (f *fakeBalance) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"n1","status":"online"}]`), nil
}
func (f *fakeBalance) GetCephOSD(n string) (json.RawMessage, error) {
	if f.noCeph {
		return nil, errors.New(`proxmox 500: binary not installed`)
	}
	return json.RawMessage(`{"root":{"type":"root","children":[{"type":"osd","id":"0","name":"osd.0","host":"n1"},{"type":"osd","id":"1","name":"osd.1","host":"n1"}]}}`), nil
}

const gibB = int64(1) << 30

func resJSON() string {
	g := func(n int64) string { return itoaT(int(n * gibB)) }
	return `[
 {"type":"node","node":"n1","status":"online","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"node","node":"n2","status":"online","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"node","node":"n3","status":"offline","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"storage","node":"n1","storage":"ceph","shared":1,"status":"available"},
 {"type":"storage","node":"n1","storage":"local-lvm","shared":0,"status":"available"},
 {"type":"qemu","node":"n1","vmid":1,"name":"a","status":"running","maxmem":` + g(30) + `,"maxcpu":4,"tags":"gruppo-rossi"},
 {"type":"qemu","node":"n1","vmid":2,"name":"gpu","status":"running","maxmem":` + g(30) + `,"maxcpu":4},
 {"type":"qemu","node":"n1","vmid":3,"name":"c","status":"running","maxmem":` + g(20) + `,"maxcpu":2,"lock":"backup"},
 {"type":"qemu","node":"n1","vmid":4,"name":"ha","status":"running","maxmem":` + g(10) + `,"maxcpu":2},
 {"type":"lxc","node":"n1","vmid":5,"name":"ct","status":"running","maxmem":` + g(8) + `,"maxcpu":1},
 {"type":"qemu","node":"n1","vmid":6,"name":"tpl","status":"stopped","template":1,"maxmem":` + g(8) + `,"maxcpu":1}
]`
}

func query(kv ...string) func(string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v.Get
}

func TestParseBalanceQuery(t *testing.T) {
	q, err := parseBalanceQuery(query())
	if err != nil || !reflect.DeepEqual(q.P, balance.DefaultParams()) || q.Fresh {
		t.Fatalf("defaults %#v %v", q, err)
	}
	q, err = parseBalanceQuery(query("host_reserve_gb", "16", "per_osd_gb", "2.5", "count_stopped", "0", "cold", "1", "max_steps", "5",
		"vcpu_per_core", "6", "cpu_warn", "70", "evacuate", "n1,n2,n1", "reserve", "n1:50,n2:0", "fresh", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if q.P.HostReserve != 16*gibB || q.P.PerOSD != gibB*5/2 || q.P.CountStopped || !q.P.Cold || q.P.MaxSteps != 5 || q.P.VCPUPerCore != 6 ||
		q.P.CPUWarnPct != 70 || !reflect.DeepEqual(q.P.Evacuate, []string{"n1", "n2"}) || q.Reserves["n1"] != 50*gibB || q.Reserves["n2"] != 0 || !q.Fresh {
		t.Fatalf("%#v", q)
	}
	for _, bad := range [][]string{
		{"host_reserve_gb", "-1"}, {"host_reserve_gb", "513"}, {"host_reserve_gb", "x"}, {"per_osd_gb", "65"},
		{"max_steps", "0"}, {"max_steps", "51"}, {"max_steps", "x"}, {"vcpu_per_core", "0"}, {"cpu_warn", "0"}, {"cpu_warn", "101"},
		{"count_stopped", "maybe"}, {"cold", "2"}, {"fresh", "yes"}, {"evacuate", "bad name"}, {"evacuate", "a;b"}, {"evacuate", "$(x)"},
		{"reserve", "n1"}, {"reserve", "n1:x"}, {"reserve", ":5"}, {"reserve", "bad name:5"}, {"reserve", "n1:5000"},
		{"evacuate", manyNodes(40)},
	} {
		if _, err := parseBalanceQuery(query(bad...)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestBuildBalanceBlockersAndMeta(t *testing.T) {
	f := &fakeBalance{resources: resJSON(), rrdCPU: 0.5}
	q, _ := parseBalanceQuery(query("cold", "1"))
	res, err := buildBalance(f, "C", 0, q, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[int][]string{}
	for _, b := range res.Blocked {
		reasons[b.VMID] = b.Reasons
	}
	if !reflect.DeepEqual(reasons[2], []string{"gpu"}) || !reflect.DeepEqual(reasons[3], []string{"lock"}) || !reflect.DeepEqual(reasons[4], []string{"ha"}) {
		t.Fatalf("%v", reasons)
	}
	if !reflect.DeepEqual(reasons[5], []string{"local_disk"}) { // rootfs on a storage that is not shared
		t.Fatalf("lxc %v", reasons[5])
	}
	if _, ok := reasons[1]; ok || reasons[6] != nil {
		t.Fatalf("free VM blocked or template listed: %v", reasons)
	}
	if !res.Meta.CephKnown || !res.Meta.HAKnown || !res.Meta.CPUKnown || res.Meta.Unverified != 0 || len(res.Meta.Partial) != 0 {
		t.Fatalf("%#v", res.Meta)
	}
	// n1 has 2 OSDs: reserve 8 + 2*4 = 16 GB; CPU p95 of the node is the constant 50%
	n1 := res.Nodes[0]
	if n1.Name != "n1" || n1.Reserve != 16*gibB || n1.OSDs != 2 || n1.CPUBefore != 50 {
		t.Fatalf("%#v", n1)
	}
	// the VM with the allowed list may go to n2 only (n3 is offline, "bad name" is dropped)
	for _, s := range res.Steps {
		if s.To != "n2" {
			t.Fatalf("%#v", s)
		}
	}
	// RRD of a template is never read
	for _, c := range f.calls {
		if strings.Contains(c, "tpl") {
			t.Fatal("template touched")
		}
	}
}

func TestBuildBalanceNeverGuesses(t *testing.T) {
	factsReset()
	f := &fakeBalance{resources: resJSON(), haErr: errors.New("403"), noCeph: true, failCfg: map[int]bool{1: true},
		prechecks: map[int]string{4: `not json`}}
	q, _ := parseBalanceQuery(query())
	res, err := buildBalance(f, "C", 1, q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta.CephKnown || res.Meta.HAKnown || res.Meta.Unverified != 2 || !reflect.DeepEqual(res.Meta.Partial, []string{"ceph", "ha", "precheck"}) {
		t.Fatalf("%#v", res.Meta)
	}
	for _, b := range res.Blocked {
		if (b.VMID == 1 || b.VMID == 4) && !containsStr(b.Reasons, "unverified") {
			t.Fatalf("%#v", b)
		}
	}
	for _, s := range res.Steps {
		if s.VMID == 1 || s.VMID == 4 {
			t.Fatalf("unverified guest moved: %#v", s)
		}
	}
	if res.Nodes[0].OSDs != 0 {
		t.Fatalf("OSDs invented: %#v", res.Nodes[0])
	}
}

func TestBuildBalanceReadOnlyAndFactsCache(t *testing.T) {
	factsReset()
	f := &fakeBalance{resources: resJSON(), rrdCPU: 0.1}
	q, _ := parseBalanceQuery(query())
	if _, err := buildBalance(f, "C", 2, q, time.Now()); err != nil {
		t.Fatal(err)
	}
	n := len(f.calls)
	if _, err := buildBalance(f, "C", 2, q, time.Now()); err != nil {
		t.Fatal(err)
	}
	again := f.calls[n:]
	for _, c := range again {
		if c == "cfg" || c == "precheck" || c == "guestrrd" {
			t.Fatalf("static facts read again within the TTL: %v", again)
		}
	}
	q.Fresh = true
	m := len(f.calls)
	if _, err := buildBalance(f, "C", 2, q, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !containsStr(f.calls[m:], "cfg") {
		t.Fatal("fresh=1 must re-read")
	}
	// the fake has no write method at all: the interface proves nothing can be written
}

func TestBuildBalanceUnknownEvacuateNode(t *testing.T) {
	f := &fakeBalance{resources: resJSON()}
	q, _ := parseBalanceQuery(query("evacuate", "nope"))
	if _, err := buildBalance(f, "C", 3, q, time.Now()); err == nil || !strings.Contains(err.Error(), "sconosciuto") {
		t.Fatalf("%v", err)
	}
}

func TestConfigBlockers(t *testing.T) {
	shared := map[string]bool{"ceph": true}
	cases := []struct {
		cfg  string
		kind string
		want []string
	}{
		{`{"name":"x"}`, "qemu", []string{}},
		{`{"hostpci0":"0000:01:00","usb0":"host=1-1"}`, "qemu", []string{"gpu", "local_resource"}},
		{`{"rootfs":"ceph:vm-1-disk-0","mp0":"/srv/data,mp=/d"}`, "lxc", []string{"local_resource"}},
		{`{"rootfs":"local-lvm:vm-1-disk-0"}`, "lxc", []string{"local_disk"}},
		{`{"rootfs":"ceph:vm-1-disk-0"}`, "lxc", []string{}},
	}
	for _, c := range cases {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(c.cfg), &m)
		if got := configBlockers(m, c.kind, shared); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v want %v", c.cfg, got, c.want)
		}
	}
}

func factsReset() {
	balanceFacts.Lock()
	balanceFacts.m = map[string]balanceFactEntry{}
	balanceFacts.Unlock()
}

func manyNodes(n int) string {
	var l []string
	for i := 0; i < n; i++ {
		l = append(l, "node"+itoaT(i))
	}
	return strings.Join(l, ",")
}
