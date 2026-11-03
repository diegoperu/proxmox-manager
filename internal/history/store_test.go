package history

import (
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sample(day string) Snapshot {
	return Snapshot{Day: day, Cluster: "CALCOLO", TakenAt: time.Unix(1000, 0), GPUKnown: true,
		Nodes:  []NodeRow{{Node: "n1", Online: true, Cores: 64, MemBytes: 1 << 40}},
		Groups: []GroupRow{{Node: "n1", Group: "rossi", Kind: "qemu", VMs: 3, Running: 2, Cores: 12, MemBytes: 1 << 36, DiskBytes: 1 << 38}},
		GPUs:   []GPURow{{Node: "n1", Model: "A100", Group: "rossi", Cards: 2}, {Node: "n1", Model: "A100", Group: "", Cards: 1}}}
}

func TestSaveLoadRoundTripAndReplace(t *testing.T) {
	s := newStore(t)
	if ok, _ := s.Has("2026-11-03", "CALCOLO"); ok {
		t.Fatal("empty store")
	}
	if err := s.Save(sample("2026-11-03")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Has("2026-11-03", "CALCOLO"); !ok || err != nil {
		t.Fatal("has")
	}
	got, ok, err := s.Load("2026-11-03", "CALCOLO")
	if err != nil || !ok || len(got.Nodes) != 1 || len(got.Groups) != 1 || len(got.GPUs) != 2 || !got.GPUKnown || got.Groups[0].Group != "rossi" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	// saving again replaces, never duplicates
	again := sample("2026-11-03")
	again.GPUKnown, again.GPUs = false, nil
	if err := s.Save(again); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Load("2026-11-03", "CALCOLO")
	if len(got.GPUs) != 0 || got.GPUKnown || len(got.Groups) != 1 {
		t.Fatalf("%+v", got)
	}
	if _, ok, _ := s.Load("2026-11-04", "CALCOLO"); ok {
		t.Fatal("other day must be absent")
	}
}

func TestSaveRejectsBadKeys(t *testing.T) {
	s := newStore(t)
	for _, c := range []struct{ day, cl string }{{"3/11/2026", "CALCOLO"}, {"2026-11-03", ""}, {"2026-11-03", "bad\nlabel"}, {"2026-11-03'; DROP", "x"}} {
		sn := sample(c.day)
		sn.Cluster = c.cl
		if err := s.Save(sn); err == nil {
			t.Fatalf("%v accepted", c)
		}
	}
}

func TestPruneKeepsFiveYears(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
	old := now.Add(-Retention - 48*time.Hour).Format("2006-01-02")
	recent := now.Add(-Retention + 48*time.Hour).Format("2006-01-02")
	for _, d := range []string{old, recent, "2026-11-03"} {
		if err := s.Save(sample(d)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(old, "CALCOLO"); ok {
		t.Fatal("old not pruned")
	}
	if _, ok, _ := s.Load(old, "CALCOLO"); ok {
		t.Fatal("old rows remain")
	}
	for _, d := range []string{recent, "2026-11-03"} {
		if ok, _ := s.Has(d, "CALCOLO"); !ok {
			t.Fatalf("%s pruned", d)
		}
	}
}
