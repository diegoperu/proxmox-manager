package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
)

// GPU inventory (read only): per node, the NVIDIA video cards, who uses each one (direct hostpci or resource
// mapping) and what has to be fixed. The truth is Proxmox itself: VM configs + node PCI list + PCI mappings.

const (
	gpuCacheTTL      = 15 * time.Second
	gpuConfigWorkers = 8
	maxGPUVMs        = 3000
)

// MappingEntry is one `node=...,path=...` entry of a PCI resource mapping.
type MappingEntry struct {
	Node string
	Addr string // normalised bus:dev
}

// MappingSet: mapping id -> its entries (all nodes).
type MappingSet map[string][]MappingEntry

// ParsePCIMappingEntries reads `/cluster/mapping/pci` keeping every node.
func ParsePCIMappingEntries(raw json.RawMessage) MappingSet {
	out := MappingSet{}
	var list []struct {
		ID  string   `json:"id"`
		Map []string `json:"map"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return out
	}
	for _, e := range list {
		if !mappingPat.MatchString(e.ID) {
			continue
		}
		for _, entry := range e.Map {
			var n, path string
			for _, kv := range strings.Split(entry, ",") {
				if k, v, ok := strings.Cut(kv, "="); ok {
					switch strings.TrimSpace(k) {
					case "node":
						n = strings.TrimSpace(v)
					case "path":
						path = strings.TrimSpace(v)
					}
				}
			}
			addr := normalizePCIAddr(path)
			if n != "" && addr != "" && len(out[e.ID]) < 64 {
				out[e.ID] = append(out[e.ID], MappingEntry{Node: n, Addr: addr})
			}
		}
	}
	return out
}

// GPUVM is what the inventory needs to know about one QEMU VM.
type GPUVM struct {
	VMID    int
	Name    string
	Status  string
	Lock    string
	Machine string
	HostPCI map[string]string // "hostpci0" -> value
}

// GPUNodeInput is one node's raw material.
type GPUNodeInput struct {
	Node   string
	Online bool
	PCI    PCITable
	VMs    []GPUVM
}

type GPUUse struct {
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Lock     string `json:"lock,omitempty"`
	Slot     string `json:"slot"`
	Kind     string `json:"kind"` // mapping | direct
	Mapping  string `json:"mapping,omitempty"`
	Editable bool   `json:"editable"` // mapping-based and not part of a group mapping: the app may remove it
}

type GPUCard struct {
	Addr     string   `json:"addr"` // bus:dev, "0000:" prefix added
	Name     string   `json:"name"`
	Group    int      `json:"iommu_group"`
	Status   string   `json:"status"` // free | assigned | conflict
	Uses     []GPUUse `json:"uses"`
	Mappings []string `json:"mappings"` // mapping ids that point to this card on this node
}

type GPUGroupMapping struct {
	ID         string   `json:"id"`
	Cards      int      `json:"cards"`
	Uses       []GPUUse `json:"uses"`
	Overbooked bool     `json:"overbooked"`
}

type GPUNode struct {
	Node   string            `json:"node"`
	Online bool              `json:"online"`
	Cards  []GPUCard         `json:"cards"`
	Groups []GPUGroupMapping `json:"groups"`
}

// GPUProblem: kind = double (one card, several VMs) | overbooked (group mapping used by more VMs than cards)
// | device_missing (card not on the VM's node) | mapping_no_node (mapping has no entry on the VM's node).
type GPUProblem struct {
	Kind    string   `json:"kind"`
	Node    string   `json:"node"`
	Card    string   `json:"card,omitempty"`
	Addr    string   `json:"addr,omitempty"`
	Mapping string   `json:"mapping,omitempty"`
	Uses    []GPUUse `json:"uses"`
	// Fixable: at least one involved assignment is mapping-based, so the app can remove it. Direct ones
	// can only be fixed in Proxmox (decision D25).
	Fixable bool `json:"fixable"`
}

type GPUInventory struct {
	Nodes    []GPUNode    `json:"nodes"`
	Problems []GPUProblem `json:"problems"`
	Mappings []string     `json:"mappings"` // all mapping ids, for the UI
	Updated  int64        `json:"updated"`
	Partial  []string     `json:"partial"` // nodes whose data could not be read
}

func fullAddr(a string) string { return "0000:" + a }

// BuildGPUInventory is pure: same inputs, same answer. Only NVIDIA display cards are tracked; other hostpci
// devices (NICs, NVMe...) are ignored.
func BuildGPUInventory(nodes []GPUNodeInput, maps MappingSet, now time.Time) GPUInventory {
	inv := GPUInventory{Nodes: []GPUNode{}, Problems: []GPUProblem{}, Mappings: []string{}, Partial: []string{}, Updated: now.Unix()}
	for id := range maps {
		inv.Mappings = append(inv.Mappings, id)
	}
	sort.Strings(inv.Mappings)
	// mapping id -> node -> entries
	byNode := func(id, node string) []MappingEntry {
		var out []MappingEntry
		for _, e := range maps[id] {
			if e.Node == node {
				out = append(out, e)
			}
		}
		return out
	}
	for _, n := range nodes {
		gn := GPUNode{Node: n.Node, Online: n.Online, Cards: []GPUCard{}, Groups: []GPUGroupMapping{}}
		cards := map[string]*GPUCard{}
		for addr, info := range n.PCI {
			if info.IsNvidiaDisplay() {
				name := strings.TrimSpace(strings.TrimSpace(info.Vendor) + " " + strings.TrimSpace(info.Device))
				cards[addr] = &GPUCard{Addr: fullAddr(addr), Name: name, Group: info.Group, Status: "free", Uses: []GPUUse{}, Mappings: []string{}}
			}
		}
		// mappings pointing to this node's cards; group mappings (several cards on this node) are tracked apart
		groups := map[string]*GPUGroupMapping{}
		for _, id := range inv.Mappings {
			es := byNode(id, n.Node)
			var ours []MappingEntry
			for _, e := range es {
				if c, ok := cards[e.Addr]; ok {
					ours = append(ours, e)
					_ = c
				}
			}
			if len(ours) == 1 {
				cards[ours[0].Addr].Mappings = append(cards[ours[0].Addr].Mappings, id)
			} else if len(ours) > 1 {
				groups[id] = &GPUGroupMapping{ID: id, Cards: len(ours), Uses: []GPUUse{}}
			}
		}
		vms := append([]GPUVM(nil), n.VMs...)
		sort.Slice(vms, func(i, j int) bool { return vms[i].VMID < vms[j].VMID })
		for _, vm := range vms {
			slots := make([]string, 0, len(vm.HostPCI))
			for k := range vm.HostPCI {
				slots = append(slots, k)
			}
			sort.Strings(slots)
			seen := map[string]bool{}
			for _, slot := range slots {
				val := vm.HostPCI[slot]
				use := GPUUse{VMID: vm.VMID, Name: clampText(vm.Name, 64), Status: vm.Status, Lock: clampText(vm.Lock, 32), Slot: slot}
				if k, v, ok := strings.Cut(strings.SplitN(val, ",", 2)[0], "="); ok && k == "mapping" {
					use.Kind, use.Mapping = "mapping", v
					es := byNode(v, n.Node)
					if len(es) == 0 {
						if _, known := maps[v]; known && isNvidiaMapping(maps[v], nodes) {
							use.Editable = true
							inv.Problems = append(inv.Problems, GPUProblem{Kind: "mapping_no_node", Node: n.Node, Mapping: v, Uses: []GPUUse{use}, Fixable: true})
						}
						continue
					}
					if g, ok := groups[v]; ok {
						g.Uses = append(g.Uses, use)
						continue
					}
					for _, e := range es {
						if c, ok := cards[e.Addr]; ok && !seen[e.Addr] {
							seen[e.Addr] = true
							use.Editable = true
							c.Uses = append(c.Uses, use)
						} else if !ok {
							if nodeHasNonNvidia(n.PCI, e.Addr) {
								continue // a mapping of a NIC or other device: not ours
							}
							use.Editable = true
							inv.Problems = append(inv.Problems, GPUProblem{Kind: "device_missing", Node: n.Node, Mapping: v, Addr: fullAddr(e.Addr), Uses: []GPUUse{use}, Fixable: true})
						}
					}
					continue
				}
				use.Kind = "direct"
				for _, a := range hostpciAddresses(val, nil) {
					addr := normalizePCIAddr(a)
					if addr == "" || seen[addr] {
						continue
					}
					if c, ok := cards[addr]; ok {
						seen[addr] = true
						c.Uses = append(c.Uses, use)
					} else if _, exists := n.PCI[addr]; !exists {
						// Not on this node at all. Reported only when it looks like a GPU slot we cannot rule out:
						// the node's own PCI list is the only authority, so an address missing there is a real finding.
						seen[addr] = true
						inv.Problems = append(inv.Problems, GPUProblem{Kind: "device_missing", Node: n.Node, Addr: fullAddr(addr), Uses: []GPUUse{use}})
					}
				}
			}
		}
		ids := make([]string, 0, len(groups))
		for id := range groups {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			g := groups[id]
			distinct := map[int]bool{}
			for _, u := range g.Uses {
				distinct[u.VMID] = true
			}
			g.Overbooked = len(distinct) > g.Cards
			if g.Overbooked {
				inv.Problems = append(inv.Problems, GPUProblem{Kind: "overbooked", Node: n.Node, Mapping: id, Uses: g.Uses, Fixable: false})
			}
			gn.Groups = append(gn.Groups, *g)
		}
		addrs := make([]string, 0, len(cards))
		for a := range cards {
			addrs = append(addrs, a)
		}
		sort.Strings(addrs)
		for _, a := range addrs {
			c := cards[a]
			sort.Strings(c.Mappings)
			switch {
			case len(c.Uses) == 0:
				c.Status = "free"
			case len(c.Uses) == 1:
				c.Status = "assigned"
			default:
				c.Status = "conflict"
				fix := false
				for _, u := range c.Uses {
					fix = fix || u.Editable
				}
				inv.Problems = append(inv.Problems, GPUProblem{Kind: "double", Node: n.Node, Card: c.Name, Addr: c.Addr, Uses: c.Uses, Fixable: fix})
			}
			gn.Cards = append(gn.Cards, *c)
		}
		inv.Nodes = append(inv.Nodes, gn)
	}
	sort.SliceStable(inv.Nodes, func(i, j int) bool { return naturalLess(inv.Nodes[i].Node, inv.Nodes[j].Node) })
	sort.SliceStable(inv.Problems, func(i, j int) bool {
		if inv.Problems[i].Node != inv.Problems[j].Node {
			return naturalLess(inv.Problems[i].Node, inv.Problems[j].Node)
		}
		return inv.Problems[i].Kind < inv.Problems[j].Kind
	})
	return inv
}

// isNvidiaMapping: a mapping with no entry on a VM's node is a problem only if it maps NVIDIA cards somewhere.
func isNvidiaMapping(entries []MappingEntry, nodes []GPUNodeInput) bool {
	for _, e := range entries {
		for _, n := range nodes {
			if n.Node == e.Node {
				if info, ok := n.PCI[e.Addr]; ok && info.IsNvidiaDisplay() {
					return true
				}
			}
		}
	}
	return false
}

func nodeHasNonNvidia(t PCITable, addr string) bool {
	info, ok := t[addr]
	return ok && !info.IsNvidiaDisplay()
}

// ── fetching ────────────────────────────────────────────────────────────────

type gpuAPI interface {
	GetNodes() (json.RawMessage, error)
	GetNodePCI(node string) (json.RawMessage, error)
	GetPCIMappings() (json.RawMessage, error)
	GetVMs(node string) (json.RawMessage, error)
	GetVMConfig(node string, vmid int) (json.RawMessage, error)
}

var _ gpuAPI = (*api.Client)(nil)

func fetchGPUInventory(c gpuAPI, now time.Time) (GPUInventory, error) {
	raw, err := c.GetNodes()
	if err != nil {
		return GPUInventory{}, err
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &nodes) != nil {
		return GPUInventory{}, errors.New("elenco nodi non valido")
	}
	var maps MappingSet = MappingSet{}
	if mraw, err := c.GetPCIMappings(); err == nil {
		maps = ParsePCIMappingEntries(mraw)
	}
	inputs := make([]*GPUNodeInput, 0, len(nodes)) // pointers: goroutines fill VMs while the loop keeps appending
	var partial []string
	var mu sync.Mutex
	sem := make(chan struct{}, gpuConfigWorkers)
	var wg sync.WaitGroup
	total := 0
	for _, n := range nodes {
		if !nodeNamePattern.MatchString(n.Node) {
			continue
		}
		in := &GPUNodeInput{Node: n.Node, Online: n.Status == "online"}
		if !in.Online {
			inputs = append(inputs, in)
			continue
		}
		praw, err := c.GetNodePCI(n.Node)
		if err != nil {
			partial = append(partial, n.Node)
			inputs = append(inputs, in)
			continue
		}
		in.PCI = ParsePCIList(praw)
		vraw, err := c.GetVMs(n.Node)
		if err != nil {
			partial = append(partial, n.Node)
			inputs = append(inputs, in)
			continue
		}
		var list []map[string]interface{}
		_ = json.Unmarshal(vraw, &list)
		in.VMs = []GPUVM{}
		inputs = append(inputs, in)
		for _, v := range list {
			vmid, _ := v["vmid"].(float64)
			if vmid <= 0 || vmid > 999999999 || total >= maxGPUVMs {
				continue
			}
			total++
			name, _ := v["name"].(string)
			status, _ := v["status"].(string)
			wg.Add(1)
			go func(in *GPUNodeInput, node string, vmid int, name, status string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				cfgRaw, err := c.GetVMConfig(node, vmid)
				if err != nil {
					mu.Lock()
					partial = append(partial, node+"/"+strconv.Itoa(vmid))
					mu.Unlock()
					return
				}
				var cfg map[string]interface{}
				if json.Unmarshal(cfgRaw, &cfg) != nil {
					return
				}
				hp := map[string]string{}
				for k, val := range cfg {
					if s, ok := val.(string); ok && hostpciKey.MatchString(k) {
						hp[k] = s
					}
				}
				if len(hp) == 0 {
					return
				}
				lock, _ := cfg["lock"].(string)
				machine, _ := cfg["machine"].(string)
				mu.Lock()
				in.VMs = append(in.VMs, GPUVM{VMID: vmid, Name: name, Status: status, Lock: lock, Machine: machine, HostPCI: hp})
				mu.Unlock()
			}(in, n.Node, int(vmid), name, status)
		}
	}
	wg.Wait()
	plain := make([]GPUNodeInput, 0, len(inputs))
	for _, in := range inputs {
		plain = append(plain, *in)
	}
	inv := BuildGPUInventory(plain, maps, now)
	sort.Strings(partial)
	inv.Partial = append(inv.Partial, partial...)
	return inv, nil
}

// GetGPUs: GET /api/gpus (read only, cached 15 s per cluster).
func (h *Handler) GetGPUs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx := clusterIdx(r)
		client, err := h.getClientFor(idx)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		v, err := cephCached("gpus|"+strconv.Itoa(idx), gpuCacheTTL, func() (interface{}, error) {
			return fetchGPUInventory(client, time.Now())
		})
		if err != nil {
			log.Printf("gpus (cluster %d): %v", idx, err)
			writeError(w, errors.New("dati GPU non disponibili"), http.StatusBadGateway)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, v)
	}
}
