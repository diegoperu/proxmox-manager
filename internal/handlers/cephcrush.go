package handlers

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Which OSDs serve which pool. A pool names a CRUSH rule; the rule's "step take <bucket> [class <c>]" lines say
// where its data goes: every OSD under that bucket (root, host, ...) of that device class (any class if none).
// Proxmox gives the rules only inside the decompiled CRUSH map (/nodes/{n}/ceph/crush, plain text): only the
// "rule" blocks and their "step take" lines are read, everything else is ignored.

const (
	crushMaxText  = 1 << 20 // the map of a large cluster is a few hundred KB
	crushMaxRules = 256
	crushMaxTakes = 8
)

var crushName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type crushTake struct {
	Bucket string
	Class  string // "" = any device class
}

// parseCrushRules returns rule name -> takes. Malformed or oversized parts are skipped, never guessed.
func parseCrushRules(text string) map[string][]crushTake {
	out := map[string][]crushTake{}
	if len(text) > crushMaxText {
		return out
	}
	cur := ""
	in := false
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case !in && f[0] == "rule" && len(f) == 3 && f[2] == "{":
			in = true
			cur = ""
			if crushName.MatchString(f[1]) && len(out) < crushMaxRules {
				if _, dup := out[f[1]]; !dup {
					cur = f[1]
					out[cur] = []crushTake{}
				}
			}
		case in && f[0] == "}":
			in = false
			cur = ""
		case in && cur != "" && f[0] == "step" && len(f) >= 3 && f[1] == "take":
			t := crushTake{Bucket: f[2]}
			if len(f) == 5 && f[3] == "class" {
				t.Class = f[4]
			} else if len(f) != 3 {
				continue
			}
			if !crushName.MatchString(t.Bucket) || (t.Class != "" && !crushName.MatchString(t.Class)) {
				continue
			}
			if len(out[cur]) < crushMaxTakes {
				out[cur] = append(out[cur], t)
			}
		}
	}
	return out
}

// cephOSDGroup is a set of OSDs serving exactly the same pools (in practice: one per device class).
type cephOSDGroup struct {
	Key     string   `json:"key"`
	Classes []string `json:"classes"`
	Pools   []string `json:"pools"`
	OSDs    int      `json:"osds"`
	Used    int64    `json:"used"`
	Total   int64    `json:"total"`
	Percent float64  `json:"percent"`
}

// mapPoolsToOSDs fills osd.Pools/Group, pool.OSDs/Classes/Mapped/AllClasses and returns the groups.
// A pool whose rule is unknown or whose buckets match no OSD stays Mapped=false: the page says so.
func mapPoolsToOSDs(osds []cephOSD, pools []cephPool, rules map[string][]crushTake) []cephOSDGroup {
	for pi := range pools {
		p := &pools[pi]
		p.Classes = []string{}
		takes, ok := rules[p.Rule]
		if !ok || len(takes) == 0 {
			continue
		}
		classes := map[string]bool{}
		for oi := range osds {
			o := &osds[oi]
			if !osdMatches(o, takes) {
				continue
			}
			p.OSDs++
			o.Pools = append(o.Pools, p.Name)
			if o.Class != "" {
				classes[o.Class] = true
			}
		}
		p.Mapped = p.OSDs > 0
		for _, t := range takes {
			if t.Class == "" {
				p.AllClasses = true
			}
		}
		for c := range classes {
			p.Classes = append(p.Classes, c)
		}
		sort.Strings(p.Classes)
	}
	groups := []cephOSDGroup{}
	idx := map[string]int{}
	for oi := range osds {
		o := &osds[oi]
		if o.Pools == nil {
			o.Pools = []string{}
		}
		sort.Strings(o.Pools)
		sig := strings.Join(o.Pools, "\x00")
		gi, ok := idx[sig]
		if !ok {
			gi = len(groups)
			idx[sig] = gi
			groups = append(groups, cephOSDGroup{Classes: []string{}, Pools: o.Pools})
		}
		g := &groups[gi]
		g.OSDs++
		g.Used += o.Used
		g.Total += o.Total
		if o.Class != "" && !containsStr(g.Classes, o.Class) {
			g.Classes = append(g.Classes, o.Class)
		}
		o.group = gi
	}
	// stable order: by class, then by pools; keys follow the order
	order := make([]int, len(groups))
	for i := range order {
		order[i] = i
	}
	for i := range groups {
		sort.Strings(groups[i].Classes)
	}
	sort.SliceStable(order, func(a, b int) bool {
		ga, gb := groups[order[a]], groups[order[b]]
		ca, cb := strings.Join(ga.Classes, ","), strings.Join(gb.Classes, ",")
		if ca != cb {
			return ca < cb
		}
		return strings.Join(ga.Pools, ",") < strings.Join(gb.Pools, ",")
	})
	key := make([]string, len(groups))
	out := make([]cephOSDGroup, 0, len(groups))
	for n, gi := range order {
		g := groups[gi]
		g.Key = "g" + strconv.Itoa(n+1)
		if g.Total > 0 {
			g.Percent = round1v(float64(g.Used) / float64(g.Total) * 100)
		}
		key[gi] = g.Key
		out = append(out, g)
	}
	for oi := range osds {
		osds[oi].Group = key[osds[oi].group]
	}
	return out
}

func osdMatches(o *cephOSD, takes []crushTake) bool {
	for _, t := range takes {
		if t.Class != "" && t.Class != o.Class {
			continue
		}
		if t.Bucket == o.Name || containsStr(o.buckets, t.Bucket) {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
