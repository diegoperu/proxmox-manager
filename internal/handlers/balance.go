package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/balance"
	"proxmox-manager/internal/config"
)

// GET /api/balance: read-only plan of VM migrations that evens out the RAM load (ROADMAP 3, D43-D49).
// This file only collects facts from Proxmox and validates the knobs; the plan itself is internal/balance.
// Nothing is written: the page runs each step with the existing migrate action, after a new plan and a confirmation.

const errTooManyBalance = "troppe VM nel cluster per calcolare il piano"

const (
	balanceMaxGuests   = 600
	balanceFactsTTL    = 5 * time.Minute // static facts of a guest (config, precheck, CPU p95): the plan itself is never cached
	balanceFactsMax    = 2000
	balanceMaxReserves = 64
)

type balanceAPI interface {
	GetClusterResources(resType string) (json.RawMessage, error)
	GetNodeRRDcf(node, tf, cf string) (json.RawMessage, error)
	GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error)
	GetVMConfig(node string, vmid int) (json.RawMessage, error)
	GetContainerConfig(node string, vmid int) (json.RawMessage, error)
	GetMigratePrecheck(node string, vmid int) (json.RawMessage, error)
	GetHAResources() (json.RawMessage, error)
	GetNodes() (json.RawMessage, error)
	GetCephOSD(node string) (json.RawMessage, error)
	GetNodeStatus(node string) (json.RawMessage, error) // cpuinfo.flags: live migration of host-CPU VMs (D63)
}

var _ balanceAPI = (*api.Client)(nil)

// ── parameters ──────────────────────────────────────────────────────────────

type balanceQuery struct {
	P        balance.Params
	Reserves map[string]int64 // node -> bytes (explicit reserve, replaces host + per OSD)
	Fresh    bool             // ignore the cached static facts
}

// balanceMinExecReserve: a plan computed with a smaller host reserve (0 is allowed: "what if") is a simulation only.
// Executing it could fill a node up to its physical RAM, so POST /api/balance/step refuses it (decision 2026-11-04).
const balanceMinExecReserve = 8 * balance.GiB

// execReserveOK: the host reserve and every explicit per-node reserve must be at least balanceMinExecReserve.
// The per-OSD part only adds to the host reserve, so it cannot bring a node under the minimum.
func (q balanceQuery) execReserveOK() bool {
	if q.P.HostReserve < balanceMinExecReserve {
		return false
	}
	for _, b := range q.Reserves {
		if b < balanceMinExecReserve {
			return false
		}
	}
	return true
}

func (q balanceQuery) key() string {
	rs := make([]string, 0, len(q.Reserves))
	for n, b := range q.Reserves {
		rs = append(rs, n+":"+strconv.FormatInt(b, 10))
	}
	sort.Strings(rs)
	ev := append([]string(nil), q.P.Evacuate...)
	sort.Strings(ev)
	ids := func(l []int) string {
		c := append([]int(nil), l...)
		sort.Ints(c)
		out := make([]string, len(c))
		for i, v := range c {
			out[i] = strconv.Itoa(v)
		}
		return strings.Join(out, ",")
	}
	return fmt.Sprintf("%d|%d|%t|%t|%d|%g|%g|%g|%s|%s|%s|%s", q.P.HostReserve, q.P.PerOSD, q.P.CountStopped, q.P.Cold, q.P.MaxSteps,
		q.P.VCPUPerCore, q.P.CPUWarnPct, q.P.MinGainPts, strings.Join(ev, ","), strings.Join(rs, ","), ids(q.P.Only), ids(q.P.ColdVMs))
}

func boolParam(v string, def bool) (bool, error) {
	switch v {
	case "":
		return def, nil
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	}
	return def, errors.New("valore booleano non valido")
}

// parseBalanceQuery: every value is whitelisted; the balance package validates the ranges again.
func parseBalanceQuery(q func(string) string) (balanceQuery, error) {
	out := balanceQuery{P: balance.DefaultParams(), Reserves: map[string]int64{}}
	var err error
	gb := func(name string, cur int64, hi float64) (int64, error) {
		if q(name) == "" {
			return cur, nil
		}
		v, err := floatParam(q(name), 0, 0, hi)
		if err != nil {
			return cur, fmt.Errorf("%s non valido", name)
		}
		return int64(math.Round(v * float64(balance.GiB))), nil
	}
	if out.P.HostReserve, err = gb("host_reserve_gb", out.P.HostReserve, 512); err != nil {
		return out, err
	}
	if out.P.PerOSD, err = gb("per_osd_gb", out.P.PerOSD, 64); err != nil {
		return out, err
	}
	if out.P.CountStopped, err = boolParam(q("count_stopped"), out.P.CountStopped); err != nil {
		return out, errors.New("count_stopped non valido")
	}
	if out.P.Cold, err = boolParam(q("cold"), out.P.Cold); err != nil {
		return out, errors.New("cold non valido")
	}
	if out.Fresh, err = boolParam(q("fresh"), false); err != nil {
		return out, errors.New("fresh non valido")
	}
	if s := q("max_steps"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 50 {
			return out, errors.New("max_steps non valido (1-50)")
		}
		out.P.MaxSteps = n
	}
	if s := q("vcpu_per_core"); s != "" {
		if out.P.VCPUPerCore, err = floatParam(s, 0, 0.5, 64); err != nil {
			return out, errors.New("vcpu_per_core non valido")
		}
	}
	if s := q("cpu_warn"); s != "" {
		if out.P.CPUWarnPct, err = floatParam(s, 0, 1, 100); err != nil {
			return out, errors.New("cpu_warn non valido")
		}
	}
	if s := q("evacuate"); s != "" {
		seen := map[string]bool{}
		for _, n := range strings.Split(s, ",") {
			if !nodeNamePattern.MatchString(n) || len(seen) >= 32 {
				return out, errors.New("nodo da svuotare non valido")
			}
			if !seen[n] {
				seen[n] = true
				out.P.Evacuate = append(out.P.Evacuate, n)
			}
		}
	}
	// only=174,180 (D54) and cold_vms=174 (D50): VMIDs, at most 64 each
	if out.P.Only, err = vmidList(q("only")); err != nil {
		return out, errors.New("only non valido (VMID separati da virgola, massimo 64)")
	}
	if out.P.ColdVMs, err = vmidList(q("cold_vms")); err != nil {
		return out, errors.New("cold_vms non valido (VMID separati da virgola, massimo 64)")
	}
	// reserve=node:gb,node:gb (explicit reserve for a node)
	if s := q("reserve"); s != "" {
		parts := strings.Split(s, ",")
		if len(parts) > balanceMaxReserves {
			return out, errors.New("troppe riserve per nodo")
		}
		for _, part := range parts {
			i := strings.LastIndexByte(part, ':')
			if i <= 0 || !nodeNamePattern.MatchString(part[:i]) {
				return out, errors.New("riserva per nodo non valida (nodo:GB)")
			}
			v, e := floatParam(part[i+1:], 0, 0, 2048)
			if e != nil {
				return out, errors.New("riserva per nodo non valida (nodo:GB)")
			}
			out.Reserves[part[:i]] = int64(math.Round(v * float64(balance.GiB)))
		}
	}
	return out, out.P.Validate()
}

// vmidList parses "174,180": unique VMIDs, at most 64; "" = none.
func vmidList(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > 64 {
		return nil, errors.New("troppe VM")
	}
	seen := map[int]bool{}
	var out []int
	for _, p := range parts {
		if !vmidPattern.MatchString(p) {
			return nil, errors.New("VMID non valido")
		}
		n, _ := strconv.Atoi(p)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

var cpuFlagPattern = regexp.MustCompile(`^[a-z0-9_.]{1,40}$`)

// hostOnlyCPUFlags describe the host itself (virtualization extensions, SEV, the host's APIC mode, the inventory
// number), not instructions a guest runs: they are left out of the live-migration comparison. Chosen on the
// matrix of 2026-11-05 (VM 108 "moving-test", the 56 ordered pairs of the 8 CPU models of CALCOLO): with them
// left out, the rule "the destination has every flag of the source" never allows a pair that failed (task error
// or a guest that hung after a "successful" task) and allows every same-generation and older->newer pair that worked.
var hostOnlyCPUFlags = map[string]bool{
	"vmx": true, "svm": true, "svm_lock": true, "ept": true, "ept_ad": true, "vpid": true, "flexpriority": true,
	"tpr_shadow": true, "vnmi": true, "npt": true, "lbrv": true, "nrip_save": true, "tsc_scale": true,
	"vmcb_clean": true, "flushbyasid": true, "decodeassists": true, "pausefilter": true, "pfthreshold": true,
	"vgif": true, "v_vmsave_vmload": true, "v_spec_ctrl": true, "avic": true, "x2avic": true,
	"sev": true, "sev_es": true, "sev_snp": true, "sme": true, "x2apic": true, "intel_ppin": true, "amd_ppin": true,
}

// cpuFlags parses cpuinfo.flags ("fpu vme de pse ..."), without the host-only ones; invalid or oversized lists
// give nil (= unknown).
func cpuFlags(s string) map[string]bool {
	fields := strings.Fields(s)
	if len(fields) == 0 || len(fields) > 1000 {
		return nil
	}
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		if !cpuFlagPattern.MatchString(f) {
			return nil
		}
		if !hostOnlyCPUFlags[f] {
			out[f] = true
		}
	}
	return out
}

// hostCPUType: the CPU type of a QEMU config passes the physical CPU to the guest (host, max) or may be derived
// from it (custom-*): a live migration then needs a destination with every CPU flag of the source (D63). No "cpu" key = Proxmox's generic default.
func hostCPUType(cfg map[string]interface{}) bool {
	s, ok := cfg["cpu"].(string)
	if !ok || s == "" {
		return false
	}
	t := s
	for _, part := range strings.Split(s, ",") {
		if strings.HasPrefix(part, "cputype=") {
			t = strings.TrimPrefix(part, "cputype=")
			break
		}
		if !strings.Contains(part, "=") {
			t = part
			break
		}
	}
	return t == "host" || t == "max" || strings.HasPrefix(t, "custom-")
}

// ── facts of a guest (slow part: config, precheck, CPU p95), cached 5 minutes per node/kind/vmid ──

type guestFacts struct {
	Blockers []string
	Allowed  []string // nil = every online node (LXC: Proxmox has no precheck)
	CPUCores float64  // real CPU p95 over 4 weeks in cores; -1 unknown
	HostCPU  bool     // QEMU cpu type host/max/custom (D55)
}

var balanceFacts = struct {
	sync.Mutex
	m map[string]balanceFactEntry
}{m: map[string]balanceFactEntry{}}

type balanceFactEntry struct {
	at time.Time
	f  guestFacts
}

func factsGet(key string) (guestFacts, bool) {
	balanceFacts.Lock()
	defer balanceFacts.Unlock()
	e, ok := balanceFacts.m[key]
	if !ok || time.Since(e.at) > balanceFactsTTL {
		return guestFacts{}, false
	}
	return e.f, true
}

func factsPut(key string, f guestFacts) {
	balanceFacts.Lock()
	defer balanceFacts.Unlock()
	if len(balanceFacts.m) >= balanceFactsMax {
		for k, e := range balanceFacts.m { // drop expired first, then anything: the map is only a cache
			if time.Since(e.at) > balanceFactsTTL {
				delete(balanceFacts.m, k)
			}
		}
		for k := range balanceFacts.m {
			if len(balanceFacts.m) < balanceFactsMax {
				break
			}
			delete(balanceFacts.m, k)
		}
	}
	balanceFacts.m[key] = balanceFactEntry{at: time.Now(), f: f}
}

// configBlockers reads a guest config for what ties it to its node. Any hostpci is treated as a PCI card
// (the 22 cases of CALCOLO are GPUs); usb, serial passthrough and similar are local resources too.
func configBlockers(cfg map[string]interface{}, kind string, shared map[string]bool) []string {
	set := map[string]bool{}
	for k, v := range cfg {
		s, _ := v.(string)
		switch {
		case strings.HasPrefix(k, "hostpci"):
			set[balance.BlockGPU] = true
		case strings.HasPrefix(k, "usb"):
			set[balance.BlockLocalRes] = true
		case kind == "lxc" && (k == "rootfs" || strings.HasPrefix(k, "mp")):
			// "storage:volume,..." (or a bind mount path): an unknown or non shared storage keeps it on the node
			if i := strings.IndexByte(s, ':'); i > 0 && !strings.HasPrefix(s, "/") {
				if !shared[s[:i]] {
					set[balance.BlockLocalDisk] = true
				}
			} else if strings.HasPrefix(s, "/") {
				set[balance.BlockLocalRes] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

func asStringList(v interface{}) []string {
	l, _ := v.([]interface{})
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok && nodeNamePattern.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// loadGuestFacts: one config read, one precheck (QEMU only) and one RRD read per guest. A failed read never
// guesses: the guest is marked `unverified` and stays where it is.
func loadGuestFacts(c balanceAPI, g resGuestB, shared map[string]bool) guestFacts {
	f := guestFacts{CPUCores: -1}
	var raw json.RawMessage
	var err error
	if g.Kind == "qemu" {
		raw, err = c.GetVMConfig(g.Node, g.VMID)
	} else {
		raw, err = c.GetContainerConfig(g.Node, g.VMID)
	}
	var cfg map[string]interface{}
	if err != nil || json.Unmarshal(raw, &cfg) != nil {
		f.Blockers = []string{balance.BlockUnverified}
		return f
	}
	f.Blockers = configBlockers(cfg, g.Kind, shared)
	if g.Kind == "qemu" {
		f.HostCPU = hostCPUType(cfg)
		pr, err := c.GetMigratePrecheck(g.Node, g.VMID)
		var m map[string]interface{}
		if err != nil || json.Unmarshal(pr, &m) != nil {
			f.Blockers = append(f.Blockers, balance.BlockUnverified)
		} else {
			if l, ok := m["local_disks"].([]interface{}); ok && len(l) > 0 {
				f.Blockers = append(f.Blockers, balance.BlockLocalDisk)
			}
			if l, ok := m["local_resources"].([]interface{}); ok && len(l) > 0 {
				f.Blockers = append(f.Blockers, balance.BlockLocalRes)
			}
			if _, ok := m["allowed_nodes"]; ok {
				// Proxmox lists the OTHER nodes it allows: the current one stays allowed so a step done later
				// (the guest moved) is still valid from the planner's point of view.
				f.Allowed = append(asStringList(m["allowed_nodes"]), g.Node)
			}
		}
	}
	// real CPU p95 over 4 weeks (RRD "month", 30 min resolution: a true percentile)
	if raw, err := c.GetGuestRRDcf(g.Node, g.Kind, g.VMID, "month", "AVERAGE"); err == nil {
		if s, err := parseRRD(raw, "cpu"); err == nil {
			cpu := scaleNaN(orNaN(s.Cols["cpu"], len(s.T)), 1)
			if st := statOf(cpu, nil, medianStep(s.T), 1e18); st.P95 != nil {
				f.CPUCores = round2(*st.P95 * float64(g.Cores))
			}
		}
	}
	f.Blockers = dedupSorted(f.Blockers)
	return f
}

func dedupSorted(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ── inventory ───────────────────────────────────────────────────────────────

type resGuestB struct {
	Node, Kind, Name string
	VMID             int
	Running          bool
	Mem              int64
	Cores            int64
	Lock             bool
	Groups           []string
}

type balanceMeta struct {
	Cluster     string   `json:"cluster"`
	GeneratedAt int64    `json:"generated_at"`
	CephKnown   bool     `json:"ceph_known"` // false: OSD counts unknown, the Ceph reserve is not applied
	HAKnown     bool     `json:"ha_known"`
	CPUKnown    bool     `json:"cpu_known"`
	Unverified  int      `json:"unverified"` // guests whose facts could not be read (not moved)
	Partial     []string `json:"partial"`
	// ExecAllowed false: the reserves are under ExecMinReserveGB, the plan is a simulation and its steps are refused
	ExecAllowed      bool  `json:"exec_allowed"`
	ExecMinReserveGB int64 `json:"exec_min_reserve_gb"`
}

type balanceResult struct {
	balance.Plan
	Meta balanceMeta `json:"meta"`
}

func buildBalance(c balanceAPI, clusterLabel string, idx int, q balanceQuery, now time.Time) (balanceResult, error) {
	raw, err := c.GetClusterResources("")
	if err != nil {
		return balanceResult{}, err
	}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return balanceResult{}, errors.New("risorse del cluster non valide")
	}
	var tagStrs []string
	for _, r := range list {
		if typ, _ := r["type"].(string); (typ == "qemu" || typ == "lxc") && !isTemplateRes(r) {
			ts, _ := r["tags"].(string)
			tagStrs = append(tagStrs, ts)
		}
	}
	resolver := newGroupResolver(tagStrs)
	type nodeRes struct {
		name   string
		online bool
		mem    int64
		cores  int64
	}
	var nodes []nodeRes
	shared := map[string]bool{}
	var guests []resGuestB
	for _, r := range list {
		typ, _ := r["type"].(string)
		node, _ := r["node"].(string)
		status, _ := r["status"].(string)
		switch typ {
		case "node":
			if nodeNamePattern.MatchString(node) {
				nodes = append(nodes, nodeRes{node, status == "online", intField(r, "maxmem"), intField(r, "maxcpu")})
			}
		case "storage":
			if sn, _ := r["storage"].(string); sn != "" && parseBoolish(r["shared"]) {
				shared[sn] = true
			}
		case "qemu", "lxc":
			vmid := int(intField(r, "vmid"))
			if isTemplateRes(r) || vmid <= 0 || !nodeNamePattern.MatchString(node) {
				continue
			}
			name, _ := r["name"].(string)
			ts, _ := r["tags"].(string)
			lock, _ := r["lock"].(string)
			guests = append(guests, resGuestB{Node: node, Kind: typ, Name: clampText(name, 64), VMID: vmid, Running: status == "running",
				Mem: intField(r, "maxmem"), Cores: intField(r, "maxcpu"), Lock: lock != "", Groups: resolver.resolve(name, ts).Groups})
		}
	}
	if len(nodes) == 0 {
		return balanceResult{}, errors.New("nessun nodo nel cluster")
	}
	if len(guests) > balanceMaxGuests {
		return balanceResult{}, errors.New(errTooManyBalance)
	}
	sort.Slice(nodes, func(i, j int) bool { return naturalLess(nodes[i].name, nodes[j].name) })
	for _, e := range q.P.Evacuate {
		found := false
		for _, n := range nodes {
			found = found || n.name == e
		}
		if !found {
			return balanceResult{}, errors.New("nodo da svuotare sconosciuto")
		}
	}

	meta := balanceMeta{Cluster: clusterLabel, GeneratedAt: now.Unix(), Partial: []string{},
		ExecAllowed: q.execReserveOK(), ExecMinReserveGB: balanceMinExecReserve / balance.GiB}

	// HA: a guest under HA is placed by the HA manager, not by us
	haSet := map[string]bool{}
	if hraw, err := c.GetHAResources(); err != nil {
		meta.Partial = append(meta.Partial, "ha")
	} else {
		meta.HAKnown = true
		var l []map[string]interface{}
		if json.Unmarshal(hraw, &l) == nil {
			for _, h := range l {
				if sid, _ := h["sid"].(string); sid != "" {
					haSet[sid] = true
				}
			}
		} else {
			meta.HAKnown = false
			meta.Partial = append(meta.Partial, "ha")
		}
	}

	// Ceph OSDs per node (reserve), best effort
	osds := map[string]int{}
	if cands, err := cephNodeCandidates(c, nil); err == nil {
		for i, n := range cands {
			if i >= cephNodeAttempts {
				break
			}
			if oraw, err := c.GetCephOSD(n); err == nil {
				meta.CephKnown = true
				for _, o := range normalizeCephOSDs(oraw) {
					osds[o.Host]++
				}
				break
			}
		}
	}
	if !meta.CephKnown {
		meta.Partial = append(meta.Partial, "ceph")
	}

	// facts per guest (parallel, bounded) and CPU p95 per node
	facts := make([]guestFacts, len(guests))
	jobs := make([]func(), 0, len(guests))
	for i := range guests {
		i := i
		jobs = append(jobs, func() {
			g := guests[i]
			key := fmt.Sprintf("%d|%s|%s|%d", idx, g.Node, g.Kind, g.VMID)
			if !q.Fresh {
				if f, ok := factsGet(key); ok {
					facts[i] = f
					return
				}
			}
			f := loadGuestFacts(c, g, shared)
			if !containsStr(f.Blockers, balance.BlockUnverified) {
				factsPut(key, f)
			}
			facts[i] = f
		})
	}
	nodeFlags := make([]map[string]bool, len(nodes))
	for i := range nodes {
		i := i
		if !nodes[i].online {
			continue
		}
		jobs = append(jobs, func() {
			raw, err := c.GetNodeStatus(nodes[i].name)
			if err != nil {
				return
			}
			var st struct {
				CPUInfo struct {
					Flags string `json:"flags"`
				} `json:"cpuinfo"`
			}
			if json.Unmarshal(raw, &st) == nil {
				nodeFlags[i] = cpuFlags(st.CPUInfo.Flags)
			}
		})
	}
	nodeCPU := make([]float64, len(nodes))
	for i := range nodes {
		i := i
		nodeCPU[i] = -1
		jobs = append(jobs, func() {
			raw, err := c.GetNodeRRDcf(nodes[i].name, "month", "AVERAGE")
			if err != nil {
				return
			}
			if s, err := parseRRD(raw, "cpu"); err == nil {
				cpu := scaleNaN(orNaN(s.Cols["cpu"], len(s.T)), 100)
				if st := statOf(cpu, nil, medianStep(s.T), 1e18); st.P95 != nil {
					nodeCPU[i] = *st.P95
				}
			}
		})
	}
	runLimited(jobs)

	in := make([]balance.Node, 0, len(nodes))
	for i, n := range nodes {
		res := int64(-1)
		if b, ok := q.Reserves[n.name]; ok {
			res = b
		}
		if nodeCPU[i] >= 0 {
			meta.CPUKnown = true
		}
		if n.online && len(nodeFlags[i]) == 0 && !containsStr(meta.Partial, "cpu_flags") {
			meta.Partial = append(meta.Partial, "cpu_flags") // unknown flags: no live destination for host-CPU VMs there
		}
		in = append(in, balance.Node{Name: n.name, Online: n.online, MemTotal: n.mem, Cores: int(n.cores), OSDs: osds[n.name], CPUP95: nodeCPU[i], Reserve: res, CPUFlags: nodeFlags[i]})
	}
	vms := make([]balance.VM, 0, len(guests))
	for i, g := range guests {
		f := facts[i]
		bl := append([]string(nil), f.Blockers...)
		if g.Lock {
			bl = append(bl, balance.BlockLock)
		}
		sid := "vm:" + strconv.Itoa(g.VMID)
		if g.Kind == "lxc" {
			sid = "ct:" + strconv.Itoa(g.VMID)
		}
		if haSet[sid] {
			bl = append(bl, balance.BlockHA)
		}
		bl = dedupSorted(bl)
		if containsStr(bl, balance.BlockUnverified) {
			meta.Unverified++
		}
		vms = append(vms, balance.VM{Node: g.Node, Kind: g.Kind, VMID: g.VMID, Name: g.Name, Mem: g.Mem, VCPU: int(g.Cores), Running: g.Running,
			Groups: g.Groups, CPUCores: f.CPUCores, Allowed: f.Allowed, Blockers: bl, HostCPU: f.HostCPU})
	}
	if meta.Unverified > 0 {
		meta.Partial = append(meta.Partial, "precheck")
	}
	plan, err := balance.Compute(in, vms, q.P)
	if err != nil {
		return balanceResult{}, err
	}
	sort.Strings(meta.Partial)
	return balanceResult{Plan: plan, Meta: meta}, nil
}

// GetBalance: GET /api/balance?host_reserve_gb=&per_osd_gb=&count_stopped=&cold=&max_steps=&vcpu_per_core=&cpu_warn=&evacuate=a,b&reserve=node:gb&fresh=1
func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	q, err := parseBalanceQuery(r.URL.Query().Get)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	idx := clusterIdx(r)
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	g := reportGate(idx)
	if !g.TryLock() {
		w.Header().Set("Retry-After", "10")
		writeError(w, errors.New("un altro calcolo e' in corso su questo cluster: riprova tra qualche secondo"), http.StatusTooManyRequests)
		return
	}
	defer g.Unlock()
	res, err := buildBalance(client, cl.Label, idx, q, time.Now())
	if err != nil {
		if err.Error() == errTooManyBalance || strings.Contains(err.Error(), "sconosciuto") || strings.Contains(err.Error(), "non valid") {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		log.Printf("balance (cluster %d): %v", idx, err)
		writeError(w, errors.New("dati del bilanciamento non disponibili"), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, res)
}
