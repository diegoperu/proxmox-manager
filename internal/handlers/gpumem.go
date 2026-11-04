package handlers

import (
	"regexp"
	"strconv"
	"strings"
)

// Video memory of a card, to group the cards in the new-VM form (ROADMAP 3b, D60). Proxmox does not report it:
// it comes from the model name ("... 40GB" / "... 94GB" in the name) or, when the name has no size, from this
// table, which the user confirmed on 2026-11-04 (A2/A16 cards are A2). 0 = unknown: the card goes in the group
// "memory not known" and the page says so; nothing is guessed.

var gbInName = regexp.MustCompile(`(?i)\b([0-9]{1,3})\s?GB\b`)

var bracketName = regexp.MustCompile(`\[([^\[\]]{1,120})\]\s*$`)

// gpuMemTable: model (the text between the last [ ] of the PCI device name) -> GB.
var gpuMemTable = []struct {
	match func(string) bool
	gb    int
}{
	{func(m string) bool { return m == "A2 / A16" || m == "A2" || m == "A16" }, 16},
	{func(m string) bool { return m == "Tesla T4" || m == "T4" }, 16},
	{func(m string) bool { return m == "A10" }, 24},
	{func(m string) bool { return m == "L4" }, 24},
	{func(m string) bool { return strings.HasPrefix(m, "RTX PRO 6000") }, 96},
}

func gpuMemoryGB(name string) int {
	if m := gbInName.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= 512 {
			return n
		}
	}
	if m := bracketName.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		model := strings.TrimSpace(m[1])
		for _, e := range gpuMemTable {
			if e.match(model) {
				return e.gb
			}
		}
	}
	return 0
}
