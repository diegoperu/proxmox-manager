package handlers

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// PCI passthrough detection: which QEMU VMs have a video card (PCI class 0x03: VGA, 3D controller, display)
// attached as hostpciN. The VM config only holds the PCI address (`0000:01:00.0`) or a resource-mapping name
// (`mapping=gpu1`), so vendor and class come from the node's `/hardware/pci` list and from `/cluster/mapping/pci`.

const (
	maxPCIDevices   = 4000
	maxGPUsPerVM    = 16
	maxGPUNameChars = 120
)

var (
	hostpciKey = regexp.MustCompile(`^hostpci(\d{1,2})$`)
	// bus:device with optional domain and optional function: "0000:01:00", "01:00.0"
	pciAddrPat = regexp.MustCompile(`^(?:([0-9a-fA-F]{4}):)?([0-9a-fA-F]{2}:[0-9a-fA-F]{2})(?:\.[0-7])?$`)
	mappingPat = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// PCIInfo is what we keep of a node's PCI device.
type PCIInfo struct {
	Class    string // 6 hex digits, e.g. "030200"
	Vendor   string
	VendorID string // lowercase hex without 0x, e.g. "10de"
	Device   string
	Group    int // IOMMU group, -1 unknown
}

// IsNvidiaDisplay: the cards the app manages (video cards by NVIDIA; BMC/ASPEED controllers are excluded).
func (i PCIInfo) IsNvidiaDisplay() bool {
	return isDisplayClass(i.Class) && (i.VendorID == "10de" || strings.Contains(strings.ToLower(i.Vendor), "nvidia"))
}

// PCITable: normalised "bus:dev" (lowercase, domain 0000 assumed) -> device. A card with several functions
// (GPU + audio) shares the same bus:dev; the display function wins.
type PCITable map[string]PCIInfo

// GPU is one video card passed through to a VM.
type GPU struct {
	Name string `json:"name"`
}

func isDisplayClass(class string) bool {
	return strings.HasPrefix(strings.ToLower(class), "0x03") || strings.HasPrefix(class, "03")
}

func shortPCIClass(raw string) string {
	c := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if len(c) > 6 {
		c = c[:6]
	}
	return c
}

// ParsePCIList reads `/nodes/{node}/hardware/pci`.
func ParsePCIList(raw json.RawMessage) PCITable {
	t := PCITable{}
	var list []map[string]interface{}
	if json.Unmarshal(raw, &list) != nil {
		return t
	}
	for i, d := range list {
		if i >= maxPCIDevices {
			break
		}
		id, _ := d["id"].(string)
		m := pciAddrPat.FindStringSubmatch(strings.TrimSpace(id))
		if m == nil || (m[1] != "" && !strings.EqualFold(m[1], "0000")) {
			continue
		}
		key := strings.ToLower(m[2])
		class, _ := d["class"].(string)
		vendor, _ := d["vendor_name"].(string)
		device, _ := d["device_name"].(string)
		vid, _ := d["vendor"].(string)
		group := -1
		if g, ok := d["iommugroup"].(float64); ok && g >= 0 && g < 1e6 {
			group = int(g)
		}
		info := PCIInfo{Class: shortPCIClass(class), Vendor: clampText(vendor, 60), VendorID: strings.TrimPrefix(strings.ToLower(strings.TrimSpace(vid)), "0x"),
			Device: clampText(device, 80), Group: group}
		if old, ok := t[key]; ok && isDisplayClass(old.Class) && !isDisplayClass(info.Class) {
			continue
		}
		t[key] = info
	}
	return t
}

// MappingTable: mapping name -> PCI addresses on a given node.
type MappingTable map[string][]string

// ParsePCIMappings reads `/cluster/mapping/pci` for one node. Entries look like
// {"id":"gpu1","map":["node=pve1,path=0000:01:00,id=10de:1eb8,subsystem-id=..."]}.
func ParsePCIMappings(raw json.RawMessage, node string) MappingTable {
	t := MappingTable{}
	var list []struct {
		ID  string   `json:"id"`
		Map []string `json:"map"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return t
	}
	for _, e := range list {
		if !mappingPat.MatchString(e.ID) {
			continue
		}
		for _, entry := range e.Map {
			var n, path string
			for _, kv := range strings.Split(entry, ",") {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					continue
				}
				switch strings.TrimSpace(k) {
				case "node":
					n = strings.TrimSpace(v)
				case "path":
					path = strings.TrimSpace(v)
				}
			}
			if n == node && path != "" && len(t[e.ID]) < 32 {
				t[e.ID] = append(t[e.ID], path)
			}
		}
	}
	return t
}

// normalizePCIAddr -> "bus:dev" lowercase, or "" when the address is not in the plain domain-0000 form.
func normalizePCIAddr(s string) string {
	m := pciAddrPat.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || (m[1] != "" && !strings.EqualFold(m[1], "0000")) {
		return ""
	}
	return strings.ToLower(m[2])
}

// hostpciAddresses: the PCI addresses behind one hostpciN value ("0000:01:00,pcie=1", "host=01:00.0;02:00.0",
// "mapping=gpu1,pcie=1"). Unknown shapes yield nothing (never a guess).
func hostpciAddresses(val string, maps MappingTable) []string {
	var out []string
	for i, field := range strings.Split(val, ",") {
		field = strings.TrimSpace(field)
		k, v, hasKey := strings.Cut(field, "=")
		switch {
		case hasKey && k == "mapping":
			out = append(out, maps[v]...)
		case hasKey && k == "host":
			out = append(out, strings.Split(v, ";")...)
		case !hasKey && i == 0:
			out = append(out, strings.Split(field, ";")...)
		}
	}
	return out
}

// VMGPUs returns the video cards among a VM config's hostpci entries (deduplicated, sorted by name).
func VMGPUs(config map[string]interface{}, pci PCITable, maps MappingTable) []GPU {
	seen := map[string]bool{}
	gpus := []GPU{}
	for key, v := range config {
		if !hostpciKey.MatchString(key) {
			continue
		}
		val, _ := v.(string)
		for _, a := range hostpciAddresses(val, maps) {
			addr := normalizePCIAddr(a)
			info, ok := pci[addr]
			if !ok || !isDisplayClass(info.Class) || seen[addr] || len(gpus) >= maxGPUsPerVM {
				continue
			}
			seen[addr] = true
			name := strings.TrimSpace(strings.TrimSpace(info.Vendor) + " " + strings.TrimSpace(info.Device))
			if name == "" {
				name = "Scheda video"
			}
			gpus = append(gpus, GPU{Name: clampText(name, maxGPUNameChars)})
		}
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i].Name < gpus[j].Name })
	return gpus
}
