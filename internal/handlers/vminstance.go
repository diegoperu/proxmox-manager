package handlers

import (
	"regexp"
	"strings"

	"proxmox-manager/internal/history"
)

// VMIDs and IPs are reused after a VM is deleted (D121): stored history is keyed by an instance id.
// For QEMU it is the smbios1 uuid, measured on CALCOLO and HOSTING as present on every VM and never
// repeated (clones get a new one); meta ctime is NOT used, clones copy the template's.

var smbiosUUID = regexp.MustCompile(`(?:^|,)uuid=([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})(?:,|$)`)

// vmInstance returns "u:<uuid>" from a QEMU config, "" when there is no valid smbios1 uuid.
func vmInstance(cfg map[string]interface{}) string {
	s, _ := cfg["smbios1"].(string)
	m := smbiosUUID.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	inst := "u:" + strings.ToLower(m[1])
	if !history.InstPattern.MatchString(inst) {
		return ""
	}
	return inst
}

type vmInstKey struct {
	vmid int
	inst string
}

// storedVMNames maps (VMID, instance) to the latest name recorded in vm_use_day over the report period.
// Empty when the history is unavailable: callers show the VMID alone.
func storedVMNames(hist *history.Store, cluster string, p reportParams) map[vmInstKey]string {
	out := map[vmInstKey]string{}
	if hist == nil {
		return out
	}
	rows, err := hist.VMUseDays(cluster, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
	if err != nil {
		return out
	}
	for _, r := range rows { // ordered by day: the latest name wins
		out[vmInstKey{r.VMID, r.Inst}] = r.Name
	}
	return out
}
