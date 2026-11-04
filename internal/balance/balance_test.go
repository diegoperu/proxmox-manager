package balance

import (
	"math/rand"
	"reflect"
	"testing"
)

func node(name string, memGiB int64, cores, osds int) Node {
	return Node{Name: name, Online: true, MemTotal: memGiB * GiB, Cores: cores, OSDs: osds, CPUP95: -1, Reserve: -1}
}

func vm(at string, id int, memGiB int64, vcpu int) VM {
	return VM{Node: at, Kind: "qemu", VMID: id, Name: "vm", Mem: memGiB * GiB, VCPU: vcpu, Running: true, CPUCores: -1}
}

// no reserve: the numbers in the tests stay readable
func noReserve() Params {
	p := DefaultParams()
	p.HostReserve, p.PerOSD = 0, 0
	return p
}

func mustPlan(t *testing.T, nodes []Node, vms []VM, p Params) Plan {
	t.Helper()
	pl, err := Compute(nodes, vms, p)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func nodeState(pl Plan, name string) NodeState {
	for _, n := range pl.Nodes {
		if n.Name == name {
			return n
		}
	}
	return NodeState{}
}

func TestReserveFromHostAndOSDs(t *testing.T) {
	n := node("a", 100, 8, 3)
	m := node("b", 100, 8, 0)
	m.Reserve = 50 * GiB // explicit per-node value wins
	pl := mustPlan(t, []Node{n, m}, nil, DefaultParams())
	if a := nodeState(pl, "a"); a.Reserve != 20*GiB || a.Assignable != 80*GiB {
		t.Fatalf("a %#v", a)
	}
	if b := nodeState(pl, "b"); b.Reserve != 50*GiB || b.Assignable != 50*GiB {
		t.Fatalf("b %#v", b)
	}
}

func TestBalancesTwoNodes(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	vms := []VM{vm("a", 1, 40, 2), vm("a", 2, 20, 2), vm("a", 3, 20, 2)}
	pl := mustPlan(t, nodes, vms, noReserve())
	if len(pl.Steps) != 1 || pl.Steps[0].VMID != 1 || pl.Steps[0].To != "b" {
		t.Fatalf("steps %#v", pl.Steps)
	}
	if a, b := nodeState(pl, "a"), nodeState(pl, "b"); a.PctAfter != 40 || b.PctAfter != 40 {
		t.Fatalf("after a=%v b=%v", a.PctAfter, b.PctAfter)
	}
	if pl.SpreadBefore != 40 || pl.SpreadAfter != 0 || pl.Stop != "balanced" || pl.MovedBytes != 40*GiB {
		t.Fatalf("%#v", pl)
	}
	// deterministic
	if again := mustPlan(t, nodes, vms, noReserve()); !reflect.DeepEqual(again, pl) {
		t.Fatal("not deterministic")
	}
}

func TestPrefersFewerGBBetweenEquivalentMoves(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	// moving 30 or 31 GB gives nearly the same result: the smaller wins
	vms := []VM{vm("a", 1, 31, 1), vm("a", 2, 30, 1)}
	pl := mustPlan(t, nodes, vms, noReserve())
	if len(pl.Steps) != 1 || pl.Steps[0].VMID != 2 {
		t.Fatalf("%#v", pl.Steps)
	}
}

func TestNeverBeyondAssignableRAM(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	// b has 30 GB free: the 40 GB VM cannot go there even if it would even things out
	vms := []VM{vm("a", 1, 40, 1), vm("a", 2, 50, 1), vm("b", 3, 70, 1)}
	pl := mustPlan(t, nodes, vms, noReserve())
	for _, s := range pl.Steps {
		if s.VMID == 1 || s.VMID == 2 {
			t.Fatalf("moved a VM that does not fit: %#v", s)
		}
	}
	if b := nodeState(pl, "b"); b.LoadAfter > b.Assignable {
		t.Fatalf("b over limit %#v", b)
	}
}

func TestOverLimitNodeIsPrioritySourceNeverDestination(t *testing.T) {
	// D43 A: node a is already beyond its assignable RAM (like datacloud-node-10)
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), node("c", 100, 32, 0)}
	vms := []VM{vm("a", 1, 60, 1), vm("a", 2, 60, 1), vm("b", 3, 30, 1), vm("c", 4, 10, 1)}
	pl := mustPlan(t, nodes, vms, noReserve())
	if a := nodeState(pl, "a"); !a.OverBefore || a.OverAfter {
		t.Fatalf("a %#v", a)
	}
	if len(pl.Steps) == 0 || pl.Steps[0].From != "a" || pl.Steps[0].Why != "over_limit" {
		t.Fatalf("steps %#v", pl.Steps)
	}
	for _, s := range pl.Steps {
		if s.To == "a" {
			t.Fatalf("over-limit node received a VM: %#v", s)
		}
	}
}

func TestBlockedVMsStayAndAreListed(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	g := vm("a", 1, 60, 1)
	g.Blockers = []string{BlockGPU}
	l := vm("a", 2, 20, 1)
	l.Kind = "lxc"
	pl := mustPlan(t, nodes, []VM{g, l}, noReserve())
	if len(pl.Steps) != 0 || pl.Stop != "no_move" {
		t.Fatalf("hot plan: %#v", pl)
	}
	// sorted by key: a/lxc/2 before a/qemu/1
	if len(pl.Blocked) != 2 || pl.Blocked[0].Reasons[0] != BlockLXCRestart || pl.Blocked[1].Reasons[0] != BlockGPU {
		t.Fatalf("blocked %#v", pl.Blocked)
	}
	// D46 B: a cold plan may move the LXC
	p := noReserve()
	p.Cold = true
	pl = mustPlan(t, nodes, []VM{g, l}, p)
	if len(pl.Steps) != 1 || pl.Steps[0].Kind != "lxc" {
		t.Fatalf("cold plan: %#v", pl.Steps)
	}
}

func TestAllowedNodesFromProxmox(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), node("c", 100, 32, 0)}
	v := vm("a", 1, 40, 1)
	v.Allowed = []string{"c"}
	w := vm("a", 2, 30, 1)
	w.Allowed = []string{} // Proxmox allows no node
	pl := mustPlan(t, nodes, []VM{v, w, vm("a", 3, 10, 1)}, noReserve())
	for _, s := range pl.Steps {
		if s.VMID == 1 && s.To != "c" || s.VMID == 2 {
			t.Fatalf("%#v", s)
		}
	}
	found := false
	for _, b := range pl.Blocked {
		found = found || (b.VMID == 2 && b.Reasons[0] == BlockNoTarget)
	}
	if !found {
		t.Fatalf("%#v", pl.Blocked)
	}
}

func TestStoppedVMsToggle(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	off := vm("a", 1, 60, 1)
	off.Running = false
	vms := []VM{off, vm("b", 2, 20, 1)}
	// default (D45 C): stopped VMs count, a is at 60%, b at 20%
	if pl := mustPlan(t, nodes, vms, noReserve()); nodeState(pl, "a").PctBefore != 60 {
		t.Fatalf("%#v", pl.Nodes)
	}
	p := noReserve()
	p.CountStopped = false
	pl := mustPlan(t, nodes, vms, p)
	// a is at 0%, b at 20%: moving the 20 GB VM would only swap them, and the stopped VM never moves
	if nodeState(pl, "a").PctBefore != 0 || len(pl.Steps) != 0 {
		t.Fatalf("%#v %#v", pl.Nodes, pl.Steps)
	}
	if len(pl.Blocked) != 1 || pl.Blocked[0].Reasons[0] != BlockNotCounted {
		t.Fatalf("%#v", pl.Blocked)
	}
}

func TestOfflineNode(t *testing.T) {
	off := node("c", 100, 32, 0)
	off.Online = false
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), off}
	pl := mustPlan(t, nodes, []VM{vm("a", 1, 40, 1), vm("c", 2, 10, 1)}, noReserve())
	for _, s := range pl.Steps {
		if s.To == "c" || s.VMID == 2 {
			t.Fatalf("%#v", s)
		}
	}
	if len(pl.Blocked) != 1 || pl.Blocked[0].Reasons[0] != BlockNodeOffline {
		t.Fatalf("%#v", pl.Blocked)
	}
}

func TestEvacuateNode(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), node("c", 100, 32, 0)}
	gpu := vm("a", 3, 10, 1)
	gpu.Blockers = []string{BlockGPU}
	vms := []VM{vm("a", 1, 50, 1), vm("a", 2, 120, 1), gpu, vm("b", 4, 40, 1), vm("c", 5, 30, 1)}
	p := noReserve()
	p.Evacuate = []string{"a"}
	pl := mustPlan(t, nodes, vms, p)
	if len(pl.Steps) == 0 || pl.Steps[0].VMID != 1 || pl.Steps[0].Why != "evacuate" {
		t.Fatalf("steps %#v", pl.Steps)
	}
	for _, s := range pl.Steps {
		if s.To == "a" {
			t.Fatalf("evacuated node is a destination: %#v", s)
		}
	}
	if len(pl.Unplaced) != 2 {
		t.Fatalf("unplaced %#v", pl.Unplaced)
	}
	// largest first: the 120 GB VM fits nowhere (checked before the others move: c has 70 GB free), the GPU one cannot move
	if u := pl.Unplaced[0]; u.VMID != 2 || u.Reason != "no_room" || u.BestFree != 70*GiB {
		t.Fatalf("%#v", u)
	}
	if u := pl.Unplaced[1]; u.VMID != 3 || u.Reason != "blocked" {
		t.Fatalf("%#v", u)
	}
	if a := nodeState(pl, "a"); !a.Evacuated || a.VMsAfter != 2 {
		t.Fatalf("%#v", a)
	}
}

func TestCPUWarningsDoNotBlockButBreakTies(t *testing.T) {
	// b and c are equally good for RAM; c would go beyond 4 vCPU per thread (8 threads: warning above 32 vCPU).
	// c already has 31 vCPU in four small VMs (each one fits the 8 threads).
	onC := func() []VM {
		return []VM{vm("c", 3, 0, 8), vm("c", 4, 0, 8), vm("c", 5, 0, 8), vm("c", 6, 0, 7)}
	}
	nodes := []Node{node("a", 100, 8, 0), node("b", 100, 8, 0), node("c", 100, 8, 0)}
	vms := append([]VM{vm("a", 1, 30, 8), vm("a", 2, 30, 1)}, onC()...)
	pl := mustPlan(t, nodes, vms, noReserve())
	if len(pl.Steps) == 0 || pl.Steps[0].To != "b" {
		t.Fatalf("%#v", pl.Steps)
	}
	// only c can receive: the move happens anyway, with the warning
	fixed := vm("a", 2, 20, 0)
	fixed.Blockers = []string{BlockGPU}
	nodes = []Node{node("a", 100, 8, 0), node("c", 100, 8, 0)}
	pl = mustPlan(t, nodes, append([]VM{vm("a", 1, 40, 8), fixed}, onC()...), noReserve())
	if len(pl.Steps) != 1 || pl.Steps[0].VMID != 1 || !reflect.DeepEqual(pl.Steps[0].Warnings, []string{"cpu_vcpu"}) {
		t.Fatalf("%#v", pl.Steps)
	}
	if c := nodeState(pl, "c"); !reflect.DeepEqual(c.Warnings, []string{"cpu_vcpu"}) || len(c.WarningsBefore) != 0 {
		t.Fatalf("%#v", c)
	}
}

func TestRealCPUProjection(t *testing.T) {
	a, b := node("a", 100, 10, 0), node("b", 100, 10, 0)
	a.CPUP95, b.CPUP95 = 50, 75
	v := vm("a", 1, 40, 1)
	v.CPUCores = 1 // 10% of a, 10% of b
	fixed := vm("a", 2, 20, 1)
	fixed.Blockers = []string{BlockGPU}
	pl := mustPlan(t, []Node{a, b}, []VM{v, fixed}, noReserve())
	if len(pl.Steps) != 1 || !reflect.DeepEqual(pl.Steps[0].Warnings, []string{"cpu_p95"}) {
		t.Fatalf("%#v", pl.Steps)
	}
	if na, nb := nodeState(pl, "a"), nodeState(pl, "b"); na.CPUAfter != 40 || nb.CPUAfter != 85 || na.CPUBefore != 50 {
		t.Fatalf("a=%v b=%v", na, nb)
	}
}

func TestGroupSpreadBetweenEquivalentMoves(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), node("c", 100, 32, 0)}
	v := vm("a", 1, 30, 1)
	v.Groups = []string{"rossi"}
	onB := vm("b", 2, 10, 1)
	onB.Groups = []string{"rossi"}
	pl := mustPlan(t, nodes, []VM{v, vm("a", 3, 30, 1), onB, vm("c", 4, 10, 1)}, noReserve())
	for _, s := range pl.Steps {
		if s.VMID == 1 && s.To == "b" {
			t.Fatalf("same-group VM sent next to its group: %#v", pl.Steps)
		}
	}
}

func TestMaxStepsAndPrefixes(t *testing.T) {
	nodes := []Node{node("a", 1000, 64, 0), node("b", 1000, 64, 0)}
	var vms []VM
	for i := 0; i < 40; i++ {
		vms = append(vms, vm("a", 100+i, 10, 1))
	}
	p := noReserve()
	p.MaxSteps = 3
	pl := mustPlan(t, nodes, vms, p)
	if len(pl.Steps) != 3 || pl.Stop != "max_steps" {
		t.Fatalf("%d %s", len(pl.Steps), pl.Stop)
	}
	for i := 1; i < len(pl.Steps); i++ {
		if pl.Steps[i].SpreadAfter > pl.Steps[i-1].SpreadAfter {
			t.Fatal("a balancing step made things worse")
		}
	}
}

func TestEdgeCases(t *testing.T) {
	if pl := mustPlan(t, []Node{node("a", 100, 8, 0)}, []VM{vm("a", 1, 10, 1)}, noReserve()); pl.Stop != "no_nodes" || len(pl.Steps) != 0 {
		t.Fatalf("one node: %#v", pl)
	}
	if pl := mustPlan(t, []Node{node("a", 100, 8, 0), node("b", 100, 8, 0)}, []VM{vm("a", 1, 10, 1), vm("b", 2, 10, 1)}, noReserve()); pl.Stop != "balanced" || len(pl.Steps) != 0 {
		t.Fatalf("already even: %#v", pl)
	}
	// reserve larger than the RAM: nothing assignable, no panic, nothing goes there
	tiny := node("t", 4, 8, 0)
	pl := mustPlan(t, []Node{node("a", 100, 8, 0), tiny}, []VM{vm("a", 1, 10, 1)}, DefaultParams())
	if nodeState(pl, "t").Assignable != 0 || len(pl.Steps) != 0 {
		t.Fatalf("%#v", pl)
	}
}

func TestInvalidInput(t *testing.T) {
	good := []Node{node("a", 100, 8, 0)}
	bad := []struct {
		name  string
		nodes []Node
		vms   []VM
		p     func(*Params)
	}{
		{"no nodes", nil, nil, nil},
		{"duplicate node", []Node{node("a", 1, 1, 0), node("a", 1, 1, 0)}, nil, nil},
		{"unknown node", good, []VM{vm("x", 1, 1, 1)}, nil},
		{"negative mem", good, []VM{vm("a", 1, -1, 1)}, nil},
		{"steps", good, nil, func(p *Params) { p.MaxSteps = 0 }},
		{"steps high", good, nil, func(p *Params) { p.MaxSteps = 51 }},
		{"reserve", good, nil, func(p *Params) { p.HostReserve = -1 }},
		{"osd", good, nil, func(p *Params) { p.PerOSD = 65 * GiB }},
		{"vcpu", good, nil, func(p *Params) { p.VCPUPerCore = 0 }},
		{"cpu pct", good, nil, func(p *Params) { p.CPUWarnPct = 101 }},
	}
	for _, c := range bad {
		p := DefaultParams()
		if c.p != nil {
			c.p(&p)
		}
		if _, err := Compute(c.nodes, c.vms, p); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}

// Invariants on random clusters: RAM conserved, no destination beyond its limit after any step,
// blocked VMs never moved, steps capped, balancing never worsens the spread.
func TestInvariantsRandomClusters(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for round := 0; round < 300; round++ {
		var nodes []Node
		nn := 2 + r.Intn(10)
		for i := 0; i < nn; i++ {
			n := node(string(rune('a'+i)), int64(64+r.Intn(2000)), 8+r.Intn(250), r.Intn(5))
			n.Online = r.Intn(12) > 0
			n.CPUP95 = float64(r.Intn(100))
			n.CPUVendor = []string{"", "amd", "intel"}[r.Intn(3)]
			nodes = append(nodes, n)
		}
		var vms []VM
		for i := 0; i < r.Intn(120); i++ {
			v := vm(nodes[r.Intn(nn)].Name, 100+i, int64(1+r.Intn(200)), 1+r.Intn(32))
			v.Running = r.Intn(5) > 0
			v.CPUCores = r.Float64() * 8
			if r.Intn(6) == 0 {
				v.Blockers = []string{BlockGPU}
			}
			if r.Intn(10) == 0 {
				v.Kind = "lxc"
			}
			v.HostCPU = r.Intn(2) == 0
			if r.Intn(8) == 0 {
				v.VCPU = 32 + r.Intn(300) // wider than many nodes
			}
			vms = append(vms, v)
		}
		p := DefaultParams()
		p.CountStopped = r.Intn(2) == 0
		p.Cold = r.Intn(2) == 0
		if r.Intn(4) == 0 {
			p.Evacuate = []string{nodes[r.Intn(nn)].Name}
		}
		for _, v := range vms {
			if r.Intn(5) == 0 && len(p.ColdVMs) < 64 {
				p.ColdVMs = append(p.ColdVMs, v.VMID)
			}
			if r.Intn(8) == 0 && len(p.Only) < 64 {
				p.Only = append(p.Only, v.VMID)
			}
		}
		pl, err := Compute(nodes, vms, p)
		if err != nil {
			t.Fatal(err)
		}
		checkInvariants(t, round, nodes, vms, p, pl)
	}
}

func checkInvariants(t *testing.T, round int, nodes []Node, vms []VM, p Params, pl Plan) {
	t.Helper()
	blocked := map[string]bool{}
	for _, b := range pl.Blocked {
		if b.Reasons[0] != BlockNotCounted {
			blocked[b.Key] = true
		}
	}
	at := map[string]string{}
	mem := map[string]int64{}
	counted := map[string]bool{}
	for _, v := range vms {
		at[v.Key()], mem[v.Key()] = v.Node, v.Mem
		counted[v.Key()] = v.Running || p.CountStopped
	}
	assign := map[string]int64{}
	load := map[string]int64{}
	var total int64
	for _, n := range pl.Nodes {
		assign[n.Name], load[n.Name] = n.Assignable, n.LoadBefore
		total += n.LoadBefore
	}
	model := map[string]string{}
	for _, n := range nodes {
		model[n.Name] = n.CPUVendor
	}
	byKey := map[string]VM{}
	for _, v := range vms {
		byKey[v.Key()] = v
	}
	only := map[int]bool{}
	for _, id := range p.Only {
		only[id] = true
	}
	coldSet := map[int]bool{}
	for _, id := range p.ColdVMs {
		coldSet[id] = true
	}
	threads := map[string]int{}
	for _, n := range nodes {
		threads[n.Name] = n.Cores
	}
	balancing := 0
	for _, s := range pl.Steps {
		v := byKey[s.Key]
		if v.VCPU > threads[s.To] || threads[s.To] <= 0 {
			t.Fatalf("round %d: %d vCPU sent to %s with %d threads %#v", round, v.VCPU, s.To, threads[s.To], s)
		}
		live := v.Kind == "qemu" && v.Running && !coldSet[v.VMID]
		if live && v.HostCPU && (model[s.From] == "" || model[s.From] != model[s.To]) {
			t.Fatalf("round %d: live move of a host-CPU VM across CPU vendors %#v", round, s)
		}
		if s.Cold != (v.Kind == "qemu" && v.Running && coldSet[v.VMID]) {
			t.Fatalf("round %d: wrong cold flag %#v", round, s)
		}
		if len(p.Only) > 0 && !only[v.VMID] {
			t.Fatalf("round %d: VM outside the filter moved %#v", round, s)
		}
		if blocked[s.Key] {
			t.Fatalf("round %d: blocked VM moved %#v", round, s)
		}
		if at[s.Key] != s.From {
			t.Fatalf("round %d: step from the wrong node %#v", round, s)
		}
		if counted[s.Key] {
			load[s.From] -= mem[s.Key]
			load[s.To] += mem[s.Key]
			if load[s.To] > assign[s.To] {
				t.Fatalf("round %d: %s beyond its limit after step %d", round, s.To, s.N)
			}
		}
		at[s.Key] = s.To
		if s.Why != "evacuate" {
			balancing++
		}
	}
	var after int64
	for _, n := range pl.Nodes {
		if load[n.Name] != n.LoadAfter {
			t.Fatalf("round %d: %s load %d, plan says %d", round, n.Name, load[n.Name], n.LoadAfter)
		}
		after += n.LoadAfter
	}
	if after != total {
		t.Fatalf("round %d: RAM not conserved", round)
	}
	if balancing > p.MaxSteps {
		t.Fatalf("round %d: %d balancing steps", round, balancing)
	}
	if len(p.Evacuate) == 0 && pl.SpreadAfter > pl.SpreadBefore {
		t.Fatalf("round %d: spread worse %v > %v", round, pl.SpreadAfter, pl.SpreadBefore)
	}
}

func TestHostCPULiveOnlyToTheSameVendor(t *testing.T) {
	amd, intel, intel2 := node("amd", 1000, 64, 0), node("intel", 1000, 64, 0), node("intel2", 1000, 64, 0)
	amd.CPUVendor, intel.CPUVendor, intel2.CPUVendor = "amd", "intel", "intel" // intel2: another Intel model
	host := vm("amd", 1, 300, 4)
	host.HostCPU = true
	host2 := vm("amd", 5, 300, 4)
	host2.HostCPU = true
	// only the AMD node has the VMs' CPU: live is impossible, both blocked with cpu_host
	pl := mustPlan(t, []Node{amd, intel}, []VM{host, host2}, noReserve())
	if len(pl.Steps) != 0 || len(pl.Blocked) != 2 || pl.Blocked[0].Reasons[0] != BlockCPUHost {
		t.Fatalf("steps %#v blocked %#v", pl.Steps, pl.Blocked)
	}
	// VM 1 chosen cold: it may go to the Intel node, the step says cold; VM 5 stays blocked
	p := noReserve()
	p.ColdVMs = []int{1}
	pl = mustPlan(t, []Node{amd, intel}, []VM{host, host2}, p)
	if len(pl.Steps) != 1 || pl.Steps[0].VMID != 1 || !pl.Steps[0].Cold || pl.Steps[0].SameVendor || pl.Steps[0].To != "intel" {
		t.Fatalf("cold %#v", pl.Steps)
	}
	// Intel host VM: live only to the other Intel of the same model, flagged same_cpu
	ih, ih2 := vm("intel", 2, 300, 4), vm("intel", 6, 300, 4)
	ih.HostCPU, ih2.HostCPU = true, true
	pl = mustPlan(t, []Node{amd, intel, intel2}, []VM{ih, ih2}, noReserve())
	if len(pl.Steps) != 1 || pl.Steps[0].To != "intel2" || !pl.Steps[0].SameVendor || pl.Steps[0].Cold {
		t.Fatalf("same model %#v", pl.Steps)
	}
	// a generic CPU type moves freely; an unknown model is never a live destination for a host VM
	gen, gen2 := vm("amd", 3, 300, 4), vm("amd", 7, 300, 4)
	unk := node("unknown", 1000, 64, 0)
	pl = mustPlan(t, []Node{amd, unk}, []VM{gen, gen2}, noReserve())
	if len(pl.Steps) != 1 {
		t.Fatalf("generic %#v", pl.Steps)
	}
	amdU := amd
	amdU.CPUVendor = ""
	unk2 := node("unknown2", 1000, 64, 0)
	pl = mustPlan(t, []Node{amdU, unk2}, []VM{host, host2}, noReserve())
	if len(pl.Steps) != 0 || pl.Blocked[0].Reasons[0] != BlockCPUHost {
		t.Fatalf("unknown models must not match: %#v", pl.Steps)
	}
	// a stopped host-CPU VM is not live: free
	off, off2 := host, host2
	off.Running, off2.Running = false, false
	pl = mustPlan(t, []Node{amd, intel}, []VM{off, off2}, noReserve())
	if len(pl.Steps) != 1 || pl.Steps[0].Cold {
		t.Fatalf("stopped %#v", pl.Steps)
	}
}

func TestOnlyFilter(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	vms := []VM{vm("a", 1, 40, 2), vm("a", 2, 10, 2), vm("a", 3, 20, 2)}
	gpu := vm("a", 4, 5, 2)
	gpu.Blockers = []string{BlockGPU}
	vms = append(vms, gpu)
	p := noReserve()
	p.Only = []int{2, 4}
	pl := mustPlan(t, nodes, vms, p)
	if len(pl.Steps) != 1 || pl.Steps[0].VMID != 2 {
		t.Fatalf("steps %#v", pl.Steps)
	}
	// only VMs in the filter are listed as blocked
	if len(pl.Blocked) != 1 || pl.Blocked[0].VMID != 4 {
		t.Fatalf("blocked %#v", pl.Blocked)
	}
	// evacuation with a filter: the others stay and say why
	p.Evacuate = []string{"a"}
	pl = mustPlan(t, nodes, vms, p)
	pinned := 0
	for _, u := range pl.Unplaced {
		if u.Reason == "pinned" {
			pinned++
		}
	}
	if pinned != 2 {
		t.Fatalf("unplaced %#v", pl.Unplaced)
	}
	p = noReserve()
	p.Only = make([]int, 65)
	if _, err := Compute(nodes, vms, p); err == nil {
		t.Fatal("65 VMIDs accepted")
	}
}

func TestVCPUsNeverBeyondTheThreadsOfTheDestination(t *testing.T) {
	// the real case: an 80-vCPU VM on an overloaded 256-thread node, a 64-thread node with room
	big, small, other := node("n10", 1000, 256, 0), node("n5", 1000, 64, 0), node("n4", 300, 80, 0)
	wide := vm("n10", 177, 160, 80)
	vms := []VM{wide, vm("n10", 1, 600, 8), vm("n4", 2, 250, 8)}
	pl := mustPlan(t, []Node{big, small, other}, vms, noReserve())
	for _, s := range pl.Steps {
		if s.VMID == 177 && s.To == "n5" {
			t.Fatalf("80 vCPU planned on 64 threads: %#v", s)
		}
	}
	// no node with enough threads apart from its own: blocked with the reason
	pl = mustPlan(t, []Node{big, small}, []VM{wide, vm("n10", 1, 600, 8)}, noReserve())
	found := false
	for _, b := range pl.Blocked {
		if b.VMID == 177 {
			found = contains(b.Reasons, BlockVCPU)
		}
	}
	if !found {
		t.Fatalf("not blocked: %#v", pl.Blocked)
	}
	// exactly as many vCPUs as threads is fine
	eq := vm("n10", 300, 400, 64)
	pl = mustPlan(t, []Node{big, small}, []VM{eq, vm("n10", 1, 400, 8)}, noReserve())
	if len(pl.Steps) == 0 || pl.Steps[0].To != "n5" {
		t.Fatalf("64 vCPU on 64 threads refused: %#v", pl.Steps)
	}
	// unknown thread count: never a destination
	unk := node("unk", 1000, 0, 0)
	pl = mustPlan(t, []Node{big, unk}, []VM{vm("n10", 5, 400, 2), vm("n10", 6, 400, 2)}, noReserve())
	if len(pl.Steps) != 0 {
		t.Fatalf("moved to a node with unknown threads: %#v", pl.Steps)
	}
}
