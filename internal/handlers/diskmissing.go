package handlers

import (
	"fmt"
	"strings"
)

type diskCommandsError string

func (e diskCommandsError) Error() string { return string(e) }

func diskMissingError(stderr string) diskCommandsError {
	if message, ok := diskMissingMessage(stderr); ok {
		return diskCommandsError(message)
	}
	return diskCommandsError("nella VM mancano comandi necessari per configurare il disco")
}

func diskMissingMessage(stderr string) (string, bool) {
	packages := map[string][2]string{
		"parted": {"parted", "parted"}, "mkfs.ext4": {"e2fsprogs", "e2fsprogs"},
		"blkid": {"util-linux", "util-linux"}, "lsblk": {"util-linux", "util-linux"},
		"mount": {"mount", "util-linux"}, "mountpoint": {"util-linux", "util-linux"},
		"findmnt": {"util-linux", "util-linux"}, "awk": {"gawk", "gawk"},
		"wc": {"coreutils", "coreutils"},
	}
	seen := make(map[string]bool)
	var messages []string
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "MISSING ") {
			continue
		}
		cmd := strings.TrimPrefix(line, "MISSING ")
		pkg, ok := packages[cmd]
		if !ok || seen[cmd] {
			continue
		}
		seen[cmd] = true
		if pkg[0] == pkg[1] {
			messages = append(messages, fmt.Sprintf("il comando %s: installa il pacchetto %s (apt install %s / dnf install %s)", cmd, pkg[0], pkg[0], pkg[1]))
		} else {
			messages = append(messages, fmt.Sprintf("il comando %s: installa il pacchetto %s (apt install %s) / %s (dnf install %s)", cmd, pkg[0], pkg[0], pkg[1], pkg[1]))
		}
	}
	if len(messages) == 0 {
		return "", false
	}
	return "nella VM manca " + strings.Join(messages, "; ") + " e riprova", true
}
