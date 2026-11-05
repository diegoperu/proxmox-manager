package balance

import (
	"errors"
	"sort"
)

// Place ranks the nodes for ONE new VM (ROADMAP 3b). It uses the same load model as Compute: assigned RAM over
// (physical - reserve), the standard deviation of the percentages as the thing to keep small, and the CPU
// warnings as soft information. A node is "not eligible" for a hard reason, always reported with the missing
// quantity so the page can say "12 GB of RAM are missing"; the user may still pick another node than the
// recommended one among the eligible ones (D58 A: a node over the RAM limit is never eligible).

// Reasons a node cannot take the new VM. `Extra` reasons come from the caller (storage, GPU card, mapping).
const (
	PlaceOffline     = "node_offline"
	PlaceRAM         = "ram"               // Missing = bytes
	PlaceVCPUThreads = "vcpu_over_threads" // Need = vCPU asked, Have = threads of the node
)

// PlaceRequest is the VM to place.
type PlaceRequest struct {
	Mem    int64    // assigned RAM, bytes (> 0)
	VCPU   int      // > 0
	Groups []string // groups of the new VM (soft preference to spread them, D49)
	// Extra maps a node to the reasons the caller already knows (e.g. "storage_unavailable", or "disk2_no_space"
	// with the bytes missing): the node is not eligible, the reasons are copied as they are.
	Extra map[string][]PlaceReason
}

// PlaceReason is one reason, with the numbers behind it when there are any.
type PlaceReason struct {
	Code    string `json:"code"`
	Missing int64  `json:"missing,omitempty"` // ram: bytes missing
	Need    int    `json:"need,omitempty"`    // vcpu_over_threads
	Have    int    `json:"have,omitempty"`
}

// PlaceNode is the answer for one node.
type PlaceNode struct {
	Name        string        `json:"name"`
	Eligible    bool          `json:"eligible"`
	Recommended bool          `json:"recommended"`
	Reasons     []PlaceReason `json:"reasons"`
	Assignable  int64         `json:"assignable"`
	LoadBefore  int64         `json:"load_before"`
	LoadAfter   int64         `json:"load_after"` // with the new VM (also for not eligible nodes)
	PctBefore   float64       `json:"pct_before"`
	PctAfter    float64       `json:"pct_after"`
	Free        int64         `json:"free"`         // assignable - load before; < 0 = already over the limit
	SpreadAfter float64       `json:"spread_after"` // cluster spread if the VM went here, points
	Warnings    []string      `json:"warnings"`     // CPU warnings of this node after the VM (soft)
	SameGroup   int           `json:"same_group"`   // VMs of the same group(s) already on the node
}

// PlaceResult: nodes sorted eligible first (best first), then the others by how close they are to being eligible.
type PlaceResult struct {
	Nodes        []PlaceNode `json:"nodes"`
	SpreadBefore float64     `json:"spread_before"`
	Recommended  string      `json:"recommended"` // "" when no node is eligible
}

// Place computes the ranking. Parameters: the reserves and CPU thresholds of p are used, the rest is ignored.
func Place(nodes []Node, vms []VM, req PlaceRequest, p Params) (PlaceResult, error) {
	if req.Mem <= 0 || req.Mem > 64*1024*GiB {
		return PlaceResult{}, errors.New("RAM della VM non valida")
	}
	if req.VCPU <= 0 || req.VCPU > 4096 {
		return PlaceResult{}, errors.New("vCPU della VM non valide")
	}
	if len(req.Groups) > 8 {
		return PlaceResult{}, errors.New("troppi gruppi")
	}
	p.Evacuate, p.Only, p.ColdVMs = nil, nil, nil
	if err := p.Validate(); err != nil {
		return PlaceResult{}, err
	}
	if len(nodes) == 0 || len(nodes) > maxNodes {
		return PlaceResult{}, errors.New("numero di nodi non valido")
	}
	if len(vms) > maxVMs {
		return PlaceResult{}, errors.New("troppe VM")
	}
	st, err := newState(nodes, vms, p)
	if err != nil {
		return PlaceResult{}, err
	}
	res := PlaceResult{Nodes: []PlaceNode{}, SpreadBefore: round2(st.spread())}
	for _, name := range st.names {
		n := st.nodes[name]
		pn := PlaceNode{Name: name, Reasons: []PlaceReason{}, Assignable: n.assign, LoadBefore: n.load,
			LoadAfter: n.load + req.Mem, PctBefore: round2(ratio(n.load, n.assign) * 100), Free: n.assign - n.load}
		pn.PctAfter = round2(ratio(pn.LoadAfter, n.assign) * 100)
		for _, g := range req.Groups {
			pn.SameGroup += n.groups[g]
		}
		if !n.in.Online {
			pn.Reasons = append(pn.Reasons, PlaceReason{Code: PlaceOffline})
		}
		if pn.LoadAfter > n.assign {
			pn.Reasons = append(pn.Reasons, PlaceReason{Code: PlaceRAM, Missing: pn.LoadAfter - n.assign})
		}
		// same hard rule as the balance: vCPUs <= threads, and a node whose threads are unknown cannot be checked
		if n.in.Cores <= 0 || req.VCPU > n.in.Cores {
			pn.Reasons = append(pn.Reasons, PlaceReason{Code: PlaceVCPUThreads, Need: req.VCPU, Have: n.in.Cores})
		}
		pn.Reasons = append(pn.Reasons, req.Extra[name]...)
		// the cluster spread and the CPU warnings with the VM on this node (online nodes only are destinations)
		n.load += req.Mem
		n.vcpu += req.VCPU
		// a new VM has no CPU history: it adds to the vCPU count, the real CPU percentage is left as it is
		pn.SpreadAfter = round2(st.spread())
		pn.Warnings = st.cpuWarnings(n)
		n.load -= req.Mem
		n.vcpu -= req.VCPU
		pn.Eligible = len(pn.Reasons) == 0
		res.Nodes = append(res.Nodes, pn)
	}
	// eligible first: lowest spread, then no CPU warnings, then fewer VMs of the same group, then name;
	// the others: smallest RAM shortfall first (closest to eligible), then name
	missing := func(n PlaceNode) int64 {
		var m int64
		for _, r := range n.Reasons {
			m += r.Missing
		}
		return m
	}
	sort.SliceStable(res.Nodes, func(i, j int) bool {
		a, b := res.Nodes[i], res.Nodes[j]
		if a.Eligible != b.Eligible {
			return a.Eligible
		}
		if a.Eligible {
			if a.SpreadAfter != b.SpreadAfter {
				return a.SpreadAfter < b.SpreadAfter
			}
			if (len(a.Warnings) == 0) != (len(b.Warnings) == 0) {
				return len(a.Warnings) == 0
			}
			if a.SameGroup != b.SameGroup {
				return a.SameGroup < b.SameGroup
			}
			return a.Name < b.Name
		}
		// a node that is down is never "close to eligible": it goes last
		if ao, bo := hasReason(a, PlaceOffline), hasReason(b, PlaceOffline); ao != bo {
			return !ao
		}
		if len(a.Reasons) != len(b.Reasons) {
			return len(a.Reasons) < len(b.Reasons)
		}
		if ma, mb := missing(a), missing(b); ma != mb {
			return ma < mb
		}
		return a.Name < b.Name
	})
	if len(res.Nodes) > 0 && res.Nodes[0].Eligible {
		res.Nodes[0].Recommended = true
		res.Recommended = res.Nodes[0].Name
	}
	return res, nil
}

func hasReason(n PlaceNode, code string) bool {
	for _, r := range n.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}
