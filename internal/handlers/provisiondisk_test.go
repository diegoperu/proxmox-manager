package handlers

import (
	"strings"
	"testing"
	"time"
)

// The reason of a failed mount reaches the page in words (not as a generic "Proxmox error").
func TestProvisionMountWarningCarriesTheReason(t *testing.T) {
	provFast(t)
	for name, c := range map[string]struct {
		blank []string
		want  string
	}{
		"no disk":   {[]string{}, "non e' comparso"},
		"two disks": {[]string{"vdb", "vdc"}, "2 dischi vuoti"},
	} {
		f := newFakeProv()
		f.blank = c.blank
		j, _ := startProv(f, 30, "C", mustPlanProv(t, withDisk(baseReq(), 10)), noOpts, time.Now())
		v := waitProv(t, j)
		note := ""
		for _, s := range v.Steps {
			if s.Phase == provPhMount {
				note = s.Note
			}
		}
		if !strings.Contains(note, c.want) {
			t.Errorf("%s: note %q", name, note)
		}
	}
}
