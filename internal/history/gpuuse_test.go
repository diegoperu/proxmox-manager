package history

import (
	"path/filepath"
	"testing"
	"time"
)

func gpuRow(day string, vmid int) GPUUseDay {
	return GPUUseDay{Day: day, Cluster: "CALCOLO", VMID: vmid, Node: "node-1", Group: "rossi", Model: "NVIDIA A100",
		Cards: 1, Expected: 10, Valid: 8, Off: 2, NoData: 2, BusyMinutes: 15, UtilAvg: 12.5, UtilP95: 80, UtilMax: 90,
		MemAvgPct: 30, MemMaxPct: 50, TempMax: 70, PowerAvg: 120}
}

func TestGPUUseUpsertQueryPrune(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertGPUUse([]GPUUseDay{gpuRow("2026-11-18", 101), gpuRow("2026-11-19", 101), gpuRow("2026-11-19", 102)}); err != nil {
		t.Fatal(err)
	}
	upd := gpuRow("2026-11-19", 101)
	upd.Expected, upd.Valid, upd.UtilMax, upd.TempMax, upd.PowerAvg = 20, 19, 100, -1, -1
	if err := s.UpsertGPUUse([]GPUUseDay{upd}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GPUUseDays("CALCOLO", "2026-11-19", "2026-11-19")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].VMID != 101 || rows[0].Expected != 20 || rows[0].Valid != 19 || rows[0].UtilMax != 100 || rows[0].TempMax != -1 {
		t.Fatalf("upsert not applied: %+v", rows)
	}
	if other, _ := s.GPUUseDays("HOSTING", "2026-01-01", "2026-12-31"); len(other) != 0 {
		t.Fatalf("other cluster leaked: %+v", other)
	}
	if err := s.PruneGPUUse(time.Date(2027, 12, 20, 12, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.GPUUseDays("CALCOLO", "2026-01-01", "2027-12-31"); len(left) != 2 {
		t.Fatalf("prune: want the 2 rows of 2026-11-19 (inside 396 days), got %d", len(left))
	}
	if err := s.PruneGPUUse(time.Date(2027, 12, 21, 12, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.GPUUseDays("CALCOLO", "2026-01-01", "2027-12-31"); len(left) != 0 {
		t.Fatalf("prune: want 0 rows, got %d", len(left))
	}
}

func TestGPUUseValidation(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bad := []func(*GPUUseDay){
		func(r *GPUUseDay) { r.Day = "2026-13-1" },
		func(r *GPUUseDay) { r.Cluster = "a\nb" },
		func(r *GPUUseDay) { r.VMID = 0 },
		func(r *GPUUseDay) { r.Node = "node 1;drop" },
		func(r *GPUUseDay) { r.Group = "Rossi" },
		func(r *GPUUseDay) { r.Model = "A100 è" },
		func(r *GPUUseDay) { r.Valid = r.Expected + 1 },
		func(r *GPUUseDay) { r.UtilAvg = 101 },
		func(r *GPUUseDay) { r.MemMaxPct = -1 },
		func(r *GPUUseDay) { r.TempMax = 500 },
		func(r *GPUUseDay) { r.BusyMinutes = 24*60 + 1 },
	}
	for i, f := range bad {
		r := gpuRow("2026-11-19", 300+i)
		f(&r)
		if err := s.UpsertGPUUse([]GPUUseDay{gpuRow("2026-11-19", 200+i), r}); err == nil {
			t.Errorf("case %d: invalid row accepted", i)
		}
	}
	// a rejected batch writes nothing
	if rows, _ := s.GPUUseDays("CALCOLO", "2026-01-01", "2026-12-31"); len(rows) != 0 {
		t.Fatalf("partial write: %+v", rows)
	}
	if _, err := s.GPUUseDays("CALCOLO", "x", "2026-12-31"); err == nil {
		t.Error("bad query accepted")
	}
}
