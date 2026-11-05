package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func nv(dev string) PCIInfo {
	return PCIInfo{Class: "030200", Vendor: "NVIDIA Corporation", VendorID: "10de", Device: dev, Group: 1}
}

func testPCI() PCITable {
	return PCITable{
		"01:00": nv("A100 #1"),
		"02:00": nv("A100 #2"),
		"03:00": nv("A100 #3"),
		"04:00": nv("A100 #4"),
		"05:00": {Class: "030000", Vendor: "ASPEED Technology", VendorID: "1a03", Device: "AST2600", Group: 2},
		"06:00": {Class: "020000", Vendor: "Intel", VendorID: "8086", Device: "X710", Group: 3},
	}
}

func testMaps() MappingSet {
	return ParsePCIMappingEntries(json.RawMessage(`[
	 {"id":"a1","map":["node=n1,path=0000:01:00","node=n2,path=0000:01:00"]},
	 {"id":"a2","map":["node=n1,path=0000:02:00"]},
	 {"id":"a3","map":["node=n1,path=0000:03:00"]},
	 {"id":"grp","map":["node=n1,path=0000:03:00","node=n1,path=0000:04:00"]},
	 {"id":"nic","map":["node=n1,path=0000:06:00"]},
	 {"id":"only-n2","map":["node=n2,path=0000:01:00"]}]`))
}

func find(inv GPUInventory, node, addr string) GPUCard {
	for _, n := range inv.Nodes {
		if n.Node == node {
			for _, c := range n.Cards {
				if c.Addr == addr {
					return c
				}
			}
		}
	}
	return GPUCard{}
}

func kinds(inv GPUInventory) map[string]int {
	m := map[string]int{}
	for _, p := range inv.Problems {
		m[p.Kind]++
	}
	return m
}

func TestInventoryFreeAssignedDirectAndIgnored(t *testing.T) {
	maps := ParsePCIMappingEntries(json.RawMessage(`[{"id":"a2","map":["node=n1,path=0000:02:00"]},{"id":"nic","map":["node=n1,path=0000:06:00"]}]`))
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
		{VMID: 100, Name: "m", Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=a2,pcie=1"}},
		{VMID: 101, Name: "d", Status: "running", HostPCI: map[string]string{"hostpci0": "0000:01:00"}},
		{VMID: 102, Name: "nic", Status: "running", HostPCI: map[string]string{"hostpci0": "mapping=nic", "hostpci1": "0000:06:00.0", "hostpci2": "0000:05:00"}},
	}}}, maps, time.Unix(1, 0))
	if len(inv.Problems) != 0 {
		t.Fatalf("problems %+v", inv.Problems)
	}
	if len(inv.Nodes[0].Cards) != 4 { // BMC (ASPEED) and NIC excluded
		t.Fatalf("cards %+v", inv.Nodes[0].Cards)
	}
	c1, c2, c3 := find(inv, "n1", "0000:01:00"), find(inv, "n1", "0000:02:00"), find(inv, "n1", "0000:03:00")
	if c1.Status != "assigned" || c1.Uses[0].Kind != "direct" || c1.Uses[0].Editable {
		t.Fatalf("direct card %+v", c1)
	}
	if c2.Status != "assigned" || c2.Uses[0].Kind != "mapping" || !c2.Uses[0].Editable || c2.Mappings[0] != "a2" {
		t.Fatalf("mapped card %+v", c2)
	}
	if c3.Status != "free" || len(c3.Uses) != 0 {
		t.Fatalf("free card %+v", c3)
	}
}

func TestInventoryDoubleAssignmentDirectPlusMapping(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
		{VMID: 100, Name: "a", Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=a2"}},
		{VMID: 101, Name: "b", Status: "stopped", HostPCI: map[string]string{"hostpci0": "0000:02:00,rombar=0"}},
	}}}, testMaps(), time.Unix(1, 0))
	c := find(inv, "n1", "0000:02:00")
	if c.Status != "conflict" || len(c.Uses) != 2 {
		t.Fatalf("card %+v", c)
	}
	var p *GPUProblem
	for i := range inv.Problems {
		if inv.Problems[i].Kind == "double" {
			p = &inv.Problems[i]
		}
	}
	if p == nil || !p.Fixable || len(p.Uses) != 2 {
		t.Fatalf("problems %+v", inv.Problems)
	}
}

func TestInventoryDoubleBothDirectIsNotFixable(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
		{VMID: 100, Status: "stopped", HostPCI: map[string]string{"hostpci0": "0000:02:00"}},
		{VMID: 101, Status: "stopped", HostPCI: map[string]string{"hostpci0": "02:00.0"}},
	}}}, testMaps(), time.Unix(1, 0))
	if k := kinds(inv); k["double"] != 1 || inv.Problems[0].Fixable {
		t.Fatalf("%+v", inv.Problems)
	}
}

func TestInventorySameVMTwiceIsNotAConflict(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
		{VMID: 100, Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=a2", "hostpci1": "0000:02:00"}},
	}}}, testMaps(), time.Unix(1, 0))
	if len(inv.Problems) != 0 || find(inv, "n1", "0000:02:00").Status != "assigned" {
		t.Fatalf("%+v", inv)
	}
}

func TestInventoryCrossNodeProblems(t *testing.T) {
	n2 := PCITable{"01:00": nv("A100 n2")}
	inv := BuildGPUInventory([]GPUNodeInput{
		{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
			{VMID: 100, Name: "wrong-node-mapping", Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=only-n2"}},
			{VMID: 101, Name: "gone-card", Status: "stopped", HostPCI: map[string]string{"hostpci0": "0000:7a:00"}},
		}},
		{Node: "n2", Online: true, PCI: n2},
	}, testMaps(), time.Unix(1, 0))
	k := kinds(inv)
	if k["mapping_no_node"] != 1 || k["device_missing"] != 1 || len(inv.Problems) != 2 {
		t.Fatalf("%+v", inv.Problems)
	}
	if find(inv, "n2", "0000:01:00").Status != "free" {
		t.Fatal("the n2 card must stay free: the VM sits on n1")
	}
}

func TestInventoryGroupMappingOverbooked(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n1", Online: true, PCI: testPCI(), VMs: []GPUVM{
		{VMID: 1, Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=grp"}},
		{VMID: 2, Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=grp"}},
		{VMID: 3, Status: "stopped", HostPCI: map[string]string{"hostpci0": "mapping=grp"}},
	}}}, testMaps(), time.Unix(1, 0))
	if kinds(inv)["overbooked"] != 1 || len(inv.Nodes[0].Groups) != 1 || inv.Nodes[0].Groups[0].Cards != 2 {
		t.Fatalf("%+v", inv)
	}
	for _, c := range inv.Nodes[0].Cards {
		if c.Addr == "0000:04:00" && len(c.Uses) != 0 {
			t.Fatalf("group users must not be attributed to a card: %+v", c)
		}
	}
}

func TestInventoryOfflineAndOrder(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{{Node: "n10", Online: true, PCI: PCITable{}}, {Node: "n2", Online: false}}, MappingSet{}, time.Unix(1, 0))
	if inv.Nodes[0].Node != "n2" || inv.Nodes[0].Online || inv.Nodes[0].Cards == nil || inv.Problems == nil {
		t.Fatalf("%+v", inv)
	}
}

type fakeGPU struct{ failPCI map[string]bool }

func (f fakeGPU) GetNodes() (json.RawMessage, error) {
	return json.RawMessage(`[{"node":"n1","status":"online"},{"node":"n2","status":"online"},{"node":"bad name","status":"online"}]`), nil
}
func (f fakeGPU) GetNodePCI(n string) (json.RawMessage, error) {
	if f.failPCI[n] {
		return nil, errBoom
	}
	return json.RawMessage(`[{"id":"0000:01:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"A100","iommugroup":5},{"id":"0000:01:00.1","class":"0x040300","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"Audio"}]`), nil
}
func (f fakeGPU) GetPCIMappings() (json.RawMessage, error) {
	return json.RawMessage(`[{"id":"g1","map":["node=n1,path=0000:01:00"]}]`), nil
}
func (f fakeGPU) GetVMs(n string) (json.RawMessage, error) {
	return json.RawMessage(`[{"vmid":100,"name":"a","status":"stopped"},{"vmid":101,"name":"b","status":"running"}]`), nil
}
func (f fakeGPU) GetVMConfig(n string, vmid int) (json.RawMessage, error) {
	if vmid == 100 {
		return json.RawMessage(`{"hostpci0":"mapping=g1,pcie=1","machine":"q35","lock":"backup"}`), nil
	}
	return json.RawMessage(`{"net0":"virtio"}`), nil
}

var errBoom = &boom{}

type boom struct{}

func (*boom) Error() string { return "boom" }

func TestFetchGPUInventoryEndToEnd(t *testing.T) {
	inv, err := fetchGPUInventory(fakeGPU{failPCI: map[string]bool{"n2": true}}, time.Unix(5, 0))
	if err != nil {
		t.Fatal(err)
	}
	c := find(inv, "n1", "0000:01:00")
	if c.Status != "assigned" || c.Uses[0].VMID != 100 || c.Uses[0].Lock != "backup" || c.Group != 5 {
		t.Fatalf("%+v", c)
	}
	if len(inv.Partial) != 1 || inv.Partial[0] != "n2" || len(inv.Nodes) != 2 {
		t.Fatalf("partial=%v nodes=%d", inv.Partial, len(inv.Nodes))
	}
}

// The page iterates every list of the answer (n.cards.length, c.uses.map, ...): none may ever be null, whatever the
// state of the nodes (offline, no PCI data, no card, a free card with no mapping).
func TestInventoryJSONNeverHasNullLists(t *testing.T) {
	inv := BuildGPUInventory([]GPUNodeInput{
		{Node: "up", Online: true, PCI: testPCI(), VMs: nil},
		{Node: "down", Online: false, PCI: nil, VMs: nil},              // offline: nothing read
		{Node: "empty", Online: true, PCI: PCITable{}, VMs: []GPUVM{}}, // online, no cards
	}, testMaps(), time.Now())
	for name, v := range map[string]interface{}{"inventory": inv, "with corrections": withGPUModels(inv, nil)} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"nodes", "cards", "groups", "uses", "mappings", "problems", "partial"} {
			if strings.Contains(string(b), `"`+k+`":null`) {
				t.Errorf("%s: %q is null: %s", name, k, b)
			}
		}
	}
}
