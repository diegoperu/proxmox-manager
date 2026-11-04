package handlers

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

func TestVMGroupConvention(t *testing.T) {
	cases := map[string]string{
		"dinitto-vm-gpu-1": "dinitto", "Rossi-VM-2": "rossi", "ai-testing-diego": "ai", "gpu-test": "gpu",
		"nodash": otherGroup, "": otherGroup, "-leading": otherGroup, "bad_name-1": otherGroup, "ro ssi-1": otherGroup,
		strings.Repeat("a", 33) + "-x": otherGroup, "  spaced-1": "spaced", "caffè-1": otherGroup,
	}
	for in, want := range cases {
		if got := vmGroup(in); got != want {
			t.Fatalf("%q -> %q want %q", in, got, want)
		}
	}
}

const snapRes = `[
 {"type":"node","node":"n1","status":"online","maxcpu":64,"maxmem":1099511627776},
 {"type":"node","node":"n2","status":"offline","maxcpu":0,"maxmem":0},
 {"type":"node","node":"bad name","status":"online","maxcpu":1,"maxmem":1},
 {"type":"qemu","node":"n1","vmid":1,"name":"rossi-vm-1","tags":"gruppo-rossi","status":"running","maxcpu":4,"maxmem":8589934592,"maxdisk":107374182400},
 {"type":"qemu","node":"n1","vmid":2,"name":"rossi-vm-2","status":"stopped","maxcpu":2,"maxmem":4294967296,"maxdisk":53687091200},
 {"type":"lxc","node":"n1","vmid":3,"name":"rossi-ct","status":"running","maxcpu":1,"maxmem":1073741824,"maxdisk":10737418240},
 {"type":"qemu","node":"n1","vmid":4,"name":"bianchi-gpu-1","tags":"gpu;gruppo-bianchi","status":"running","maxcpu":8,"maxmem":17179869184,"maxdisk":0},
 {"type":"qemu","node":"n1","vmid":5,"name":"rocky-template","status":"stopped","template":1,"maxcpu":2,"maxmem":1,"maxdisk":1},
 {"type":"qemu","node":"n1","vmid":6,"name":"misc","status":"running","maxcpu":1,"maxmem":1,"maxdisk":1},
 {"type":"storage","node":"n1","storage":"local","status":"available"}]`

func TestBuildSnapshotCountsAndNoNames(t *testing.T) {
	gpu := &GPUInventory{Nodes: []GPUNode{{Node: "n1", Cards: []GPUCard{
		{Name: "NVIDIA A100", Uses: []GPUUse{{VMID: 4, Name: "bianchi-gpu-1"}}},
		{Name: "NVIDIA A100"}, {Name: "NVIDIA A100", Uses: []GPUUse{{VMID: 1, Name: "rossi-vm-1"}}}}}}}
	snap, err := BuildSnapshot(json.RawMessage(snapRes), gpu, "CALCOLO", time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Day != "2026-11-03" || !snap.GPUKnown || len(snap.Nodes) != 2 {
		t.Fatalf("%+v", snap)
	}
	byKey := map[string]history.GroupRow{}
	for _, g := range snap.Groups {
		byKey[g.Group+"/"+g.Kind] = g
	}
	r := byKey["rossi/qemu"]
	if r.VMs != 2 || r.Running != 1 || r.Cores != 6 || r.MemBytes != 12884901888 || r.DiskBytes != 161061273600 {
		t.Fatalf("rossi %+v", r)
	}
	if byKey["rossi/lxc"].VMs != 1 || byKey["bianchi/qemu"].VMs != 1 || byKey[otherGroup+"/qemu"].VMs != 1 || len(snap.Groups) != 4 {
		t.Fatalf("%+v", snap.Groups)
	}
	cards := map[string]int{}
	for _, g := range snap.GPUs {
		cards[g.Group] += g.Cards
	}
	if cards["bianchi"] != 1 || cards["rossi"] != 1 || cards[""] != 1 {
		t.Fatalf("gpus %+v", snap.GPUs)
	}
	// no VM name, IP or other identifier may be in what is stored
	b, _ := json.Marshal(snap)
	for _, leak := range []string{"vm-1", "-gpu-1", "rocky", "bad name"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("%q leaked into the snapshot: %s", leak, b)
		}
	}
}

func TestBuildSnapshotGPUUnknown(t *testing.T) {
	snap, err := BuildSnapshot(json.RawMessage(snapRes), nil, "X", time.Now())
	if err != nil || snap.GPUKnown || len(snap.GPUs) != 0 {
		t.Fatalf("%+v %v", snap, err)
	}
	if _, err := BuildSnapshot(json.RawMessage(`nope`), nil, "X", time.Now()); err == nil {
		t.Fatal("expected error")
	}
}

type fakeSnapAPI struct {
	fakeGPU
	resErr error
	calls  *int
}

func (f fakeSnapAPI) GetClusterResources(string) (json.RawMessage, error) {
	*f.calls++
	if f.resErr != nil {
		return nil, f.resErr
	}
	return json.RawMessage(snapRes), nil
}

func TestTakeMissingSnapshotsOncePerDayAndRetry(t *testing.T) {
	store, err := history.New(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := 0
	failing := true
	mk := func(config.ClusterConfig) snapshotAPI {
		var e error
		if failing {
			e = errors.New("proxmox down")
		}
		return fakeSnapAPI{fakeGPU: fakeGPU{}, resErr: e, calls: &calls}
	}
	clusters := []config.ClusterConfig{{Label: "A", URL: "https://x"}, {Label: "", URL: "https://y"}, {Label: "B", URL: ""}}
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.Local)
	takeMissingSnapshots(store, clusters, now, mk)
	if ok, _ := store.Has("2026-11-03", "A"); ok {
		t.Fatal("failed read must not store anything")
	}
	failing = false
	takeMissingSnapshots(store, clusters, now.Add(time.Hour), mk)
	if ok, _ := store.Has("2026-11-03", "A"); !ok {
		t.Fatal("retry must store")
	}
	before := calls
	takeMissingSnapshots(store, clusters, now.Add(2*time.Hour), mk)
	if calls != before {
		t.Fatal("a second snapshot on the same day must not even read Proxmox")
	}
	takeMissingSnapshots(store, clusters, now.Add(24*time.Hour), mk)
	if ok, _ := store.Has("2026-11-04", "A"); !ok {
		t.Fatal("next day must store")
	}
	snap, ok, _ := store.Load("2026-11-03", "A")
	if !ok || len(snap.Groups) == 0 {
		t.Fatalf("%+v", snap)
	}
}
