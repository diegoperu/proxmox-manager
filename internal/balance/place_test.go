package balance

import (
	"math/rand"
	"testing"
)

func placeIDs(r PlaceResult) []string {
	out := []string{}
	for _, n := range r.Nodes {
		out = append(out, n.Name)
	}
	return out
}

func TestPlacePicksTheLeastLoadedNodeAndEvensOut(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0), node("c", 100, 32, 0)}
	vms := []VM{vm("a", 1, 60, 4), vm("b", 2, 20, 4), vm("c", 3, 40, 4)}
	r, err := Place(nodes, vms, PlaceRequest{Mem: 10 * GiB, VCPU: 2}, noReserve())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommended != "b" || !r.Nodes[0].Recommended || r.Nodes[0].Name != "b" {
		t.Fatalf("recommended %q %v", r.Recommended, placeIDs(r))
	}
	if r.Nodes[0].PctBefore != 20 || r.Nodes[0].PctAfter != 30 || r.Nodes[0].Free != 80*GiB {
		t.Fatalf("%+v", r.Nodes[0])
	}
	// placing it on the loaded node would leave the cluster less even
	last := r.Nodes[2]
	if last.Name != "a" || last.SpreadAfter <= r.Nodes[0].SpreadAfter {
		t.Fatalf("%+v vs %+v", last, r.Nodes[0])
	}
}

func TestPlaceRAMIsAHardLimitWithTheMissingAmount(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0), node("b", 100, 32, 0)}
	vms := []VM{vm("a", 1, 90, 4), vm("b", 2, 85, 4)}
	r, err := Place(nodes, vms, PlaceRequest{Mem: 20 * GiB, VCPU: 2}, noReserve())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommended != "" {
		t.Fatalf("no node should be eligible: %+v", r)
	}
	// closest to eligible first: b misses 5 GB, a misses 10 GB
	if r.Nodes[0].Name != "b" || r.Nodes[0].Eligible || r.Nodes[0].Reasons[0].Code != PlaceRAM || r.Nodes[0].Reasons[0].Missing != 5*GiB {
		t.Fatalf("%+v", r.Nodes[0])
	}
	if r.Nodes[1].Reasons[0].Missing != 10*GiB {
		t.Fatalf("%+v", r.Nodes[1])
	}
	// a node already over its limit has a negative Free and is not eligible
	vms = append(vms, vm("a", 3, 30, 4))
	r, _ = Place(nodes, vms, PlaceRequest{Mem: GiB, VCPU: 1}, noReserve())
	for _, n := range r.Nodes {
		if n.Name == "a" && (n.Free >= 0 || n.Eligible) {
			t.Fatalf("%+v", n)
		}
	}
}

func TestPlaceReserveCountsAsInTheBalance(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 4)} // reserve 8 + 4*4 = 24 GB
	r, err := Place(nodes, nil, PlaceRequest{Mem: 80 * GiB, VCPU: 2}, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if r.Nodes[0].Eligible || r.Nodes[0].Reasons[0].Missing != 4*GiB || r.Nodes[0].Assignable != 76*GiB {
		t.Fatalf("%+v", r.Nodes[0])
	}
}

func TestPlaceOfflineThreadsAndExtraReasons(t *testing.T) {
	off := node("off", 100, 32, 0)
	off.Online = false
	small := node("small", 100, 4, 0)
	nodes := []Node{off, small, node("ok", 100, 32, 0), node("nostorage", 100, 32, 0)}
	r, err := Place(nodes, nil, PlaceRequest{Mem: GiB, VCPU: 8, Extra: map[string][]PlaceReason{"nostorage": {{Code: "storage_unavailable"}, {Code: "disk2_no_space", Missing: 5 * GiB}}}}, noReserve())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PlaceNode{}
	for _, n := range r.Nodes {
		by[n.Name] = n
	}
	if !by["ok"].Eligible || r.Recommended != "ok" {
		t.Fatalf("%+v", r)
	}
	if c := by["off"].Reasons[0].Code; c != PlaceOffline || by["off"].Eligible {
		t.Fatalf("%+v", by["off"])
	}
	if rs := by["small"].Reasons; len(rs) != 1 || rs[0].Code != PlaceVCPUThreads || rs[0].Need != 8 || rs[0].Have != 4 {
		t.Fatalf("%+v", by["small"])
	}
	if rs := by["nostorage"].Reasons; len(rs) != 2 || rs[0].Code != "storage_unavailable" || rs[1].Missing != 5*GiB {
		t.Fatalf("%+v", by["nostorage"])
	}
}

func TestPlaceTieBreaksCPUWarningsThenGroupsThenName(t *testing.T) {
	a, b, c := node("a", 100, 8, 0), node("b", 100, 8, 0), node("c", 100, 8, 0)
	a.CPUP95, b.CPUP95, c.CPUP95 = 95, 10, 10 // a is above 80% already
	// same load on every node: the spread is identical, the tie-breaks decide
	vms := []VM{vm("a", 1, 10, 1), vm("b", 2, 10, 1), vm("c", 3, 10, 1)}
	vms[2].Groups = []string{"g"}
	r, _ := Place([]Node{a, b, c}, vms, PlaceRequest{Mem: GiB, VCPU: 1, Groups: []string{"g"}}, noReserve())
	// a: CPU warning; c: same group; b first
	if got := placeIDs(r); got[0] != "b" || got[1] != "c" || got[2] != "a" {
		t.Fatalf("%v %+v", got, r.Nodes)
	}
	if len(r.Nodes[2].Warnings) == 0 || r.Nodes[1].SameGroup != 1 {
		t.Fatalf("%+v", r.Nodes)
	}
	// deterministic
	r2, _ := Place([]Node{a, b, c}, vms, PlaceRequest{Mem: GiB, VCPU: 1, Groups: []string{"g"}}, noReserve())
	if placeIDs(r2)[0] != "b" {
		t.Fatal("not deterministic")
	}
}

func TestPlaceInvalidInput(t *testing.T) {
	nodes := []Node{node("a", 100, 32, 0)}
	for name, req := range map[string]PlaceRequest{
		"no mem": {Mem: 0, VCPU: 1}, "negative mem": {Mem: -1, VCPU: 1}, "huge mem": {Mem: 1 << 62, VCPU: 1},
		"no vcpu": {Mem: GiB}, "huge vcpu": {Mem: GiB, VCPU: 1 << 20}, "many groups": {Mem: GiB, VCPU: 1, Groups: make([]string, 9)},
	} {
		if _, err := Place(nodes, nil, req, noReserve()); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Place(nil, nil, PlaceRequest{Mem: GiB, VCPU: 1}, noReserve()); err == nil {
		t.Error("no nodes accepted")
	}
	bad := noReserve()
	bad.HostReserve = -1
	if _, err := Place(nodes, nil, PlaceRequest{Mem: GiB, VCPU: 1}, bad); err == nil {
		t.Error("bad params accepted")
	}
	// a VM on an unknown node is an input error, not a crash
	if _, err := Place(nodes, []VM{vm("zzz", 1, 1, 1)}, PlaceRequest{Mem: GiB, VCPU: 1}, noReserve()); err == nil {
		t.Error("unknown node accepted")
	}
}

// Invariants on random clusters: the recommended node is eligible and has the lowest spread among eligible
// ones; an eligible node never ends beyond its assignable RAM; reasons and eligibility agree.
func TestPlaceInvariantsRandomClusters(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for round := 0; round < 300; round++ {
		var nodes []Node
		nn := 1 + r.Intn(10)
		for i := 0; i < nn; i++ {
			n := node(string(rune('a'+i)), int64(32+r.Intn(1000)), 4+r.Intn(120), r.Intn(5))
			n.Online = r.Intn(10) > 0
			n.CPUP95 = float64(r.Intn(100))
			nodes = append(nodes, n)
		}
		var vms []VM
		for i := 0; i < r.Intn(80); i++ {
			v := vm(nodes[r.Intn(nn)].Name, 100+i, int64(1+r.Intn(100)), 1+r.Intn(16))
			v.Running = r.Intn(4) > 0
			vms = append(vms, v)
		}
		req := PlaceRequest{Mem: int64(1+r.Intn(200)) * GiB, VCPU: 1 + r.Intn(64)}
		res, err := Place(nodes, vms, req, DefaultParams())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Nodes) != nn {
			t.Fatalf("round %d: %d nodes", round, len(res.Nodes))
		}
		best := -1.0
		for i, n := range res.Nodes {
			if n.Eligible != (len(n.Reasons) == 0) {
				t.Fatalf("round %d: eligibility and reasons disagree %+v", round, n)
			}
			if n.Eligible {
				if n.LoadAfter > n.Assignable {
					t.Fatalf("round %d: eligible node beyond its limit %+v", round, n)
				}
				if best < 0 {
					best = n.SpreadAfter
					if !n.Recommended || res.Recommended != n.Name || i != 0 {
						t.Fatalf("round %d: first eligible is not the recommendation %+v", round, res)
					}
				} else if n.SpreadAfter < best {
					t.Fatalf("round %d: eligible nodes not sorted by spread", round)
				}
			}
			if !n.Eligible && n.Recommended {
				t.Fatalf("round %d: recommended but not eligible", round)
			}
		}
		for i := 1; i < len(res.Nodes); i++ {
			if res.Nodes[i].Eligible && !res.Nodes[i-1].Eligible {
				t.Fatalf("round %d: eligible node after a not eligible one", round)
			}
		}
		if (best < 0) != (res.Recommended == "") {
			t.Fatalf("round %d: recommended %q with best %v", round, res.Recommended, best)
		}
	}
}

func TestPlaceOfflineNodesGoLastAmongTheNotEligible(t *testing.T) {
	off := node("a-off", 1000, 32, 0) // plenty of RAM, but down
	off.Online = false
	nodes := []Node{off, node("b", 100, 32, 0), node("c", 100, 32, 0)}
	vms := []VM{vm("b", 1, 90, 2), vm("c", 2, 80, 2)}
	r, err := Place(nodes, vms, PlaceRequest{Mem: 50 * GiB, VCPU: 2}, noReserve())
	if err != nil {
		t.Fatal(err)
	}
	if got := placeIDs(r); got[0] != "c" || got[1] != "b" || got[2] != "a-off" {
		t.Fatalf("%v", got)
	}
}

func TestPlaceUnknownThreadsIsNotEligible(t *testing.T) {
	r, err := Place([]Node{node("unk", 100, 0, 0), node("ok", 100, 8, 0)}, nil, PlaceRequest{Mem: GiB, VCPU: 2}, noReserve())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommended != "ok" || r.Nodes[1].Name != "unk" || r.Nodes[1].Eligible || r.Nodes[1].Reasons[0].Code != PlaceVCPUThreads {
		t.Fatalf("%+v", r.Nodes)
	}
}
