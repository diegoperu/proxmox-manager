package handlers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What a template needs from the node that gets its clone (ROADMAP 10).
//
//   - disk volumes (disks, EFI, TPM, the cloud-init drive) are COPIED by a full clone: they need space on their storage
//     and the storage must be reachable from the target node;
//   - file references (the snippets of `cicustom`, the ISO images attached to the template) are NOT copied: the clone
//     only keeps the path, so the file must already exist on the target node (unless its storage is shared).

type fileRef struct {
	Kind    string // "snippets" | "iso": the content type of the storage listing
	Storage string
	Name    string // the volume name as written in the config, after "<storage>:<kind>/"
}

func (f fileRef) volid() string { return f.Storage + ":" + f.Kind + "/" + f.Name }

type tplInfo struct {
	MainStorage string           // storage of the main disk (the default of the extra disk)
	Storages    []string         // storages of the disk volumes, sorted
	Need        map[string]int64 // bytes the clone writes on each storage (main disk at the requested size)
	Files       []fileRef
	CurDiskGB   int
}

var (
	tplVolumeKey  = regexp.MustCompile(`^((virtio|scsi|sata|ide)[0-9]{1,2}|efidisk[0-9]|tpmstate[0-9])$`)
	cicustomItem  = regexp.MustCompile(`^(meta|network|user|vendor)=([A-Za-z0-9][A-Za-z0-9._-]{0,63}):snippets/([^\x00-\x1f/,]{1,128})$`)
	volumeNameSet = regexp.MustCompile(`^[^\x00-\x1f/,]{1,200}$`)
)

const cloudInitDriveBytes = 4 << 20 // Proxmox's cloud-init drive when the config does not say

// diskSizeBytes reads "size=32G" (K/M/G/T, decimals allowed) from a volume line.
func diskSizeBytes(line string) (int64, bool) {
	m := diskSizePattern.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	mult := map[string]float64{"": 1 << 30, "G": 1 << 30, "K": 1 << 10, "M": 1 << 20, "T": 1 << 40}[m[2]]
	if m[2] == "" {
		mult = 1 << 30
	}
	b := v * mult
	if b > 1<<50 {
		return 0, false
	}
	return int64(b), true
}

// parseTemplateInfo reads the volumes of a template config. diskDev is the main disk (resized to desiredGB if larger).
func parseTemplateInfo(cfg map[string]interface{}, diskDev string, desiredGB int) tplInfo {
	info := tplInfo{Need: map[string]int64{}}
	stor := map[string]bool{}
	for key, v := range cfg {
		line, _ := v.(string)
		if !tplVolumeKey.MatchString(key) || line == "" || strings.HasPrefix(line, "none") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 || !storageNamePattern.MatchString(line[:i]) {
			continue
		}
		sid, rest := line[:i], line[i+1:]
		vol := rest
		if k := strings.IndexByte(rest, ','); k >= 0 {
			vol = rest[:k]
		}
		if strings.HasPrefix(vol, "iso/") { // an ISO attached to the template: a file, not copied
			if name := strings.TrimPrefix(vol, "iso/"); volumeNameSet.MatchString(name) {
				info.Files = append(info.Files, fileRef{Kind: "iso", Storage: sid, Name: name})
			}
			continue
		}
		size, ok := diskSizeBytes(line)
		if !ok && strings.Contains(vol, "cloudinit") {
			size, ok = cloudInitDriveBytes, true
		}
		if key == diskDev {
			info.MainStorage = sid
			if ok {
				info.CurDiskGB = int((size + (1 << 30) - 1) >> 30)
				if want := int64(desiredGB) << 30; desiredGB > 0 && want > size {
					size = want
				}
			}
		}
		stor[sid] = true
		if ok {
			info.Need[sid] += size
		}
	}
	if c, _ := cfg["cicustom"].(string); c != "" {
		for _, item := range strings.Split(c, ",") {
			if m := cicustomItem.FindStringSubmatch(strings.TrimSpace(item)); m != nil {
				info.Files = append(info.Files, fileRef{Kind: "snippets", Storage: m[2], Name: m[3]})
			}
		}
	}
	for sid := range stor {
		info.Storages = append(info.Storages, sid)
	}
	sort.Strings(info.Storages)
	sort.Slice(info.Files, func(i, j int) bool { return info.Files[i].volid() < info.Files[j].volid() })
	if len(info.Files) > 16 {
		info.Files = info.Files[:16]
	}
	return info
}

// ── is a file present on a node? ──

type storageContentAPI interface {
	GetStorageContent(node, storage, content string) (json.RawMessage, error)
}

const fileListTTL = 60 * time.Second

// fileOnNode: present=false only when the listing was read and the volume is not in it; known=false when it could
// not be read (then nothing is blocked, the page says the check was not possible).
func fileOnNode(c interface{}, cluster int, node string, ref fileRef) (present, known bool) {
	sc, ok := c.(storageContentAPI)
	if !ok || !nodeNamePattern.MatchString(node) || !storageNamePattern.MatchString(ref.Storage) || (ref.Kind != "snippets" && ref.Kind != "iso") {
		return false, false
	}
	key := fmt.Sprintf("content|%d|%s|%s|%s", cluster, node, ref.Storage, ref.Kind)
	v, err := cephCached(key, fileListTTL, func() (interface{}, error) {
		raw, err := sc.GetStorageContent(node, ref.Storage, ref.Kind)
		if err != nil {
			return nil, err
		}
		var list []struct {
			VolID string `json:"volid"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, fmt.Errorf("elenco non valido")
		}
		set := map[string]bool{}
		for _, x := range list {
			set[x.VolID] = true
		}
		return set, nil
	})
	if err != nil {
		return false, false
	}
	set, _ := v.(map[string]bool)
	return set[ref.volid()], true
}
