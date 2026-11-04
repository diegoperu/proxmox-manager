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

func TestSharedVMsKeepClusterTotalsAndMilliShares(t *testing.T) {
	s := newStore(t)
	snap := Snapshot{Day: "2026-11-04", Cluster: "C", TakenAt: time.Unix(1, 0), GPUKnown: true, GroupRule: RuleTags,
		HasTotals: true, TotalVMs: 3, TotalRunning: 2, SharedVMs: 1,
		Nodes: []NodeRow{{Node: "n1", Online: true, Cores: 64, MemBytes: 1 << 40}},
		Groups: []GroupRow{
			{Node: "n1", Group: "a", Kind: "qemu", VMs: 2, Running: 2, SharedVMs: 1, CoresMilli: 4500, MemBytes: 6, DiskBytes: 10},
			{Node: "n1", Group: "b", Kind: "qemu", VMs: 2, Running: 1, SharedVMs: 1, CoresMilli: 3500, MemBytes: 4, DiskBytes: 10}},
		GPUs: []GPURow{{Node: "n1", Model: "A100", Group: "a", CardsMilli: 500}, {Node: "n1", Model: "A100", Group: "b", CardsMilli: 500},
			{Node: "n1", Model: "A100", Group: "", CardsMilli: 1000}}}
	if err := s.Save(snap); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load("2026-11-04", "C")
	if err != nil || !ok || got.GroupRule != RuleTags || !got.HasTotals || got.TotalVMs != 3 || got.SharedVMs != 1 || got.Groups[0].CoresMilli != 4500 || got.Groups[0].Cores != 4 || got.GPUs[0].CardsMilli == 0 {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	d, err := s.Days("C", "2026-11-04", "2026-11-04")
	if err != nil || len(d) != 1 {
		t.Fatal(err, d)
	}
	// 3 VMs (not 2+2), 8 cores (4500+3500), 2 cards of which 1 assigned (500+500)
	if d[0].VMs != 3 || d[0].Running != 2 || d[0].SharedVMs != 1 || d[0].AllocCores != 8 || d[0].AllocMem != 10 || d[0].GPUTotal != 2 || d[0].GPUAssigned != 1 || d[0].GroupRule != RuleTags {
		t.Fatalf("%+v", d[0])
	}
}

func TestMigrationKeepsOldRowsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// a database as created before the tag rule (no new columns)
	old, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP TABLE inv_meta`, `DROP TABLE inv_group`, `DROP TABLE inv_gpu`,
		`CREATE TABLE inv_meta (day TEXT NOT NULL, cluster TEXT NOT NULL, taken_at INTEGER NOT NULL, gpu_known INTEGER NOT NULL, PRIMARY KEY (day, cluster))`,
		`CREATE TABLE inv_group (day TEXT NOT NULL, cluster TEXT NOT NULL, node TEXT NOT NULL, grp TEXT NOT NULL, kind TEXT NOT NULL, vms INTEGER NOT NULL, running INTEGER NOT NULL, cores INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, disk_bytes INTEGER NOT NULL, PRIMARY KEY (day, cluster, node, grp, kind))`,
		`CREATE TABLE inv_gpu (day TEXT NOT NULL, cluster TEXT NOT NULL, node TEXT NOT NULL, model TEXT NOT NULL, grp TEXT NOT NULL, cards INTEGER NOT NULL, PRIMARY KEY (day, cluster, node, model, grp))`,
		`INSERT INTO inv_meta VALUES ('2026-11-03','C',1,1)`,
		`INSERT INTO inv_group VALUES ('2026-11-03','C','n1','rossi','qemu',3,2,12,100,200)`,
		`INSERT INTO inv_gpu VALUES ('2026-11-03','C','n1','A100','rossi',2)`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	old.Close()
	for i := 0; i < 2; i++ { // the migration is idempotent
		s, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.Load("2026-11-03", "C")
		if err != nil || !ok || got.GroupRule != RuleName || got.HasTotals || got.Groups[0].Cores != 12 || got.Groups[0].CoresMilli != 12000 || got.GPUs[0].Cards != 2 {
			t.Fatalf("%+v %v %v", got, ok, err)
		}
		d, _ := s.Days("C", "2026-11-03", "2026-11-03")
		if len(d) != 1 || d[0].VMs != 3 || d[0].AllocCores != 12 || d[0].GPUTotal != 2 || d[0].GroupRule != RuleName {
			t.Fatalf("%+v", d)
		}
		s.Close()
	}
}

func TestInvalidGroupRuleRejected(t *testing.T) {
	s := newStore(t)
	snap := sample("2026-11-04")
	snap.GroupRule = "x'; DROP TABLE inv_meta;--"
	if err := s.Save(snap); err == nil {
		t.Fatal("rule outside the whitelist accepted")
	}
}
