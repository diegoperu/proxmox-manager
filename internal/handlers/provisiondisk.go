package handlers

import (
	"fmt"
	"strings"
	"time"
)

// formatAndMountBlank partitions, formats (ext4) and mounts the ONE blank disk of the guest, for the extra disk of
// a new VM (D64 B). Same guest script and same rules as the "Aggiungi disco" button of the VM panel (SetupVMDisk),
// with one deliberate difference: it never stops and starts the VM to make the disk appear. A disk attached
// before the first boot is there from the start; if it is not found after a bus rescan the procedure reports it
// and leaves the VM alone.
//
// It only ever touches a disk with no partitions and no filesystem (or one left half-done by a previous attempt,
// which find-blank also reports), and refuses when there is more than one candidate: on a fresh clone the only
// blank disk is the new one, anything else is ambiguous and nothing is written.
func formatAndMountBlank(c agentExecer, node string, vmid int, mount string, sticky bool) error {
	if err := validateMountPoint(mount); err != nil {
		return coldMsg(err.Error())
	}
	useHelper, err := diskTool.probeHelper(c, node, vmid)
	if err != nil {
		return err
	}
	run := func(timeout time.Duration, args ...string) (guestResult, error) {
		return diskTool.run(c, node, vmid, useHelper, args, timeout)
	}
	findBlank := func() ([]string, error) {
		res, err := run(30*time.Second, "find-blank")
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 { // a failing lsblk is an error, not "no disk"
			return nil, coldMsg(fmt.Sprintf("ricerca dischi fallita nel guest (exit %d)", res.ExitCode))
		}
		return strings.Fields(res.Out), nil
	}
	candidates, err := findBlank()
	if err != nil {
		return err
	}
	for attempt := 0; attempt < provDiskScans && len(candidates) == 0; attempt++ {
		_, _ = run(30*time.Second, "rescan") // best effort
		time.Sleep(provTiming.DiskScanWait)
		if candidates, err = findBlank(); err != nil {
			return err
		}
	}
	switch {
	case len(candidates) == 0:
		return coldMsg("il disco non e' comparso nel guest: si formatta dal pannello della VM (Aggiungi disco)")
	case len(candidates) > 1:
		return coldMsg(fmt.Sprintf("%d dischi vuoti nel guest: non si sa quale sia il nuovo, nessuna modifica", len(candidates)))
	}
	perm := "1777"
	if !sticky {
		perm = "0777"
	}
	res, err := run(120*time.Second, "setup", "/dev/"+candidates[0], mount, perm) // mkfs can exceed 30 s
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return coldMsg(fmt.Sprintf("formattazione o montaggio non riusciti (exit %d)", res.ExitCode))
	}
	return nil
}

const provDiskScans = 5
