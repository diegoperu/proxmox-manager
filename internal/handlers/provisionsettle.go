package handlers

import (
	"strings"
	"time"
)

// settleScript: read-only probe run in the guest through the agent. Prints "<boot id> <state>".
// state = finished when cloud-init has completed its final stage IN THIS BOOT (the marker file
// is newer than PID 1), or when cloud-init is disabled / not installed; pending otherwise.
// `read` is a builtin: on SELinux guests the agent domain cannot run most binaries.
//
// Why: the template snippets restart qemu-guest-agent and end with a reboot. The agent answers
// as soon as the package is installed, long before that: a command started in that window is
// killed by the agent restart or by the reboot (seen on pittorino-gpu-2: user created, sudo
// never granted). Only builtins and `cloud-init status` (read only) are used. ASCII only.
const settleScript = `b=noboot
read b < /proc/sys/kernel/random/boot_id 2>/dev/null || b=noboot
f=pending
m=/var/lib/cloud/instance/boot-finished
if [ -e "$m" ] && [ "$m" -nt /proc/1 ]; then
  f=finished
elif [ ! -d /var/lib/cloud ]; then
  f=finished
elif command -v cloud-init >/dev/null 2>&1; then
  s=$(cloud-init status 2>/dev/null | sed -n 's/^status: *//p' | head -n 1)
  case "$s" in disabled|"not run") f=finished ;; esac
fi
echo "$b $f"
`

// settleStable: number of consecutive polls that must report "finished" on the same boot id.
const settleStable = 2

// parseSettle returns the boot id and whether the guest reported "finished". Anything else
// (empty output, unknown words, extra lines) counts as not finished: never guess.
func parseSettle(out string) (boot string, finished bool) {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 2 || len(f[0]) > 64 {
		return "", false
	}
	return f[0], f[1] == "finished"
}

// waitGuestSettled waits until cloud-init has finished in the current boot (and the guest did
// not reboot between two polls). Agent errors are normal while the guest reboots: they only
// mean "not yet". Returns false when limit passes first; the caller must then NOT run
// anything in the guest.
func waitGuestSettled(c agentExecer, node string, vmid int, limit, poll time.Duration) bool {
	end := time.Now().Add(limit)
	lastBoot, streak := "", 0
	for {
		res, err := runGuestScript(c, node, vmid, settleScript, nil, 15*time.Second)
		if err == nil && res.ExitCode == 0 {
			if boot, ok := parseSettle(res.Out); ok && boot == lastBoot {
				streak++
			} else if ok {
				lastBoot, streak = boot, 1
			} else {
				lastBoot, streak = "", 0
			}
			if streak >= settleStable {
				return true
			}
		} else {
			lastBoot, streak = "", 0
		}
		if !time.Now().Before(end) {
			return false
		}
		time.Sleep(poll)
	}
}
