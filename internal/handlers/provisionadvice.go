package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxmox-manager/internal/balance"
	"proxmox-manager/internal/config"
)

// GET /api/provision/advice: where a new VM would fit best (ROADMAP 3b, D58-D59). READ ONLY: it writes nothing and
// reserves nothing. It uses the balance model (assigned RAM over physical - reserve, spread of the percentages,
// soft CPU warnings) with the default reserves of the balance page (8 GB + 4 GB per OSD).
//
//   memory_mb, cores   the VM (required)
//   storage            storage of the template's disk: a node must have it available (and, if it is not shared,
//                      the template must be on that very node: template_node)
//   gpu=1              the VM wants a card: only nodes with a free card that has a mapping are eligible, and the
//                      response lists every card (free, in use with the VM using it, no mapping) grouped by the page
//   group              group name of the new VM (soft preference to spread the group)

const (
	adviceMemMinMB = 128
	adviceMemMaxMB = 4 * 1024 * 1024 // 4 TB
	adviceMaxCores = 1024
	adviceCacheTTL = 10 * time.Second
)

// codes added by this endpoint, next to the balance.Place* ones
const (
	adviceStorageUnavailable = "storage_unavailable" // the storage is not available on the node
	adviceStorageNotShared   = "storage_not_shared"  // not shared and the template is on another node
	adviceStorageNoImages    = "storage_no_images"   // the storage cannot hold VM disks
	adviceNoFreeGPU          = "no_free_gpu"         // gpu requested, no free card with a mapping on the node
	adviceCardBusy           = "card_busy"           // card: in use (see uses)
	adviceCardNoMapping      = "no_mapping"          // card: free but no resource mapping points to it
	adviceCardNodeNotFit     = "node_not_eligible"   // card: its node cannot take the VM (reasons are on the node)
	adviceCardReserved       = "card_reserved"       // card: free, but reserved by a creation in progress (soft lock)
	// the extra disk (D64-D67): its own storage, which may differ from the template's
	adviceDisk2Unavailable = "disk2_storage_unavailable" // the storage of the extra disk is not available on the node
	adviceDisk2NoImages    = "disk2_storage_no_images"   // ... or cannot hold VM disks
	adviceDisk2NoSpace     = "disk2_no_space"            // ... or has less free space than the disk (Missing = bytes)
)

var storageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type adviceQuery struct {
	MemMB        int64
	Cores        int
	GPU          bool
	Storages     []string // storages of the template's volumes: all must be reachable from the node
	TemplateNode string
	Group        string
	Disk2GB      int    // extra disk size in GB (0 = none)
	Disk2Storage string // its storage; "" = the first of Storages (the template's)
}

func (q adviceQuery) key() string {
	return fmt.Sprintf("%d|%d|%t|%s|%s|%s", q.MemMB, q.Cores, q.GPU, strings.Join(q.Storages, ","), q.TemplateNode, q.Group) + fmt.Sprintf("|%d|%s", q.Disk2GB, q.Disk2Storage)
}

func parseAdviceQuery(q func(string) string) (adviceQuery, error) {
	var out adviceQuery
	mem, err := strconv.ParseInt(q("memory_mb"), 10, 64)
	if err != nil || mem < adviceMemMinMB || mem > adviceMemMaxMB {
		return out, errors.New("memory_mb non valido (128 - 4194304)")
	}
	cores, err := strconv.Atoi(q("cores"))
	if err != nil || cores < 1 || cores > adviceMaxCores {
		return out, errors.New("cores non valido (1 - 1024)")
	}
	out.MemMB, out.Cores = mem, cores
	if out.GPU, err = boolParam(q("gpu"), false); err != nil {
		return out, errors.New("gpu non valido")
	}
	if s := q("storage"); s != "" { // one storage, or several separated by commas (at most 8)
		parts := strings.Split(s, ",")
		if len(parts) > 8 {
			return out, errors.New("storage non valido")
		}
		seen := map[string]bool{}
		for _, x := range parts {
			if !storageNamePattern.MatchString(x) {
				return out, errors.New("storage non valido")
			}
			if !seen[x] {
				seen[x] = true
				out.Storages = append(out.Storages, x)
			}
		}
		sort.Strings(out.Storages)
	}
	if s := q("template_node"); s != "" {
		if !nodeNamePattern.MatchString(s) {
			return out, errors.New("template_node non valido")
		}
		out.TemplateNode = s
	}
	if s := q("disk2_gb"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 16384 {
			return out, errors.New("disk2_gb non valido (1 - 16384)")
		}
		out.Disk2GB = n
	}
	if s := q("disk2_storage"); s != "" {
		if !storageNamePattern.MatchString(s) {
			return out, errors.New("disk2_storage non valido")
		}
		out.Disk2Storage = s
	}
	if s := q("group"); s != "" {
		if !groupPattern.MatchString(s) {
			return out, errors.New("group non valido")
		}
		out.Group = s
	}
	return out, nil
}

// adviceCard is one video card as the form shows it.
type adviceCard struct {
	Node        string      `json:"node"`
	Addr        string      `json:"addr"`
	Name        string      `json:"name"`
	MemoryGB    int         `json:"memory_gb"` // 0 = unknown
	Status      string      `json:"status"`    // free | assigned | conflict
	Uses        []adviceUse `json:"uses"`
	Mappings    []string    `json:"mappings"`
	Reserved    bool        `json:"reserved"` // reserved by a creation in progress ("in creazione")
	Selectable  bool        `json:"selectable"`
	Recommended bool        `json:"recommended"`
	Reasons     []string    `json:"reasons"`
}

type adviceUse struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type adviceMeta struct {
	Cluster     string   `json:"cluster"`
	GeneratedAt int64    `json:"generated_at"`
	CephKnown   bool     `json:"ceph_known"`
	CPUKnown    bool     `json:"cpu_known"`
	Partial     []string `json:"partial"`
}

// adviceStorageOpt is a storage the form may offer for the extra disk on a node.
type adviceStorageOpt struct {
	ID     string `json:"id"`
	Shared bool   `json:"shared"`
	Free   int64  `json:"free"`  // bytes; -1 = not reported
	Total  int64  `json:"total"` // bytes
}

type adviceResult struct {
	balance.PlaceResult
	Cards    []adviceCard                  `json:"cards"`
	Storages map[string][]adviceStorageOpt `json:"storages"` // node -> active storages that accept VM disks
	Meta     adviceMeta                    `json:"meta"`
}

type adviceStorage struct {
	Shared bool
	Images bool
	Online bool
	Free   int64 // maxdisk - disk in bytes; -1 = not reported by Proxmox
	Total  int64
}

// reserved: node|addr of the cards reserved by creations in progress (they are shown, not selectable).
func buildAdvice(c balanceAPI, inv *GPUInventory, label string, q adviceQuery, reserved map[string]bool, now time.Time) (adviceResult, error) {
	if q.GPU && inv == nil {
		return adviceResult{}, errors.New("inventario delle schede non disponibile")
	}
	raw, err := c.GetClusterResources("")
	if err != nil {
		return adviceResult{}, err
	}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return adviceResult{}, errors.New("risorse del cluster non valide")
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
	storages := map[string]map[string]adviceStorage{} // storage id -> node -> state
	var vms []balance.VM
	for _, r := range list {
		typ, _ := r["type"].(string)
		node, _ := r["node"].(string)
		status, _ := r["status"].(string)
		if !nodeNamePattern.MatchString(node) {
			continue
		}
		switch typ {
		case "node":
			nodes = append(nodes, nodeRes{node, status == "online", intField(r, "maxmem"), intField(r, "maxcpu")})
		case "storage":
			if sn, _ := r["storage"].(string); storageNamePattern.MatchString(sn) {
				content, _ := r["content"].(string)
				if storages[sn] == nil {
					storages[sn] = map[string]adviceStorage{}
				}
				free := int64(-1)
				if total := intField(r, "maxdisk"); total > 0 {
					free = total - intField(r, "disk")
					if free < 0 {
						free = 0
					}
				}
				storages[sn][node] = adviceStorage{Shared: parseBoolish(r["shared"]), Images: containsStr(strings.Split(content, ","), "images"), Online: status == "available", Free: free, Total: intField(r, "maxdisk")}
			}
		case "qemu", "lxc":
			vmid := int(intField(r, "vmid"))
			if isTemplateRes(r) || vmid <= 0 {
				continue
			}
			name, _ := r["name"].(string)
			ts, _ := r["tags"].(string)
			vms = append(vms, balance.VM{Node: node, Kind: typ, VMID: vmid, Name: clampText(name, 64), Mem: intField(r, "maxmem"),
				VCPU: int(intField(r, "maxcpu")), Running: status == "running", Groups: resolver.resolve(name, ts).Groups, CPUCores: -1})
		}
	}
	if len(nodes) == 0 {
		return adviceResult{}, errors.New("nessun nodo nel cluster")
	}
	if len(vms) > balanceMaxGuests*4 {
		return adviceResult{}, errors.New(errTooManyBalance)
	}
	sort.Slice(nodes, func(i, j int) bool { return naturalLess(nodes[i].name, nodes[j].name) })
	meta := adviceMeta{Cluster: label, GeneratedAt: now.Unix(), Partial: []string{}}
	osds, cephKnown := osdCounts(c)
	meta.CephKnown = cephKnown
	if !cephKnown {
		meta.Partial = append(meta.Partial, "ceph")
	}
	cpu := make([]float64, len(nodes))
	jobs := make([]func(), 0, len(nodes))
	for i := range nodes {
		i := i
		jobs = append(jobs, func() { cpu[i] = nodeCPUP95(c, nodes[i].name) })
	}
	runLimited(jobs)
	in := make([]balance.Node, 0, len(nodes))
	for i, n := range nodes {
		if cpu[i] >= 0 {
			meta.CPUKnown = true
		}
		in = append(in, balance.Node{Name: n.name, Online: n.online, MemTotal: n.mem, Cores: int(n.cores), OSDs: osds[n.name], CPUP95: cpu[i], Reserve: -1})
	}

	// reasons known here, per node: storage, and a free card with a mapping when a GPU is wanted
	extra := map[string][]balance.PlaceReason{}
	d2Storage := q.Disk2Storage
	if d2Storage == "" && len(q.Storages) > 0 {
		d2Storage = q.Storages[0]
	}
	for _, n := range nodes {
		seen := map[string]bool{}
		for _, sid := range q.Storages {
			st, ok := storages[sid][n.name]
			code := ""
			switch {
			case !ok || !st.Online:
				code = adviceStorageUnavailable
			case !st.Images && sid == q.Storages[0] && len(q.Storages) == 1:
				code = adviceStorageNoImages
			case !st.Shared && n.name != q.TemplateNode:
				code = adviceStorageNotShared
			}
			if code != "" && !seen[code] {
				seen[code] = true
				extra[n.name] = append(extra[n.name], balance.PlaceReason{Code: code})
			}
		}
		// the extra disk is a NEW volume: its storage only has to exist and be active on this node (a node local
		// storage is fine), accept VM disks and have the space; space Proxmox does not report is not checked
		if q.Disk2GB > 0 && d2Storage != "" {
			st, ok := storages[d2Storage][n.name]
			need := int64(q.Disk2GB) * balance.GiB
			switch {
			case !ok || !st.Online:
				extra[n.name] = append(extra[n.name], balance.PlaceReason{Code: adviceDisk2Unavailable})
			case !st.Images:
				extra[n.name] = append(extra[n.name], balance.PlaceReason{Code: adviceDisk2NoImages})
			case st.Free >= 0 && st.Free < need:
				extra[n.name] = append(extra[n.name], balance.PlaceReason{Code: adviceDisk2NoSpace, Missing: need - st.Free})
			}
		}
	}
	var cards []adviceCard
	if q.GPU {
		cards = adviceCards(inv, reserved)
		freeOn := map[string]bool{}
		for _, cd := range cards {
			if cd.Selectable {
				freeOn[cd.Node] = true
			}
		}
		for _, n := range nodes {
			if !freeOn[n.name] {
				extra[n.name] = append(extra[n.name], balance.PlaceReason{Code: adviceNoFreeGPU})
			}
		}
		if len(inv.Partial) > 0 {
			meta.Partial = append(meta.Partial, "gpu")
		}
	}
	var groups []string
	if q.Group != "" {
		groups = []string{q.Group}
	}
	place, err := balance.Place(in, vms, balance.PlaceRequest{Mem: q.MemMB << 20, VCPU: q.Cores, Groups: groups, Extra: extra}, balance.DefaultParams())
	if err != nil {
		return adviceResult{}, err
	}
	opts := map[string][]adviceStorageOpt{}
	for _, n := range nodes {
		list := []adviceStorageOpt{}
		for sid, per := range storages {
			if st, ok := per[n.name]; ok && st.Online && st.Images {
				list = append(list, adviceStorageOpt{ID: sid, Shared: st.Shared, Free: st.Free, Total: st.Total})
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		opts[n.name] = list
	}
	res := adviceResult{PlaceResult: place, Cards: []adviceCard{}, Storages: opts, Meta: meta}
	sort.Strings(res.Meta.Partial)
	if q.GPU {
		// a card is usable only if its node can take the VM; the recommended one sits on the best ranked node
		eligible := map[string]bool{}
		for _, n := range place.Nodes {
			eligible[n.Name] = n.Eligible
		}
		for i := range cards {
			if cards[i].Selectable && !eligible[cards[i].Node] {
				cards[i].Selectable = false
				cards[i].Reasons = append(cards[i].Reasons, adviceCardNodeNotFit)
			}
		}
		for _, n := range place.Nodes {
			done := false
			for i := range cards {
				if cards[i].Node == n.Name && cards[i].Selectable {
					cards[i].Recommended = true
					done = true
					break
				}
			}
			if done {
				break
			}
		}
		sort.SliceStable(cards, func(i, j int) bool {
			a, b := cards[i], cards[j]
			if a.MemoryGB != b.MemoryGB {
				return a.MemoryGB > b.MemoryGB // known sizes first, biggest first; unknown (0) last
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			if a.Node != b.Node {
				return naturalLess(a.Node, b.Node)
			}
			return a.Addr < b.Addr
		})
		res.Cards = cards
	}
	return res, nil
}

// adviceCards flattens the GPU inventory. A card is selectable when it is free and a mapping points to it:
// the app only assigns through mappings (decision of 2026-11-02).
func adviceCards(inv *GPUInventory, reserved map[string]bool) []adviceCard {
	out := []adviceCard{}
	for _, n := range inv.Nodes {
		if !n.Online {
			continue
		}
		for _, cd := range n.Cards {
			shown := cd.Name
			if cd.DisplayName != "" {
				shown = cd.DisplayName
			}
			c := adviceCard{Node: n.Node, Addr: cd.Addr, Name: clampText(shown, 120), MemoryGB: cd.MemoryGB, Status: cd.Status,
				Uses: []adviceUse{}, Mappings: append([]string{}, cd.Mappings...), Reasons: []string{}}
			for _, u := range cd.Uses {
				c.Uses = append(c.Uses, adviceUse{VMID: u.VMID, Name: clampText(u.Name, 64), Status: u.Status})
			}
			switch {
			case cd.Status != "free":
				c.Reasons = append(c.Reasons, adviceCardBusy)
			case len(cd.Mappings) == 0:
				c.Reasons = append(c.Reasons, adviceCardNoMapping)
			case reserved[n.Node+"|"+cd.Addr]:
				c.Reserved = true
				c.Reasons = append(c.Reasons, adviceCardReserved)
			default:
				c.Selectable = true
			}
			out = append(out, c)
		}
	}
	return out
}

// GetProvisionAdvice: GET /api/provision/advice?memory_mb=&cores=&gpu=&storage=&template_node=&group=
func (h *Handler) GetProvisionAdvice(w http.ResponseWriter, r *http.Request) {
	q, err := parseAdviceQuery(r.URL.Query().Get)
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
		w.Header().Set("Retry-After", "5")
		writeError(w, errors.New("un altro calcolo e' in corso su questo cluster: riprova tra qualche secondo"), http.StatusTooManyRequests)
		return
	}
	defer g.Unlock()
	v, err := cephCached("advice|"+strconv.Itoa(idx)+"|"+q.key()+"|"+reservedKey(idx)+"|"+gpuModelsKey(), adviceCacheTTL, func() (interface{}, error) {
		var inv *GPUInventory
		if q.GPU {
			gv, gerr := cephCached("gpus|"+strconv.Itoa(idx), gpuCacheTTL, func() (interface{}, error) {
				return fetchGPUInventory(client, time.Now())
			})
			if gerr != nil {
				log.Printf("advice gpus (cluster %d): %v", idx, gerr)
				return nil, errors.New("inventario delle schede non disponibile")
			}
			if x, ok := gv.(GPUInventory); ok {
				x = withGPUModels(x, config.GPUModels())
				inv = &x
			}
		}
		return buildAdvice(client, inv, cl.Label, q, reservedCards(idx), time.Now())
	})
	if err != nil {
		log.Printf("advice (cluster %d): %v", idx, err)
		msg := "consiglio non disponibile"
		if err.Error() == errTooManyBalance || strings.HasPrefix(err.Error(), "inventario") {
			msg = err.Error()
		}
		writeError(w, errors.New(msg), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
}

// reservedKey: the reserved cards of a cluster, sorted, for the cache key (a new reservation is never hidden).
func reservedKey(cluster int) string {
	r := reservedCards(cluster)
	out := make([]string, 0, len(r))
	for k := range r {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
