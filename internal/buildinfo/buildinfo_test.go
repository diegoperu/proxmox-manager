package buildinfo

import (
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

func TestLinkedInfo(t *testing.T) {
	if os.Getenv("PMGR_TEST_LINKED_INFO") != "1" {
		t.Skip("run with build metadata ldflags")
	}
	info := Info()
	if info.Commit != strings.Repeat("c", 40) || info.Short != "ccccccc" || info.Date != "2026-11-20T10:00:00Z" || !info.Dirty || !info.Known {
		t.Fatalf("linked build metadata: %+v", info)
	}
}

func TestInfo(t *testing.T) {
	oldCommit, oldDate, oldDirty, oldRead := Commit, Date, Dirty, readBuildInfo
	t.Cleanup(func() { Commit, Date, Dirty, readBuildInfo = oldCommit, oldDate, oldDirty, oldRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: strings.Repeat("b", 40)},
			{Key: "vcs.time", Value: "2026-11-19T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	}
	Commit, Date, Dirty = strings.Repeat("a", 40), "2026-11-20T10:00:00+01:00", "false"
	if got := Info(); got.Commit != Commit || got.Short != "aaaaaaa" || got.Date != Date || got.Dirty || !got.Known {
		t.Fatalf("explicit ldflags values: %+v", got)
	}
	Commit, Date, Dirty = "", "", ""
	if got := Info(); got.Commit != strings.Repeat("b", 40) || got.Short != "bbbbbbb" || got.Date != "2026-11-19T10:00:00Z" || !got.Dirty || !got.Known {
		t.Fatalf("fallback: %+v", got)
	}
	for _, invalid := range []string{"main", strings.Repeat("A", 40), strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 40) + "\n", "../../main"} {
		Commit, Date, Dirty = invalid, "not a date", "yes"
		if got := Info(); got.Commit != "" || got.Short != "" || got.Date != "" || got.Known || got.Dirty {
			t.Fatalf("invalid metadata: %+v", got)
		}
	}
	Commit, Date, Dirty = "", "", ""
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	if got := Info(); got != (BuildInfo{}) {
		t.Fatalf("missing metadata: %+v", got)
	}
}
