package handlers

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Excerpt of the real CRUSH map of the CALCOLO cluster (2026-11-04), plus a rule on a host bucket.
const crushSample = `# begin crush map
tunable choose_total_tries 50
device 0 osd.0 class ssd
host node-1 {
	id -3		# do not change unnecessarily
	item osd.0 weight 1.0
}
# rules
rule replicated_rule {
	id 0
	type replicated
	step take default
	step chooseleaf firstn 0 type host
	step emit
}
rule replicated_ssd {
	id 1
	step take default class ssd   # comment
	step emit
}
rule replicated_hdd {
	id 2
	step take default class hdd
	step emit
}
rule only_node2 {
	id 3
	step take node-2
	step emit
}
# end crush map
`

func TestParseCrushRules(t *testing.T) {
	r := parseCrushRules(crushSample)
	want := map[string][]crushTake{
		"replicated_rule": {{Bucket: "default"}},
		"replicated_ssd":  {{Bucket: "default", Class: "ssd"}},
		"replicated_hdd":  {{Bucket: "default", Class: "hdd"}},
		"only_node2":      {{Bucket: "node-2"}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("got %#v", r)
	}
}

func TestParseCrushRulesRejectsMalformed(t *testing.T) {
	text := "rule bad$name {\n step take default\n}\n" +
		"rule ok {\n step take de;fault\n step take default class ss d\n step take default class $x\n step take root2 class nvme\n}\n" +
		"rule ok {\n step take other\n}\n" + // duplicate: the first one wins
		"step take outside\n"
	r := parseCrushRules(text)
	want := map[string][]crushTake{"ok": {{Bucket: "root2", Class: "nvme"}}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("got %#v", r)
	}
	if len(parseCrushRules(strings.Repeat("x", crushMaxText+1))) != 0 {
		t.Fatal("oversized map must be ignored")
	}
	many := strings.Builder{}
	for i := 0; i < crushMaxRules+10; i++ {
		many.WriteString("rule r" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + itoaTest(i) + " {\n")
		for j := 0; j < crushMaxTakes+3; j++ {
			many.WriteString(" step take default\n")
		}
		many.WriteString("}\n")
	}
	rr := parseCrushRules(many.String())
	if len(rr) != crushMaxRules {
		t.Fatalf("rules not capped: %d", len(rr))
	}
	for _, tk := range rr {
		if len(tk) != crushMaxTakes {
			t.Fatalf("takes not capped: %d", len(tk))
		}
	}
}

func itoaTest(i int) string {
	return strings.TrimLeft(string([]byte{byte('0' + i/100), byte('0' + i/10%10), byte('0' + i%10)}), "")
}

func sampleOSDs() []cephOSD {
	mk := func(id int, host, class string, used, total int64) cephOSD {
		return cephOSD{ID: id, Name: "osd." + itoaTest(id), Host: host, Class: class, Used: used, Total: total,
			Pools: []string{}, buckets: []string{"default", host}}
	}
	return []cephOSD{mk(0, "node-1", "ssd", 50, 100), mk(1, "node-1", "hdd", 10, 100), mk(2, "node-2", "ssd", 30, 100), mk(3, "node-2", "hdd", 40, 100)}
}

func TestMapPoolsToOSDsByClass(t *testing.T) {
	osds := sampleOSDs()
	pools := []cephPool{{Name: "fast", Rule: "replicated_ssd"}, {Name: "slow", Rule: "replicated_hdd"}, {Name: ".mgr", Rule: "replicated_ssd"}, {Name: "lost", Rule: "missing"}}
	groups := mapPoolsToOSDs(osds, pools, parseCrushRules(crushSample))
	if len(groups) != 2 {
		t.Fatalf("groups %#v", groups)
	}
	hdd, ssd := groups[0], groups[1] // ordered by class
	if !reflect.DeepEqual(hdd.Classes, []string{"hdd"}) || !reflect.DeepEqual(hdd.Pools, []string{"slow"}) || hdd.OSDs != 2 || hdd.Percent != 25 || hdd.Key != "g1" {
		t.Fatalf("hdd %#v", hdd)
	}
	if !reflect.DeepEqual(ssd.Pools, []string{".mgr", "fast"}) || ssd.OSDs != 2 || ssd.Percent != 40 || ssd.Key != "g2" {
		t.Fatalf("ssd %#v", ssd)
	}
	if osds[0].Group != "g2" || osds[1].Group != "g1" || !reflect.DeepEqual(osds[0].Pools, []string{".mgr", "fast"}) {
		t.Fatalf("osd %#v", osds[0])
	}
	if !pools[0].Mapped || pools[0].OSDs != 2 || !reflect.DeepEqual(pools[0].Classes, []string{"ssd"}) || pools[0].AllClasses {
		t.Fatalf("pool %#v", pools[0])
	}
	if pools[3].Mapped || pools[3].OSDs != 0 || pools[3].Classes == nil {
		t.Fatalf("unknown rule must stay unmapped: %#v", pools[3])
	}
}

func TestMapPoolsToOSDsAllClassesAndHostBucket(t *testing.T) {
	osds := sampleOSDs()
	pools := []cephPool{{Name: "all", Rule: "replicated_rule"}, {Name: "n2", Rule: "only_node2"}}
	groups := mapPoolsToOSDs(osds, pools, parseCrushRules(crushSample))
	if !pools[0].AllClasses || pools[0].OSDs != 4 || !reflect.DeepEqual(pools[0].Classes, []string{"hdd", "ssd"}) {
		t.Fatalf("all %#v", pools[0])
	}
	if pools[1].OSDs != 2 || pools[1].AllClasses != true {
		t.Fatalf("host bucket %#v", pools[1])
	}
	// node-1 OSDs serve {all}, node-2 OSDs serve {all,n2}: two groups, mixed classes each
	if len(groups) != 2 || groups[0].OSDs != 2 || len(groups[0].Classes) != 2 {
		t.Fatalf("groups %#v", groups)
	}
}

func TestFetchCephFullMapsPools(t *testing.T) {
	f := &fakeCeph{crush: "rule r {\n step take default\n}\n"}
	full, err := fetchCephFull(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// the fake OSD tree has no named root, so "default" matches nothing: the pool stays unmapped
	if len(full.Pools) != 1 || full.Pools[0].Mapped || containsStr(full.Partial, "crush") {
		t.Fatalf("%#v %v", full.Pools, full.Partial)
	}
	f = &fakeCeph{}
	full, _ = fetchCephFull(f, time.Now())
	if !containsStr(full.Partial, "crush") || len(full.Groups) != 0 || full.Groups == nil {
		t.Fatalf("unreadable map must be partial, groups empty: %v %#v", full.Partial, full.Groups)
	}
}
