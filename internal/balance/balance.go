// Package balance computes a plan of VM migrations that evens out the RAM load of the nodes of a cluster
// (ROADMAP 3, decisions D43-D49 in DOMANDE.md). It is pure: no I/O, no clock, no randomness. The same input
// always gives the same plan, so every step can be explained and recomputed right before it is executed.
//
// Model:
//   - assignable RAM of a node = physical RAM - reserve; reserve = host reserve + per-OSD reserve x OSDs
//     (or an explicit per-node value);
//   - load of a node = RAM assigned to its counted VMs (running ones; stopped ones too if CountStopped, D45);
//   - ratio = load / assignable. The goal is to make the ratios as equal as possible (D44: assigned RAM);
//   - hard rule: a migration never takes the destination beyond its assignable RAM (D43 A). A node that is
//     already beyond it can only lose VMs: it is a priority source, never a destination;
//   - CPU (vCPU per core, real CPU p95) only produces warnings, never blocks (decision 2026-11-03);
//   - same-group VMs: soft preference to spread them (D49 B), used only between equivalent moves.
//
// The plan is greedy: each step is the best single move on the state left by the previous steps, so every
// prefix of the plan is itself a valid plan, and the page can execute and recompute one step at a time (D47).
package balance

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	GiB = int64(1) << 30

	maxNodes = 256
	maxVMs   = 5000
	maxPick  = 64 // VMIDs in Only / ColdVMs

	// equivalentShare: moves whose gain is at least this share of the best gain are "equivalent"; among them
	// the tie-breaks decide (destination without CPU warnings, fewer GB moved, group spread, name).
	equivalentShare = 0.9
)

// Blocking reasons for a VM that cannot be moved (codes: the page writes the sentences).
const (
	BlockGPU         = "gpu"            // hostpci with a video card (direct or mapping): the card belongs to the node
	BlockLocalRes    = "local_resource" // other PCI/USB devices bound to the node
	BlockLocalDisk   = "local_disk"     // a disk on a storage that is not shared
	BlockLock        = "lock"           // backup, migration, snapshot in progress
	BlockTemplate    = "template"
	BlockHA          = "ha"           // managed by HA: the HA manager decides where it runs
	BlockLXCRestart  = "lxc_restart"  // LXC can move only with a restart, and the plan is "hot" (D46)
	BlockNoTarget    = "no_target"    // Proxmox allows no other node
	BlockNodeOffline = "node_offline" // the node it lives on is offline
	BlockNotCounted  = "not_counted"  // stopped VM that does not count (moving it changes nothing)
	BlockUnverified  = "unverified"   // Proxmox's migration precheck could not be read: never guess
	// BlockCPUHost: running QEMU VM with the host CPU type (cpu: host|max|custom-*), migrated live: the guest sees
	// the physical CPU, so it can only resume on a node with the same CPU vendor (D55, incident of VM 109 on
	// 2026-11-04: AMD -> Intel live migration, the VM died at resume). Set when no such node can take it.
	BlockCPUHost = "cpu_host"
	BlockPinned  = "pinned" // not in the "only these VMs" filter (D54 A): stays where it is
	// BlockVCPU: the VM has more vCPUs than the threads of every node that could receive it. Proxmox refuses to
	// run a VM with more vCPUs than the host has (found on 2026-11-04: an 80-vCPU VM planned to a 64-thread node).
	BlockVCPU = "vcpu_over_threads"
)

// Node is one node of the cluster as seen now.
type Node struct {
	Name     string
	Online   bool
	MemTotal int64   // physical RAM, bytes
	Cores    int     // physical CPU threads as Proxmox reports them (maxcpu)
	OSDs     int     // Ceph OSDs running on the node
	CPUP95   float64 // real CPU use p95 over 4 weeks, percent 0-100; < 0 = unknown
	// Reserve, if >= 0, replaces the automatic reserve (host + per OSD) for this node.
	Reserve int64
	// CPUFlags: the CPU feature flags of the node (cpuinfo.flags). A VM with the host CPU type resumes after a live
	// migration only on a node that has ALL the flags of the source (D63 B, checked on 2026-11-05 on every pair
	// of CPU models of the cluster). Empty = unknown: never a live destination for a host-CPU VM.
	CPUFlags map[string]bool
}

// VM is one guest (QEMU or LXC). Blockers are filled by the caller from the config and from Proxmox's
// own migration precheck; the planner adds the ones that depend on the parameters.
type VM struct {
	Node     string
	Kind     string // qemu | lxc
	VMID     int
	Name     string
	Mem      int64 // assigned RAM, bytes
	VCPU     int
	Running  bool
	Groups   []string
	CPUCores float64  // real CPU use p95 in cores (e.g. 3.2); < 0 = unknown
	Allowed  []string // nodes Proxmox allows (nil = every online node)
	Blockers []string
	HostCPU  bool // cpu type host/max/custom: a live migration needs the same CPU model on the destination
}

// Key identifies a VM inside a plan.
func (v VM) Key() string { return fmt.Sprintf("%s/%s/%d", v.Node, v.Kind, v.VMID) }

// Params are the knobs of the page. Defaults: see DefaultParams.
type Params struct {
	HostReserve  int64   // bytes per node for the host (default 8 GiB)
	PerOSD       int64   // bytes per Ceph OSD (default 4 GiB)
	CountStopped bool    // D45: stopped VMs count in the load (default true)
	Cold         bool    // D46: migration with shutdown/restart: LXC become movable
	MaxSteps     int     // D47: balancing steps (evacuation steps are not capped)
	VCPUPerCore  float64 // CPU warning: vCPU assigned per physical core
	CPUWarnPct   float64 // CPU warning: projected real CPU p95, percent
	MinGainPts   float64 // stop when a step improves the spread (std dev, points of %) by less than this
	// Evacuate lists nodes to empty ("what if I shut node X down"): all their VMs must move elsewhere.
	Evacuate []string
	// Only, if not empty, lists the VMIDs that may move (D54 A); every other guest stays where it is.
	Only []int
	// ColdVMs lists the running QEMU VMIDs the user wants moved cold (shutdown, migrate, start: D50).
	// It changes the plan: a cold move has no CPU vendor constraint (D55).
	ColdVMs []int
}

// DefaultParams are the values approved on 2026-11-03 (D43-D47).
func DefaultParams() Params {
	return Params{HostReserve: 8 * GiB, PerOSD: 4 * GiB, CountStopped: true, MaxSteps: 10,
		VCPUPerCore: 4, CPUWarnPct: 80, MinGainPts: 0.1}
}

// Validate checks the parameters against fixed bounds (they come from the page).
func (p Params) Validate() error {
	switch {
	case p.HostReserve < 0 || p.HostReserve > 512*GiB:
		return errors.New("riserva per l'host fuori dai limiti (0-512 GB)")
	case p.PerOSD < 0 || p.PerOSD > 64*GiB:
		return errors.New("riserva per OSD fuori dai limiti (0-64 GB)")
	case p.MaxSteps < 1 || p.MaxSteps > 50:
		return errors.New("numero di passi fuori dai limiti (1-50)")
	case p.VCPUPerCore < 0.5 || p.VCPUPerCore > 64:
		return errors.New("vCPU per core fuori dai limiti (0,5-64)")
	case p.CPUWarnPct < 1 || p.CPUWarnPct > 100:
		return errors.New("soglia CPU fuori dai limiti (1-100%)")
	case p.MinGainPts < 0 || p.MinGainPts > 10:
		return errors.New("guadagno minimo fuori dai limiti (0-10 punti)")
	case len(p.Evacuate) > maxNodes:
		return errors.New("troppi nodi da svuotare")
	case len(p.Only) > maxPick || len(p.ColdVMs) > maxPick:
		return errors.New("troppe VM indicate (massimo 64)")
	}
	return nil
}

// NodeState is a node before and after the plan.
type NodeState struct {
	Name           string   `json:"name"`
	Online         bool     `json:"online"`
	Evacuated      bool     `json:"evacuated"`
	MemTotal       int64    `json:"mem_total"`
	Reserve        int64    `json:"reserve"`
	Assignable     int64    `json:"assignable"`
	OSDs           int      `json:"osds"`
	Cores          int      `json:"cores"`
	LoadBefore     int64    `json:"load_before"`
	LoadAfter      int64    `json:"load_after"`
	PctBefore      float64  `json:"pct_before"` // load / assignable, percent
	PctAfter       float64  `json:"pct_after"`
	VCPUBefore     int      `json:"vcpu_before"`
	VCPUAfter      int      `json:"vcpu_after"`
	CPUBefore      float64  `json:"cpu_p95_before"` // < 0 = unknown
	CPUAfter       float64  `json:"cpu_p95_after"`
	VMsBefore      int      `json:"vms_before"`
	VMsAfter       int      `json:"vms_after"`
	OverBefore     bool     `json:"over_before"` // load beyond assignable RAM (D43: priority source)
	OverAfter      bool     `json:"over_after"`
	WarningsBefore []string `json:"warnings_before"` // CPU warnings already present: cpu_vcpu | cpu_p95
	Warnings       []string `json:"warnings"`        // after the plan
}

// Step is one migration of the plan.
type Step struct {
	N          int      `json:"n"`
	Key        string   `json:"key"`
	Kind       string   `json:"kind"`
	VMID       int      `json:"vmid"`
	Name       string   `json:"name"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Mem        int64    `json:"mem"`
	Running    bool     `json:"running"`
	Cold       bool     `json:"cold"`        // running QEMU VM moved with shutdown, migrate, start (chosen by the user)
	CPUChecked bool     `json:"cpu_checked"` // live move of a host-CPU VM: the destination has every CPU flag of the source
	Why        string   `json:"why"`         // evacuate | over_limit | balance
	Warnings   []string `json:"warnings"`    // CPU warnings of the destination after this step (new or already there)
	// spread of the online nodes (std dev of the %, points) after this step
	SpreadAfter float64 `json:"spread_after"`
}

// Blocked is a VM the planner will not move, with every reason that applies.
type Blocked struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"`
	VMID    int      `json:"vmid"`
	Name    string   `json:"name"`
	Node    string   `json:"node"`
	Mem     int64    `json:"mem"`
	Reasons []string `json:"reasons"`
}

// Unplaced is a VM of an evacuated node that fits nowhere.
type Unplaced struct {
	Key    string `json:"key"`
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Mem    int64  `json:"mem"`
	Reason string `json:"reason"` // blocked | pinned | no_room
	// for no_room: the most free assignable RAM on any destination
	BestFree int64 `json:"best_free"`
}

// Plan is the result.
type Plan struct {
	Nodes        []NodeState `json:"nodes"`
	Steps        []Step      `json:"steps"`
	Blocked      []Blocked   `json:"blocked"`
	Unplaced     []Unplaced  `json:"unplaced"`
	TargetPct    float64     `json:"target_pct"`    // total load / total assignable of the destination nodes
	SpreadBefore float64     `json:"spread_before"` // std dev of the %, points
	SpreadAfter  float64     `json:"spread_after"`
	MovedBytes   int64       `json:"moved_bytes"`
	Stop         string      `json:"stop"` // balanced | max_steps | no_move | no_nodes
}

// ── planner state ───────────────────────────────────────────────────────────

type nodeRT struct {
	in      *Node
	evac    bool
	assign  int64
	reserve int64
	load    int64
	vcpu    int
	cpu     float64 // projected p95 percent (< 0 unknown)
	vms     int
	groups  map[string]int
}

type vmRT struct {
	in      *VM
	at      string
	counted bool
	blocked []string
	moved   bool
	cold    bool // chosen cold by the user (running QEMU only)
	pinned  bool // outside the Only filter
}

// needsSameCPU: a live migration of a host-CPU QEMU VM. History: D55 same model, then same vendor (2026-11-04),
// then D63 B (2026-11-05): the destination must have every CPU flag of the source, because two live migrations
// from an EPYC 7713 to an EPYC 7302 (same vendor, older generation) died at resume.
func (v *vmRT) needsSameCPU() bool {
	return v.in.Kind == "qemu" && v.in.Running && !v.cold && v.in.HostCPU
}

type state struct {
	p     Params
	nodes map[string]*nodeRT
	names []string // sorted node names
	vms   []*vmRT  // sorted by key
}

// Compute builds the plan. It returns an error only for invalid input.
func Compute(nodes []Node, vms []VM, p Params) (Plan, error) {
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	if len(nodes) == 0 || len(nodes) > maxNodes {
		return Plan{}, errors.New("numero di nodi non valido")
	}
	if len(vms) > maxVMs {
		return Plan{}, errors.New("troppe VM")
	}
	st, err := newState(nodes, vms, p)
	if err != nil {
		return Plan{}, err
	}
	before := st.snapshot()
	plan := Plan{Steps: []Step{}, Blocked: []Blocked{}, Unplaced: []Unplaced{}}
	plan.SpreadBefore = round2(st.spread())
	st.evacuate(&plan)
	plan.Stop = st.balance(&plan)
	plan.TargetPct = round2(st.target() * 100)
	plan.SpreadAfter = round2(st.spread())
	plan.Nodes = st.nodeStates(before)
	plan.Blocked = st.blockedList()
	for _, s := range plan.Steps {
		plan.MovedBytes += s.Mem
	}
	return plan, nil
}

func newState(nodes []Node, vms []VM, p Params) (*state, error) {
	st := &state{p: p, nodes: map[string]*nodeRT{}}
	evac := map[string]bool{}
	for _, n := range p.Evacuate {
		evac[n] = true
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Name == "" || st.nodes[n.Name] != nil {
			return nil, fmt.Errorf("nodo duplicato o senza nome: %q", n.Name)
		}
		if n.MemTotal < 0 || n.Cores < 0 || n.OSDs < 0 || n.OSDs > 1000 {
			return nil, fmt.Errorf("valori non validi per il nodo %q", n.Name)
		}
		reserve := p.HostReserve + p.PerOSD*int64(n.OSDs)
		if n.Reserve >= 0 {
			reserve = n.Reserve
		}
		assign := n.MemTotal - reserve
		if assign < 0 {
			assign = 0
		}
		cpu := -1.0
		if n.CPUP95 >= 0 && n.CPUP95 <= 100 {
			cpu = n.CPUP95
		}
		st.nodes[n.Name] = &nodeRT{in: n, evac: evac[n.Name], assign: assign, reserve: reserve, cpu: cpu, groups: map[string]int{}}
		st.names = append(st.names, n.Name)
	}
	sort.Strings(st.names)
	only, cold := map[int]bool{}, map[int]bool{}
	for _, id := range p.Only {
		only[id] = true
	}
	for _, id := range p.ColdVMs {
		cold[id] = true
	}
	for i := range vms {
		v := &vms[i]
		nd := st.nodes[v.Node]
		if nd == nil {
			return nil, fmt.Errorf("VM %d su un nodo sconosciuto", v.VMID)
		}
		if v.Mem < 0 || v.VCPU < 0 {
			return nil, fmt.Errorf("valori non validi per la VM %d", v.VMID)
		}
		r := &vmRT{in: v, at: v.Node, counted: v.Running || p.CountStopped,
			cold: cold[v.VMID] && v.Kind == "qemu" && v.Running, pinned: len(only) > 0 && !only[v.VMID]}
		r.blocked = st.blockersOf(r, nd)
		st.vms = append(st.vms, r)
		nd.vms++
		for _, g := range v.Groups {
			nd.groups[g]++
		}
		if r.counted {
			nd.load += v.Mem
			nd.vcpu += v.VCPU
		}
	}
	sort.Slice(st.vms, func(i, j int) bool { return st.vms[i].in.Key() < st.vms[j].in.Key() })
	return st, nil
}

// blockersOf merges the caller's reasons with the ones that depend on the parameters, sorted and unique.
func (st *state) blockersOf(r *vmRT, nd *nodeRT) []string {
	v := r.in
	set := map[string]bool{}
	if r.pinned {
		set[BlockPinned] = true
	}
	if r.needsSameCPU() && !st.sameCPUExists(r, nd) {
		set[BlockCPUHost] = true
	}
	if !st.enoughThreadsExists(r, nd) {
		set[BlockVCPU] = true
	}
	for _, b := range v.Blockers {
		set[b] = true
	}
	if v.Kind == "lxc" && !st.p.Cold {
		set[BlockLXCRestart] = true
	}
	if !nd.in.Online {
		set[BlockNodeOffline] = true
	}
	if v.Allowed != nil && len(v.Allowed) == 0 {
		set[BlockNoTarget] = true
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// sameCPUExists: another destination node whose CPU flags include the source's, that Proxmox allows.
func (st *state) sameCPUExists(r *vmRT, from *nodeRT) bool {
	for _, name := range st.names {
		n := st.nodes[name]
		if n == from || !st.dest(n) || !sameCPU(from, n) {
			continue
		}
		if r.in.Allowed == nil || contains(r.in.Allowed, name) {
			return true
		}
	}
	return false
}

// enoughThreads: hard rule, a node runs a VM only if it has at least as many threads as the VM has vCPUs.
// A node whose thread count is unknown (0) is never a destination: it cannot be checked.
func enoughThreads(v *VM, n *nodeRT) bool { return n.in.Cores > 0 && v.VCPU <= n.in.Cores }

// enoughThreadsExists: another destination node with enough threads that Proxmox allows.
func (st *state) enoughThreadsExists(r *vmRT, from *nodeRT) bool {
	for _, name := range st.names {
		n := st.nodes[name]
		if n == from || !st.dest(n) || !enoughThreads(r.in, n) {
			continue
		}
		if r.in.Allowed == nil || contains(r.in.Allowed, name) {
			return true
		}
	}
	return false
}

// sameCPU: b can run what a's CPU offers to a host-CPU guest (b's flags include all of a's; unknown = no).
func sameCPU(a, b *nodeRT) bool {
	if len(a.in.CPUFlags) == 0 || len(b.in.CPUFlags) == 0 {
		return false
	}
	for f := range a.in.CPUFlags {
		if !b.in.CPUFlags[f] {
			return false
		}
	}
	return true
}

// dest reports whether a node can receive VMs at all.
func (st *state) dest(n *nodeRT) bool { return n.in.Online && !n.evac }

// target is the ratio every destination node would have in a perfect split.
func (st *state) target() float64 {
	var load, assign int64
	for _, name := range st.names {
		n := st.nodes[name]
		if st.dest(n) {
			load += n.load
			assign += n.assign
		}
	}
	if assign == 0 {
		return 0
	}
	return float64(load) / float64(assign)
}

func ratio(load, assign int64) float64 {
	if assign <= 0 {
		if load > 0 {
			return 10 // nothing assignable but load present: as bad as it gets, finite for the arithmetic
		}
		return 0
	}
	return float64(load) / float64(assign)
}

// spread is the standard deviation of the destination nodes' % (points), unrounded: used for every
// comparison; only the values written in the plan are rounded.
func (st *state) spread() float64 {
	var rs []float64
	for _, name := range st.names {
		n := st.nodes[name]
		if st.dest(n) {
			rs = append(rs, ratio(n.load, n.assign)*100)
		}
	}
	return stddev(rs)
}

func stddev(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var s float64
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)))
}

// ── moves ───────────────────────────────────────────────────────────────────

type move struct {
	v        *vmRT
	to       *nodeRT
	gain     float64 // decrease of the spread, points (negative when the move makes it worse, as evacuations can)
	warns    []string
	groupHit int
}

// fits applies the hard rules for moving v to n (D43 A: never beyond the assignable RAM).
func (st *state) fits(v *vmRT, n *nodeRT) bool {
	if !st.dest(n) || n.in.Name == v.at {
		return false
	}
	if v.in.Allowed != nil && !contains(v.in.Allowed, n.in.Name) {
		return false
	}
	if v.needsSameCPU() && !sameCPU(st.nodes[v.at], n) {
		return false
	}
	if !enoughThreads(v.in, n) {
		return false
	}
	if v.counted && n.load+v.in.Mem > n.assign {
		return false
	}
	return true
}

// apply moves v to n and returns a function that undoes it (used to evaluate candidates).
func (st *state) apply(v *vmRT, n *nodeRT) func() {
	from := st.nodes[v.at]
	cpuFrom, cpuTo := from.cpu, n.cpu
	if v.counted {
		from.load -= v.in.Mem
		n.load += v.in.Mem
		from.vcpu -= v.in.VCPU
		n.vcpu += v.in.VCPU
		if v.in.CPUCores >= 0 {
			if from.cpu >= 0 && from.in.Cores > 0 {
				from.cpu = math.Max(0, from.cpu-v.in.CPUCores/float64(from.in.Cores)*100)
			}
			if n.cpu >= 0 && n.in.Cores > 0 {
				n.cpu += v.in.CPUCores / float64(n.in.Cores) * 100
			}
		}
	}
	from.vms--
	n.vms++
	for _, g := range v.in.Groups {
		from.groups[g]--
		n.groups[g]++
	}
	prevAt := v.at
	v.at = n.in.Name
	return func() {
		v.at = prevAt
		for _, g := range v.in.Groups {
			from.groups[g]++
			n.groups[g]--
		}
		from.vms++
		n.vms--
		if v.counted {
			from.load += v.in.Mem
			n.load -= v.in.Mem
			from.vcpu += v.in.VCPU
			n.vcpu -= v.in.VCPU
		}
		from.cpu, n.cpu = cpuFrom, cpuTo
	}
}

// cpuWarnings are the CPU warnings of node n in its current state (non-blocking).
func (st *state) cpuWarnings(n *nodeRT) []string {
	w := []string{}
	if n.in.Cores > 0 && float64(n.vcpu) > st.p.VCPUPerCore*float64(n.in.Cores) {
		w = append(w, "cpu_vcpu")
	}
	if n.cpu >= 0 && n.cpu > st.p.CPUWarnPct {
		w = append(w, "cpu_p95")
	}
	return w
}

// evaluate fills a candidate move: gain, the CPU warnings of the destination after it, group crowding.
func (st *state) evaluate(v *vmRT, n *nodeRT, base float64) move {
	undo := st.apply(v, n)
	sp := st.spread()
	warns := st.cpuWarnings(n)
	hit := 0
	for _, g := range v.in.Groups {
		hit += n.groups[g] - 1 // other VMs of the same group already there
	}
	undo()
	// every warning the destination has after the move counts, also the ones it had already: sending more
	// load to a node that is already above the CPU limit must lose against an equivalent calm destination.
	return move{v: v, to: n, gain: base - sp, groupHit: hit, warns: warns}
}

// better orders equivalent moves: destination without CPU warnings, fewer GB, fewer same-group VMs, then keys.
func better(a, b move) bool {
	if (len(a.warns) == 0) != (len(b.warns) == 0) {
		return len(a.warns) == 0
	}
	if a.v.in.Mem != b.v.in.Mem {
		return a.v.in.Mem < b.v.in.Mem
	}
	if a.groupHit != b.groupHit {
		return a.groupHit < b.groupHit
	}
	if ka, kb := a.v.in.Key(), b.v.in.Key(); ka != kb {
		return ka < kb
	}
	return a.to.in.Name < b.to.in.Name
}

// pick returns the best move among cands: the highest gain, then the tie-breaks among the equivalent ones.
func pick(cands []move) (move, bool) {
	if len(cands) == 0 {
		return move{}, false
	}
	best := cands[0].gain
	for _, c := range cands {
		if c.gain > best {
			best = c.gain
		}
	}
	floor := best * equivalentShare
	if best <= 0 {
		floor = best
	}
	var out move
	found := false
	for _, c := range cands {
		if c.gain < floor {
			continue
		}
		if !found || better(c, out) {
			out, found = c, true
		}
	}
	return out, found
}

func (st *state) record(plan *Plan, m move, why string) {
	from := m.v.at
	st.apply(m.v, m.to)
	m.v.moved = true
	plan.Steps = append(plan.Steps, Step{N: len(plan.Steps) + 1, Key: m.v.in.Key(), Kind: m.v.in.Kind, VMID: m.v.in.VMID,
		Name: m.v.in.Name, From: from, To: m.to.in.Name, Mem: m.v.in.Mem, Running: m.v.in.Running, Why: why,
		Cold: m.v.cold, CPUChecked: m.v.needsSameCPU(),
		Warnings: m.warns, SpreadAfter: round2(st.spread())})
}

// evacuate moves every VM off the nodes to empty, largest first (hardest to place), each to the destination
// that leaves the most even cluster. VMs that cannot move or fit anywhere are reported, never forced.
func (st *state) evacuate(plan *Plan) {
	var list []*vmRT
	for _, v := range st.vms {
		if st.nodes[v.at].evac {
			list = append(list, v)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].in.Mem > list[j].in.Mem })
	for _, v := range list {
		if len(v.blocked) > 0 {
			reason := "blocked"
			if v.pinned {
				reason = "pinned"
			}
			plan.Unplaced = append(plan.Unplaced, Unplaced{Key: v.in.Key(), VMID: v.in.VMID, Name: v.in.Name, Node: v.at, Mem: v.in.Mem, Reason: reason})
			continue
		}
		base := st.spread()
		var cands []move
		var bestFree int64
		for _, name := range st.names {
			n := st.nodes[name]
			if st.dest(n) && n.in.Name != v.at {
				if free := n.assign - n.load; free > bestFree {
					bestFree = free
				}
			}
			if st.fits(v, n) {
				cands = append(cands, st.evaluate(v, n, base))
			}
		}
		m, ok := pick(cands)
		if !ok {
			plan.Unplaced = append(plan.Unplaced, Unplaced{Key: v.in.Key(), VMID: v.in.VMID, Name: v.in.Name, Node: v.at, Mem: v.in.Mem, Reason: "no_room", BestFree: bestFree})
			continue
		}
		st.record(plan, m, "evacuate")
	}
}

// balance adds up to MaxSteps moves, each the best single move, while it improves the spread enough.
func (st *state) balance(plan *Plan) string {
	if st.countDest() < 2 {
		return "no_nodes"
	}
	for i := 0; i < st.p.MaxSteps; i++ {
		base := st.spread()
		var cands []move
		for _, v := range st.vms {
			if len(v.blocked) > 0 || !v.counted || st.nodes[v.at].evac {
				continue
			}
			for _, name := range st.names {
				n := st.nodes[name]
				if st.fits(v, n) {
					if c := st.evaluate(v, n, base); c.gain > 0 {
						cands = append(cands, c)
					}
				}
			}
		}
		m, ok := pick(cands)
		if !ok || m.gain < st.p.MinGainPts {
			if i == 0 && len(plan.Steps) == 0 && base > st.p.MinGainPts {
				return "no_move"
			}
			return "balanced"
		}
		why := "balance"
		if from := st.nodes[m.v.at]; from.load > from.assign {
			why = "over_limit"
		}
		st.record(plan, m, why)
	}
	return "max_steps"
}

func (st *state) countDest() int {
	c := 0
	for _, n := range st.nodes {
		if st.dest(n) {
			c++
		}
	}
	return c
}

// ── output ──────────────────────────────────────────────────────────────────

type snap struct {
	load  int64
	vcpu  int
	cpu   float64
	vms   int
	warns []string
}

func (st *state) snapshot() map[string]snap {
	out := map[string]snap{}
	for name, n := range st.nodes {
		out[name] = snap{load: n.load, vcpu: n.vcpu, cpu: n.cpu, vms: n.vms, warns: st.cpuWarnings(n)}
	}
	return out
}

func (st *state) nodeStates(before map[string]snap) []NodeState {
	out := make([]NodeState, 0, len(st.names))
	for _, name := range st.names {
		n, b := st.nodes[name], before[name]
		out = append(out, NodeState{Name: name, Online: n.in.Online, Evacuated: n.evac, MemTotal: n.in.MemTotal,
			Reserve: n.reserve, Assignable: n.assign, OSDs: n.in.OSDs, Cores: n.in.Cores,
			LoadBefore: b.load, LoadAfter: n.load, PctBefore: round2(ratio(b.load, n.assign) * 100), PctAfter: round2(ratio(n.load, n.assign) * 100),
			VCPUBefore: b.vcpu, VCPUAfter: n.vcpu, CPUBefore: round2(b.cpu), CPUAfter: round2(n.cpu),
			VMsBefore: b.vms, VMsAfter: n.vms, OverBefore: b.load > n.assign, OverAfter: n.load > n.assign,
			WarningsBefore: b.warns, Warnings: st.cpuWarnings(n)})
	}
	return out
}

func (st *state) blockedList() []Blocked {
	out := []Blocked{}
	for _, v := range st.vms {
		if v.pinned {
			continue // outside the "only" filter: not a reason worth listing
		}
		reasons := v.blocked
		if len(reasons) == 0 && !v.counted {
			reasons = []string{BlockNotCounted}
		}
		if len(reasons) == 0 {
			continue
		}
		out = append(out, Blocked{Key: v.in.Key(), Kind: v.in.Kind, VMID: v.in.VMID, Name: v.in.Name, Node: v.in.Node, Mem: v.in.Mem, Reasons: reasons})
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// round2 rounds for the output; -1 (unknown) stays -1.
func round2(x float64) float64 {
	if x < 0 {
		return -1
	}
	return math.Round(x*100) / 100
}
