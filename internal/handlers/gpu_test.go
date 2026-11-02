package handlers

import (
	"encoding/json"
	"testing"
)

func TestVMGPUsRawAddressAndMapping(t *testing.T) {
	pci := ParsePCIList(json.RawMessage(`[
	 {"id":"0000:01:00.1","class":"0x040300","vendor_name":"NVIDIA Corporation","device_name":"GA102 High Definition Audio"},
	 {"id":"0000:01:00.0","class":"0x030000","vendor_name":"NVIDIA Corporation","device_name":"GA102 [GeForce RTX 3090]"},
	 {"id":"0000:02:00.0","class":"0x030200","vendor_name":"NVIDIA Corporation","device_name":"A100 PCIe"},
	 {"id":"0000:03:00.0","class":"0x020000","vendor_name":"Intel","device_name":"X710 NIC"},
	 {"id":"0001:05:00.0","class":"0x030000","vendor_name":"Other","device_name":"Other domain"}]`))
	maps := ParsePCIMappings(json.RawMessage(`[{"id":"gpu-a100","map":["node=pve1,path=0000:02:00,id=10de:20f1","node=pve2,path=0000:09:00"]}]`), "pve1")

	cases := []struct {
		name string
		cfg  map[string]interface{}
		want []string
	}{
		{"raw address, GPU+audio share the slot", map[string]interface{}{"hostpci0": "0000:01:00,pcie=1,x-vga=1"}, []string{"NVIDIA Corporation GA102 [GeForce RTX 3090]"}},
		{"short address with function", map[string]interface{}{"hostpci0": "01:00.0"}, []string{"NVIDIA Corporation GA102 [GeForce RTX 3090]"}},
		{"3D controller via mapping", map[string]interface{}{"hostpci1": "mapping=gpu-a100,pcie=1"}, []string{"NVIDIA Corporation A100 PCIe"}},
		{"NIC is not a GPU", map[string]interface{}{"hostpci0": "0000:03:00.0"}, []string{}},
		{"unknown mapping or address", map[string]interface{}{"hostpci0": "mapping=nope", "hostpci1": "0000:ff:00.0"}, []string{}},
		{"no hostpci / other keys", map[string]interface{}{"hostpciX": "01:00", "net0": "virtio"}, []string{}},
		{"two hostpci to the same card are one", map[string]interface{}{"hostpci0": "0000:02:00", "hostpci1": "mapping=gpu-a100"}, []string{"NVIDIA Corporation A100 PCIe"}},
		{"other PCI domain ignored", map[string]interface{}{"hostpci0": "0001:05:00.0"}, []string{}},
	}
	for _, c := range cases {
		got := VMGPUs(c.cfg, pci, maps)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %+v want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i].Name != c.want[i] {
				t.Fatalf("%s: got %+v want %v", c.name, got, c.want)
			}
		}
	}
	if g := VMGPUs(map[string]interface{}{"hostpci0": "01:00"}, nil, nil); g == nil || len(g) != 0 {
		t.Fatalf("nil tables must give an empty, non-nil list: %v", g)
	}
}

func TestParsePCIMappingsRejectsOddNames(t *testing.T) {
	m := ParsePCIMappings(json.RawMessage(`[{"id":"bad name!","map":["node=n,path=0000:01:00"]},{"id":"ok","map":["node=n,path=0000:01:00"]}]`), "n")
	if len(m) != 1 || len(m["ok"]) != 1 {
		t.Fatalf("%v", m)
	}
}
