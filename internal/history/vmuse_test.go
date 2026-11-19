package history

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func vmRow(day string, vmid int, inst string) VMUseDay {
	return VMUseDay{Day: day, Cluster: "CALCOLO", VMID: vmid, Inst: inst, Kind: "qemu", Name: "rossi-vm-1", Group: "rossi",
		Node: "pve1", Cores: 4, MemBytes: 4 << 30, DiskBytes: 32 << 30, Points: 48, StepS: 1800,
		CPUAvg: 10, CPUP95: 20, CPUMax: 30, MemAvg: 40, MemP95: 50, MemMax: 60, NetIn: 1, NetOut: 2, DiskRead: 3, DiskWrite: 4}
}

const u1 = "u:11111111-1111-1111-1111-111111111111"

func TestVMUseSaveValidatePrune(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	if err := s.SaveVMUseDay("2026-11-19", "CALCOLO", []VMUseDay{vmRow("2026-11-19", 101, u1), vmRow("2026-11-19", 102, "l:2026-11-01")}, now); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasVMUseDay("2026-11-19", "CALCOLO"); !has {
		t.Fatal("day not marked")
	}
	if first, _ := s.VMUseFirstDay("CALCOLO"); first != "2026-11-19" {
		t.Fatalf("first day %q", first)
	}
	// replacing a day keeps only the new rows
	if err := s.SaveVMUseDay("2026-11-19", "CALCOLO", []VMUseDay{vmRow("2026-11-19", 101, u1)}, now); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.VMUseDays("CALCOLO", "2026-11-19", "2026-11-19"); len(rows) != 1 {
		t.Fatalf("replace: %d rows", len(rows))
	}
	if last, ok, _ := s.VMUseLastBefore("CALCOLO", 101, "qemu", "2026-11-20"); !ok || last.Inst != u1 {
		t.Fatalf("last before: %+v %v", last, ok)
	}
	bad := []func(*VMUseDay){
		func(r *VMUseDay) { r.Inst = "123" },
		func(r *VMUseDay) { r.Inst = "u:XYZ" },
		func(r *VMUseDay) { r.Kind = "vm" },
		func(r *VMUseDay) { r.Name = "nome\ncon a capo" },
		func(r *VMUseDay) { r.CPUAvg = 150 },
		func(r *VMUseDay) { r.MemMax = -2 },
		func(r *VMUseDay) { r.NetIn = -1 },
		func(r *VMUseDay) { r.Day = "2026-11-18" }, // row of another day
	}
	for i, f := range bad {
		r := vmRow("2026-11-18", 200+i, u1)
		r.Day = "2026-11-18"
		f(&r)
		if err := s.SaveVMUseDay("2026-11-17", "CALCOLO", []VMUseDay{r}, now); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if has, _ := s.HasVMUseDay("2026-11-17", "CALCOLO"); has {
		t.Fatal("a rejected day was marked as taken")
	}
	if err := s.PruneVMUse(now.Add(VMUseRetention + 48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasVMUseDay("2026-11-19", "CALCOLO"); has {
		t.Fatal("prune kept the day mark")
	}
}

// A database created before D121 (gpu_use_day without inst) is rebuilt with its rows kept (inst "").
func TestGPUUseMigrationAddsInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gpu_use_day (
		day TEXT NOT NULL, cluster TEXT NOT NULL, vmid INTEGER NOT NULL,
		node TEXT NOT NULL, grp TEXT NOT NULL, model TEXT NOT NULL, cards INTEGER NOT NULL,
		expected INTEGER NOT NULL, valid INTEGER NOT NULL, off INTEGER NOT NULL,
		no_driver INTEGER NOT NULL, driver_error INTEGER NOT NULL, no_data INTEGER NOT NULL,
		busy_minutes INTEGER NOT NULL, util_avg REAL NOT NULL, util_p95 REAL NOT NULL, util_max REAL NOT NULL,
		mem_avg_pct REAL NOT NULL, mem_max_pct REAL NOT NULL, temp_max REAL NOT NULL, power_avg REAL NOT NULL,
		PRIMARY KEY (day, cluster, vmid));
		INSERT INTO gpu_use_day VALUES ('2026-11-19','CALCOLO',202,'pve1','ict','A100',1,10,9,0,0,0,1,15,20,80,90,30,40,65,200);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.GPUUseDays("CALCOLO", "2026-11-19", "2026-11-19")
	if err != nil || len(rows) != 1 || rows[0].Inst != "" || rows[0].Reserved != 0 || rows[0].UtilMax != 90 {
		t.Fatalf("old row after migration: %+v %v", rows, err)
	}
	// the same VMID with an instance is now a separate row
	r := rows[0]
	r.Inst = "u:22222222-2222-2222-2222-222222222222"
	if err := s.UpsertGPUUse([]GPUUseDay{r}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.GPUUseDays("CALCOLO", "2026-11-19", "2026-11-19"); len(rows) != 2 {
		t.Fatalf("want 2 rows (legacy + instance), got %d", len(rows))
	}
	// opening again does not rebuild or lose anything
	s.Close()
	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if rows, _ := s2.GPUUseDays("CALCOLO", "2026-11-19", "2026-11-19"); len(rows) != 2 {
		t.Fatalf("second open: %d rows", len(rows))
	}
}
