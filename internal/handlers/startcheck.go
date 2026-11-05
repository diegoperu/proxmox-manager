package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"proxmox-manager/internal/balance"
)

// Before a stopped VM or container is started, check that its node can hold it (ROADMAP 12). Same model as the
// balance page and the advice for new VMs: the RAM assigned to the RUNNING guests of the node plus the RAM of the
// guest to start must fit the assignable RAM (physical - 8 GB - 4 GB per Ceph OSD); stopped guests use no RAM and
// do not count. A guest that does not fit is refused (409 not_enough_ram) with the GB that are missing.
//
// The check is a safeguard, not a condition for being able to start: when the cluster cannot be read the start goes on
// (Proxmox decides) and the reason is logged.

type startCheckAPI interface {
	GetClusterResources(resType string) (json.RawMessage, error)
	GetNodes() (json.RawMessage, error)
	GetCephOSD(node string) (json.RawMessage, error)
}

// startRefusal says why a guest cannot be started on its node.
type startRefusal struct {
	Node    string
	VMID    int
	Name    string
	Mem     int64
	Reasons []balance.PlaceReason
	// numbers of the node, for the message
	Assignable int64
	Load       int64
}

func gbCeil(b int64) int64 { return (b + balance.GiB - 1) / balance.GiB }

func (r *startRefusal) Message() string {
	for _, re := range r.Reasons {
		switch re.Code {
		case balance.PlaceRAM:
			return fmt.Sprintf("RAM insufficiente su %s: la VM %s (%d) richiede %d GB e ne mancano %d (assegnabile %d GB, gia' assegnati alle VM accese %d GB)",
				r.Node, r.Name, r.VMID, gbCeil(r.Mem), gbCeil(re.Missing), gbCeil(r.Assignable), gbCeil(r.Load))
		case balance.PlaceVCPUThreads:
			return fmt.Sprintf("il nodo %s non ha abbastanza thread per la VM %s (%d): ne servono %d, ne ha %d", r.Node, r.Name, r.VMID, re.Need, re.Have)
		case balance.PlaceOffline:
			return fmt.Sprintf("il nodo %s e' fuori linea", r.Node)
		}
	}
	return "il nodo non puo' avviare la VM"
}

func (r *startRefusal) MissingBytes() int64 {
	for _, re := range r.Reasons {
		if re.Code == balance.PlaceRAM {
			return re.Missing
		}
	}
	return 0
}

// ── guests started a moment ago that Proxmox still shows as stopped ──

const startReserveTTL = 90 * time.Second

var startedRecently = struct {
	sync.Mutex
	m map[string]time.Time // cluster|node|kind|vmid -> when
}{m: map[string]time.Time{}}

func startKey(cluster int, node, kind string, vmid int) string {
	return fmt.Sprintf("%d|%s|%s|%d", cluster, node, kind, vmid)
}

func markStarted(cluster int, node, kind string, vmid int, now time.Time) {
	startedRecently.Lock()
	defer startedRecently.Unlock()
	for k, t := range startedRecently.m { // forget the old ones
		if now.Sub(t) > startReserveTTL {
			delete(startedRecently.m, k)
		}
	}
	if len(startedRecently.m) < 4096 {
		startedRecently.m[startKey(cluster, node, kind, vmid)] = now
	}
}

func recentlyStarted(cluster int, node, kind string, vmid int, now time.Time) bool {
	startedRecently.Lock()
	defer startedRecently.Unlock()
	t, ok := startedRecently.m[startKey(cluster, node, kind, vmid)]
	return ok && now.Sub(t) <= startReserveTTL
}

// one lock per cluster+node (striped, fixed memory): check and start of two guests of a node never interleave
var startLocks [64]sync.Mutex

func startLock(cluster int, node string) *sync.Mutex {
	h := uint32(cluster) * 2654435761
	for i := 0; i < len(node); i++ {
		h = h*31 + uint32(node[i])
	}
	return &startLocks[h%uint32(len(startLocks))]
}

// checkStartRAM returns nil when the guest may start (or when there is nothing to check), else the refusal.
func checkStartRAM(c startCheckAPI, cluster int, node, kind string, vmid int, now time.Time) *startRefusal {
	raw, err := c.GetClusterResources("")
	var list []map[string]interface{}
	if err != nil || json.Unmarshal(raw, &list) != nil {
		log.Printf("start check %s/%s/%d: cluster not readable, the start goes on", node, kind, vmid)
		return nil
	}
	var target map[string]interface{}
	type nodeRes struct {
		name   string
		online bool
		mem    int64
		cores  int64
	}
	var nodes []nodeRes
	for _, r := range list {
		typ, _ := r["type"].(string)
		n, _ := r["node"].(string)
		switch {
		case typ == "node" && nodeNamePattern.MatchString(n):
			st, _ := r["status"].(string)
			nodes = append(nodes, nodeRes{n, st == "online", intField(r, "maxmem"), intField(r, "maxcpu")})
		case typ == kind && n == node && int(intField(r, "vmid")) == vmid:
			target = r
		}
	}
	if target == nil || isTemplateRes(target) {
		return nil // unknown or a template: Proxmox answers
	}
	if status, _ := target["status"].(string); status == "running" {
		return nil
	}
	mem, cores := intField(target, "maxmem"), int(intField(target, "maxcpu"))
	if mem <= 0 || cores <= 0 {
		return nil
	}
	sort.Slice(nodes, func(i, j int) bool { return naturalLess(nodes[i].name, nodes[j].name) })
	osds, _ := osdCounts(c)
	in := make([]balance.Node, 0, len(nodes))
	for _, n := range nodes {
		in = append(in, balance.Node{Name: n.name, Online: n.online, MemTotal: n.mem, Cores: int(n.cores), OSDs: osds[n.name], CPUP95: -1, Reserve: -1})
	}
	// the guests that use RAM now: running ones, and the ones we started a moment ago that Proxmox still shows as stopped
	var vms []balance.VM
	for _, r := range list {
		typ, _ := r["type"].(string)
		n, _ := r["node"].(string)
		id := int(intField(r, "vmid"))
		if (typ != "qemu" && typ != "lxc") || id <= 0 || isTemplateRes(r) || (typ == kind && n == node && id == vmid) {
			continue
		}
		status, _ := r["status"].(string)
		if status != "running" && !recentlyStarted(cluster, n, typ, id, now) {
			continue
		}
		vms = append(vms, balance.VM{Node: n, Kind: typ, VMID: id, Mem: intField(r, "maxmem"), VCPU: int(intField(r, "maxcpu")), Running: true, CPUCores: -1})
	}
	res, err := balance.Place(in, vms, balance.PlaceRequest{Mem: mem, VCPU: cores}, balance.DefaultParams())
	if err != nil {
		log.Printf("start check %s/%s/%d: %v, the start goes on", node, kind, vmid, err)
		return nil
	}
	for _, pn := range res.Nodes {
		if pn.Name != node || pn.Eligible {
			continue
		}
		name, _ := target["name"].(string)
		return &startRefusal{Node: node, VMID: vmid, Name: clampText(name, 64), Mem: mem, Reasons: pn.Reasons, Assignable: pn.Assignable, Load: pn.LoadBefore}
	}
	return nil
}

// startGuarded runs do (the start request) only if the node can hold the guest; check and start are serialised per node.
func startGuarded(c startCheckAPI, cluster int, node, kind string, vmid int, now time.Time, do func() (json.RawMessage, error)) (json.RawMessage, *startRefusal, error) {
	l := startLock(cluster, node)
	l.Lock()
	defer l.Unlock()
	if ref := checkStartRAM(c, cluster, node, kind, vmid, now); ref != nil {
		log.Printf("start refused: cluster=%d %s=%d on %s: %s", cluster, kind, vmid, node, ref.Message())
		return nil, ref, nil
	}
	data, err := do()
	if err == nil {
		markStarted(cluster, node, kind, vmid, now)
	}
	return data, nil, err
}
