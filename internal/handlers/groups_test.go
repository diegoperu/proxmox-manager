package handlers

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestTagGroups(t *testing.T) {
	cases := map[string][]string{
		"":                                         nil,
		"cloudinit;ubuntu-template;24.04":          nil,
		"gruppo-rossi":                             {"rossi"},
		"Gruppo-Rossi;LTSC":                        {"rossi"},
		"gruppo-rossi;gruppo-bianchi":              {"bianchi", "rossi"},
		"gruppo-rossi;GRUPPO-ROSSI":                {"rossi"},
		"gruppo-ro.ssi;gruppo-;gruppo-a_b":         nil,
		"gruppo-altro":                             nil, // reserved
		"gruppo-" + string(make([]byte, 33)):       nil,
		"gruppo-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": nil,       // 33 chars
		"x,gruppo-rossi y":                         {"rossi"}, // old separators
	}
	for in, want := range cases {
		if got := tagGroups(in); !reflect.DeepEqual(got, want) {
			t.Errorf("tagGroups(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolveFollowsD36(t *testing.T) {
	r := newGroupResolver([]string{"gruppo-rossi", "cloudinit", ""})
	cases := []struct {
		name, tags string
		groups     []string
		src        string
	}{
		{"qualunque", "gruppo-bianchi", []string{"bianchi"}, groupSrcTag},
		{"rossi-vm2", "", []string{"rossi"}, groupSrcName},                    // prefix exists as a tag elsewhere
		{"ubuntu-test", "", []string{otherGroup}, groupSrcNone},               // prefix not an existing group
		{"bianchi-vm", "cloudinit;24.04", []string{otherGroup}, groupSrcNone}, // no tag, group unknown
		{"rossi-vm", "gruppo-a;gruppo-b", []string{"a", "b"}, groupSrcTag},    // tag wins over the name
		{"nodash", "", []string{otherGroup}, groupSrcNone},
	}
	for _, c := range cases {
		got := r.resolve(c.name, c.tags)
		if !reflect.DeepEqual(got.Groups, c.groups) || got.Source != c.src {
			t.Errorf("%q %q -> %+v, want %v %s", c.name, c.tags, got, c.groups, c.src)
		}
	}
}

func TestSplitEvenSumsExactly(t *testing.T) {
	for _, total := range []int64{0, 1, 7, 1000, 8589934592, 1<<40 + 3} {
		for n := 1; n <= 5; n++ {
			sum := int64(0)
			parts := splitEven(total, n)
			for _, p := range parts {
				sum += p
			}
			if sum != total || len(parts) != n || parts[0] < parts[n-1] {
				t.Fatalf("total %d n %d: %v", total, n, parts)
			}
		}
	}
	if splitEven(10, 0) != nil {
		t.Fatal("n=0")
	}
}

const sharedRes = `[
 {"type":"node","node":"n1","status":"online","maxcpu":64,"maxmem":1099511627776},
 {"type":"qemu","node":"n1","vmid":1,"name":"a-1","tags":"gruppo-a","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":100},
 {"type":"qemu","node":"n1","vmid":2,"name":"shared","tags":"gruppo-b;gruppo-a","status":"running","maxcpu":3,"maxmem":10,"maxdisk":5},
 {"type":"qemu","node":"n1","vmid":3,"name":"a-stopped","status":"stopped","maxcpu":1,"maxmem":4,"maxdisk":1},
 {"type":"qemu","node":"n1","vmid":4,"name":"zz-1","status":"running","maxcpu":1,"maxmem":1,"maxdisk":1},
 {"type":"qemu","node":"n1","vmid":5,"name":"base-template","tags":"gruppo-tpl","template":1,"status":"stopped","maxcpu":2,"maxmem":1,"maxdisk":1},
 {"type":"qemu","node":"n1","vmid":6,"name":"tpl-1","status":"running","maxcpu":1,"maxmem":1,"maxdisk":1}]`

func TestSnapshotSharedVMsSplitAndTotals(t *testing.T) {
	gpu := &GPUInventory{Nodes: []GPUNode{{Node: "n1", Cards: []GPUCard{
		{Name: "A100", Uses: []GPUUse{{VMID: 2}}}, {Name: "A100", Uses: []GPUUse{{VMID: 1}}}, {Name: "A100"}, {Name: "A100", Uses: []GPUUse{{VMID: 999}}}}}}}
	snap, err := BuildSnapshot(json.RawMessage(sharedRes), gpu, "C", time.Date(2026, 11, 4, 8, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if snap.GroupRule != "tags" || !snap.HasTotals || snap.TotalVMs != 5 || snap.TotalRunning != 4 || snap.SharedVMs != 1 {
		t.Fatalf("%+v", snap)
	}
	var mem, disk, milli int64
	by := map[string]int{}
	for _, g := range snap.Groups {
		mem += g.MemBytes
		disk += g.DiskBytes
		milli += int64(g.CoresMilli)
		by[g.Group] += g.VMs
	}
	// the sum of the groups equals the cluster (templates excluded): 5 VMs
	if mem != 8589934592+10+4+1+1 || disk != 100+5+1+1+1 || milli != 10000 {
		t.Fatalf("resources not conserved: mem %d disk %d milli %d", mem, disk, milli)
	}
	// "a" has VM 1 + shared + a-stopped (name rule, a exists); "b" only the shared one; zz-1 and tpl-1 are "altro"
	if by["a"] != 3 || by["b"] != 1 || by[otherGroup] != 2 {
		t.Fatalf("%v", by)
	}
	for _, g := range snap.Groups {
		if g.Group == "b" && (g.MemBytes != 5 || g.CoresMilli != 1500 || g.SharedVMs != 1) {
			t.Fatalf("b %+v", g)
		}
		if g.Group == "a" && g.Kind == "qemu" && g.SharedVMs != 1 {
			t.Fatalf("a %+v", g)
		}
		if g.Group == "tpl" {
			t.Fatal("a template tag created a group")
		}
	}
	// GPU: 4 cards = 4000 thousandths conserved; shared VM's card split a/b, free card unassigned, unknown VM -> altro
	var tot int
	got := map[string]int{}
	for _, c := range snap.GPUs {
		tot += c.CardsMilli
		got[c.Group] += c.CardsMilli
	}
	if tot != 4000 || got["a"] != 1500 || got["b"] != 500 || got[""] != 1000 || got[otherGroup] != 1000 {
		t.Fatalf("gpu %v", got)
	}
}
