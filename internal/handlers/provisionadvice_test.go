package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// two online nodes of 100 GB (n1 loaded, n2 empty), one offline, storages: ceph shared on n1+n2, local only on n1
func adviceRes() string {
	g := func(n int64) string { return itoaT(int(n * gibB)) }
	return `[
 {"type":"node","node":"n1","status":"online","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"node","node":"n2","status":"online","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"node","node":"n3","status":"offline","maxmem":` + g(100) + `,"maxcpu":16},
 {"type":"storage","node":"n1","storage":"ceph","shared":1,"status":"available","content":"images,rootdir"},
 {"type":"storage","node":"n2","storage":"ceph","shared":1,"status":"available","content":"images,rootdir"},
 {"type":"storage","node":"n1","storage":"local","shared":0,"status":"available","content":"images"},
 {"type":"storage","node":"n2","storage":"local","shared":0,"status":"available","content":"images"},
 {"type":"storage","node":"n1","storage":"isos","shared":1,"status":"available","content":"iso"},
 {"type":"storage","node":"n2","storage":"isos","shared":1,"status":"unknown","content":"iso"},
 {"type":"qemu","node":"n1","vmid":1,"name":"a","status":"running","maxmem":` + g(60) + `,"maxcpu":4,"tags":"gruppo-rossi"},
 {"type":"qemu","node":"n2","vmid":2,"name":"b","status":"running","maxmem":` + g(20) + `,"maxcpu":4},
 {"type":"qemu","node":"n2","vmid":6,"name":"tpl","status":"stopped","template":1,"maxmem":` + g(50) + `,"maxcpu":1}
]`
}

func adviceFake() *fakeBalance { return &fakeBalance{resources: adviceRes(), rrdCPU: 0.1} }

func aq(t *testing.T, kv ...string) adviceQuery {
	t.Helper()
	q, err := parseAdviceQuery(query(kv...))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func nodeBy(r adviceResult, name string) (n struct {
	Eligible bool
	Reasons  []string
	Pct      float64
}) {
	for _, x := range r.Nodes {
		if x.Name == name {
			n.Eligible, n.Pct = x.Eligible, x.PctAfter
			for _, re := range x.Reasons {
				n.Reasons = append(n.Reasons, re.Code)
			}
		}
	}
	return
}

func TestAdviceRanksNodesAndTemplatesDoNotCount(t *testing.T) {
	r, err := buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "8192", "cores", "2"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// n2 has 20 GB used (the 50 GB template does not count), n1 60 GB: n2 first
	if r.Recommended != "n2" || r.Nodes[0].Name != "n2" || !r.Nodes[0].Recommended {
		t.Fatalf("%+v", r.Nodes)
	}
	// n2: reserve 8 GB -> 92 GB assignable; 20 GB in use + 8 GB for the new VM = 30.43%
	if got := nodeBy(r, "n2").Pct; got != 30.43 {
		t.Fatalf("n2 pct %v", got)
	}
	if n3 := nodeBy(r, "n3"); n3.Eligible || len(n3.Reasons) != 1 || n3.Reasons[0] != "node_offline" {
		t.Fatalf("%+v", n3)
	}
	if len(r.Cards) != 0 {
		t.Fatalf("cards without gpu=1: %+v", r.Cards)
	}
}

func TestAdviceRAMShortfallIsReportedPerNode(t *testing.T) {
	// 90 GB asked: n1 (60 used) and n2 (20 used) both over; n2 is the closest
	r, err := buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", itoaT(90*1024), "cores", "2"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommended != "" || r.Nodes[0].Name != "n2" || r.Nodes[0].Eligible || r.Nodes[0].Reasons[0].Code != "ram" || r.Nodes[0].Reasons[0].Missing <= 0 {
		t.Fatalf("%+v", r.Nodes)
	}
}

func TestAdviceStorageRules(t *testing.T) {
	// shared storage: both online nodes fine; offline n3 only for being offline plus no storage entry
	r, _ := buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "storage", "ceph"), nil, time.Now())
	if !nodeBy(r, "n1").Eligible || !nodeBy(r, "n2").Eligible {
		t.Fatalf("%+v", r.Nodes)
	}
	if rs := nodeBy(r, "n3").Reasons; len(rs) != 2 || rs[1] != adviceStorageUnavailable {
		t.Fatalf("n3 %v", rs)
	}
	// a local storage: only the template's node can clone from it
	r, _ = buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "storage", "local", "template_node", "n1"), nil, time.Now())
	if !nodeBy(r, "n1").Eligible || nodeBy(r, "n2").Eligible || nodeBy(r, "n2").Reasons[0] != adviceStorageNotShared {
		t.Fatalf("%+v", r.Nodes)
	}
	// a storage that cannot hold disks / not active on a node
	r, _ = buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "storage", "isos"), nil, time.Now())
	if rs := nodeBy(r, "n1").Reasons; len(rs) != 1 || rs[0] != adviceStorageNoImages {
		t.Fatalf("n1 %v", rs)
	}
	if rs := nodeBy(r, "n2").Reasons; rs[0] != adviceStorageUnavailable {
		t.Fatalf("n2 %v", rs)
	}
	// unknown storage: unavailable everywhere
	r, _ = buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "storage", "nope"), nil, time.Now())
	if r.Recommended != "" || nodeBy(r, "n1").Reasons[0] != adviceStorageUnavailable {
		t.Fatalf("%+v", r.Nodes)
	}
}

func card(addr, name string, mem int, status string, maps []string, uses ...GPUUse) GPUCard {
	return GPUCard{Addr: addr, Name: name, MemoryGB: mem, Status: status, Mappings: maps, Uses: uses}
}

func TestAdviceGPUCards(t *testing.T) {
	inv := &GPUInventory{Nodes: []GPUNode{
		{Node: "n1", Online: true, Cards: []GPUCard{
			card("0000:01:00", "A100 40GB", 40, "assigned", []string{"M1"}, GPUUse{VMID: 9, Name: "busy-vm", Status: "running"}),
			card("0000:02:00", "A100 40GB", 40, "free", nil), // free but no mapping
		}},
		{Node: "n2", Online: true, Cards: []GPUCard{
			card("0000:03:00", "T4", 16, "free", []string{"M2"}),
			card("0000:04:00", "Mystery", 0, "free", []string{"M3"}),
		}},
		{Node: "n3", Online: false, Cards: []GPUCard{card("0000:05:00", "X", 24, "free", []string{"M4"})}},
	}}
	r, err := buildAdvice(adviceFake(), inv, "C", aq(t, "memory_mb", "4096", "cores", "2", "gpu", "1"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// only n2 has a free card with a mapping: n1 is not eligible (no_free_gpu), n3 offline
	if r.Recommended != "n2" || nodeBy(r, "n1").Eligible || nodeBy(r, "n1").Reasons[0] != adviceNoFreeGPU {
		t.Fatalf("%+v", r.Nodes)
	}
	if len(r.Cards) != 4 { // the offline node's card is not listed
		t.Fatalf("%+v", r.Cards)
	}
	// sorted by memory, biggest first, unknown last
	if r.Cards[0].MemoryGB != 40 || r.Cards[3].MemoryGB != 0 {
		t.Fatalf("%+v", r.Cards)
	}
	for _, c := range r.Cards {
		switch c.Addr {
		case "0000:01:00":
			if c.Selectable || c.Reasons[0] != adviceCardBusy || len(c.Uses) != 1 || c.Uses[0].Name != "busy-vm" {
				t.Errorf("busy card %+v", c)
			}
		case "0000:02:00":
			if c.Selectable || c.Reasons[0] != adviceCardNoMapping {
				t.Errorf("no mapping card %+v", c)
			}
		case "0000:03:00", "0000:04:00":
			if !c.Selectable {
				t.Errorf("free card %+v", c)
			}
		}
	}
	rec := 0
	for _, c := range r.Cards {
		if c.Recommended {
			rec++
			if c.Node != "n2" || !c.Selectable {
				t.Errorf("recommended %+v", c)
			}
		}
	}
	if rec != 1 {
		t.Fatalf("%d recommended cards", rec)
	}
}

func TestAdviceGPUCardOnANodeThatCannotTakeTheVM(t *testing.T) {
	inv := &GPUInventory{Nodes: []GPUNode{
		{Node: "n1", Online: true, Cards: []GPUCard{card("0000:01:00", "L4", 24, "free", []string{"M1"})}},
		{Node: "n2", Online: true, Cards: []GPUCard{card("0000:03:00", "L4", 24, "free", []string{"M2"})}},
	}}
	// n1 has 60 GB used: a 50 GB VM does not fit there, it does on n2
	r, err := buildAdvice(adviceFake(), inv, "C", aq(t, "memory_mb", itoaT(50*1024), "cores", "2", "gpu", "1"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommended != "n2" {
		t.Fatalf("%+v", r.Nodes)
	}
	for _, c := range r.Cards {
		if c.Node == "n1" && (c.Selectable || c.Reasons[len(c.Reasons)-1] != adviceCardNodeNotFit) {
			t.Errorf("%+v", c)
		}
	}
	// nothing eligible anywhere: no recommended node, no recommended card
	r, _ = buildAdvice(adviceFake(), inv, "C", aq(t, "memory_mb", itoaT(95*1024), "cores", "2", "gpu", "1"), nil, time.Now())
	for _, c := range r.Cards {
		if c.Recommended || c.Selectable {
			t.Errorf("%+v", c)
		}
	}
	// gpu wanted but the inventory could not be read: an error, never a guess
	if _, err := buildAdvice(adviceFake(), nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "gpu", "1"), nil, time.Now()); err == nil {
		t.Fatal("no inventory accepted")
	}
}

func TestAdviceGroupPreferenceAndReadOnly(t *testing.T) {
	f := adviceFake()
	before := len(f.calls)
	r, err := buildAdvice(f, nil, "C", aq(t, "memory_mb", "1024", "cores", "1", "group", "rossi"), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// the "rossi" VM is on n1: the group preference keeps n2 first, and it is reported
	for _, n := range r.Nodes {
		if n.Name == "n1" && n.SameGroup != 1 {
			t.Fatalf("%+v", n)
		}
	}
	for _, c := range f.calls[before:] {
		if strings.HasPrefix(c, "mig") || strings.Contains(c, "set") || strings.Contains(c, "post") {
			t.Fatalf("not read-only: %s", c)
		}
	}
	// JSON has no nulls in the lists the page iterates
	b, _ := json.Marshal(r)
	for _, k := range []string{"nodes", "cards", "reasons", "partial", "warnings"} {
		if strings.Contains(string(b), `"`+k+`":null`) {
			t.Fatalf("%s is null: %s", k, b)
		}
	}
}

func TestParseAdviceQuery(t *testing.T) {
	good := []string{"memory_mb", "4096", "cores", "4"}
	if _, err := parseAdviceQuery(query(append(good, "gpu", "1", "storage", "datacloud-ssd", "template_node", "datacloud-node-1", "group", "rossi")...)); err != nil {
		t.Fatal(err)
	}
	for name, kv := range map[string][]string{
		"no mem":       {"cores", "4"},
		"small mem":    {"memory_mb", "64", "cores", "4"},
		"huge mem":     {"memory_mb", "99999999999", "cores", "4"},
		"mem text":     {"memory_mb", "4G", "cores", "4"},
		"no cores":     {"memory_mb", "4096"},
		"zero cores":   {"memory_mb", "4096", "cores", "0"},
		"many cores":   {"memory_mb", "4096", "cores", "5000"},
		"bad gpu":      append(good, "gpu", "maybe"),
		"bad storage":  append(good, "storage", "a;b"),
		"storage path": append(good, "storage", "../x"),
		"bad node":     append(good, "template_node", "bad name"),
		"bad group":    append(good, "group", "Rossi!"),
		"long group":   append(good, "group", strings.Repeat("a", 33)),
		"long storage": append(good, "storage", strings.Repeat("a", 65)),
	} {
		if _, err := parseAdviceQuery(query(kv...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAdviceExtraDiskSpaceAndStorages(t *testing.T) {
	f := newFakeProv()
	// 'fast' on n2 has 5 GB free: a 10 GB disk misses 5 GB there, and n2 is not eligible; n1 has no 'fast'
	q := adviceQuery{MemMB: 1024, Cores: 1, Storages: []string{"ceph"}, TemplateNode: "n1", Disk2GB: 10, Disk2Storage: "fast"}
	r, err := buildAdvice(f, nil, "C", q, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]string{}
	missing := int64(0)
	for _, n := range r.Nodes {
		for _, re := range n.Reasons {
			by[n.Name] = append(by[n.Name], re.Code)
			if re.Code == adviceDisk2NoSpace {
				missing = re.Missing
			}
		}
	}
	if !containsStr(by["n2"], adviceDisk2NoSpace) || missing != 5*(int64(1)<<30) || !containsStr(by["n1"], adviceDisk2Unavailable) || r.Recommended != "" {
		t.Fatalf("%v missing=%d rec=%q", by, missing, r.Recommended)
	}
	// the default storage of the extra disk is the template's: ceph has room everywhere
	q.Disk2Storage = ""
	r, _ = buildAdvice(f, nil, "C", q, nil, time.Now())
	if r.Recommended == "" {
		t.Fatalf("%+v", r.Nodes)
	}
	// the storages offered per node: active and accepting VM disks (not the ISO-only one)
	ids := func(node string) string {
		out := []string{}
		for _, s := range r.Storages[node] {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	if ids("n1") != "ceph" || ids("n2") != "ceph,fast" {
		t.Fatalf("n1=%s n2=%s", ids("n1"), ids("n2"))
	}
	for _, s := range r.Storages["n2"] {
		if s.ID == "fast" && (s.Free != 5*(int64(1)<<30) || s.Total != 20*(int64(1)<<30)) {
			t.Fatalf("%+v", s)
		}
	}
	// without a disk nothing about disk2 is asked
	r, _ = buildAdvice(f, nil, "C", adviceQuery{MemMB: 1024, Cores: 1}, nil, time.Now())
	for _, n := range r.Nodes {
		for _, re := range n.Reasons {
			if strings.HasPrefix(re.Code, "disk2") {
				t.Fatalf("disk2 reason without a disk: %+v", n)
			}
		}
	}
}

func TestParseAdviceExtraDiskParams(t *testing.T) {
	good := []string{"memory_mb", "4096", "cores", "4"}
	q, err := parseAdviceQuery(query(append(good, "disk2_gb", "100", "disk2_storage", "datacloud-ssd")...))
	if err != nil || q.Disk2GB != 100 || q.Disk2Storage != "datacloud-ssd" {
		t.Fatalf("%v %+v", err, q)
	}
	if a, b := q.key(), (adviceQuery{MemMB: q.MemMB, Cores: q.Cores}).key(); a == b {
		t.Fatal("the cache key ignores the extra disk")
	}
	for name, kv := range map[string][]string{
		"zero":         append(good, "disk2_gb", "0"),
		"too big":      append(good, "disk2_gb", "16385"),
		"text":         append(good, "disk2_gb", "100G"),
		"bad storage":  append(good, "disk2_storage", "a;b"),
		"long storage": append(good, "disk2_storage", strings.Repeat("a", 65)),
	} {
		if _, err := parseAdviceQuery(query(kv...)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// ── a new VM is placed on the RAM that guests use now: stopped ones do not count ──

func pending(t *testing.T) {
	t.Helper()
	provPending.Lock()
	provPending.m = map[string]pendingVM{}
	provPending.Unlock()
	startedRecently.Lock()
	startedRecently.m = map[string]time.Time{}
	startedRecently.Unlock()
}

func adviceFor(t *testing.T, f *fakeProv, memGB int, now time.Time) adviceResult {
	t.Helper()
	r, err := buildAdvice(f, nil, "C", adviceQuery{Cluster: 41, MemMB: int64(memGB) * 1024, Cores: 2}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func nodeEligible(r adviceResult, name string) (bool, float64) {
	for _, n := range r.Nodes {
		if n.Name == name {
			return n.Eligible, n.PctBefore
		}
	}
	return false, -1
}

func TestAdviceIgnoresStoppedVMs(t *testing.T) {
	pending(t)
	f := newFakeProv()
	// n2: 92 GB assignable, VM 50 (20 GB) running. A stopped 200 GB VM must not take room: a 60 GB VM fits.
	stoppedVM(f, 70, "n2", 200)
	stoppedVM(f, 71, "n2", 40)
	r := adviceFor(t, f, 60, time.Now())
	ok, before := nodeEligible(r, "n2")
	if !ok {
		t.Fatalf("stopped VMs taken into account: %+v", r.Nodes)
	}
	// 20 GB of 92 assignable = 21.74 %: the stopped guests are not in the load
	if before != 21.74 {
		t.Fatalf("load before = %v, want 21.74", before)
	}
	// and a running one does count: 20 + 60 + 40 (running) > 92
	f.vms[71].status = "running"
	if ok, _ := nodeEligible(adviceFor(t, f, 60, time.Now()), "n2"); ok {
		t.Fatal("a running VM is not counted")
	}
}

func TestAdviceCountsCreationsInFlightAndGuestsStartedAMomentAgo(t *testing.T) {
	pending(t)
	f := newFakeProv()
	// a VM being created on n2 (70 GB, cloned but not started): 20 + 70 + 40 > 92 -> n2 is full for a 40 GB VM
	addPending("job-1", pendingVM{Cluster: 41, Node: "n2", VMID: 300, Mem: 70 << 30, VCPU: 4})
	if ok, _ := nodeEligible(adviceFor(t, f, 40, time.Now()), "n2"); ok {
		t.Fatal("a creation in flight is not counted")
	}
	// another cluster's creation does not count here
	if ok, _ := nodeEligible(func() adviceResult {
		r, _ := buildAdvice(f, nil, "C", adviceQuery{Cluster: 42, MemMB: 40 * 1024, Cores: 2}, nil, time.Now())
		return r
	}(), "n2"); !ok {
		t.Fatal("a creation of another cluster counted")
	}
	releasePending("job-1")
	if ok, _ := nodeEligible(adviceFor(t, f, 40, time.Now()), "n2"); !ok {
		t.Fatal("the promise was not released")
	}
	// a guest started a moment ago that Proxmox still shows stopped counts; after the TTL it does not
	stoppedVM(f, 70, "n2", 70)
	now := time.Now()
	markStarted(41, "n2", "qemu", 70, now)
	if ok, _ := nodeEligible(adviceFor(t, f, 40, now.Add(5*time.Second)), "n2"); ok {
		t.Fatal("a guest started a moment ago is not counted")
	}
	if ok, _ := nodeEligible(adviceFor(t, f, 40, now.Add(startReserveTTL+time.Second)), "n2"); !ok {
		t.Fatal("the reservation did not expire")
	}
	// a creation whose VM is already running is counted once, not twice
	f.vms[70].status = "running"
	addPending("job-2", pendingVM{Cluster: 41, Node: "n2", VMID: 70, Mem: 70 << 30, VCPU: 2})
	if ok, _ := nodeEligible(adviceFor(t, f, 2, time.Now()), "n2"); !ok {
		t.Fatal("a running VM with a promise still pending was counted twice")
	}
	releasePending("job-2")
}

func TestProvisionPromisesRAMUntilTheVMStartsAndKeepsNoneAfterAFailure(t *testing.T) {
	provFast(t)
	pending(t)
	// the job ends well: the promise is gone, the VM counts as running
	f := newFakeProv()
	j, err := startProv(f, 41, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pendingCreations(41)); got != 1 {
		t.Fatalf("no promise right after the start of the creation: %d", got)
	}
	waitProv(t, j)
	if got := len(pendingCreations(41)); got != 0 {
		t.Fatalf("promise left after the job: %d", got)
	}
	// a failed clone leaves no promise
	f = newFakeProv()
	f.failClone = true
	j, _ = startProv(f, 41, "C", mustPlanProv(t, baseReq()), noOpts, time.Now())
	waitProv(t, j)
	if got := len(pendingCreations(41)); got != 0 {
		t.Fatalf("promise left after a failed clone: %d", got)
	}
}
