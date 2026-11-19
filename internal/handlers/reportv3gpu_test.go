package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

func TestV3GPUMemoryClasses(t *testing.T) {
	models := map[string]config.GPUModel{"removed-custom": {MemoryGB: 48}}
	for _, tt := range []struct {
		model string
		class int
	}{
		{"NVIDIA [A2]", 0}, {"NVIDIA 16GB", 0}, {"NVIDIA 17GB", 1}, {"NVIDIA [L4]", 1},
		{"NVIDIA 24GB", 1}, {"NVIDIA 25GB", 2}, {"NVIDIA 48GB", 2}, {"NVIDIA 49GB", 3},
		{"NVIDIA [RTX PRO 6000 Blackwell]", 3}, {"NVIDIA 89GB", 3}, {"NVIDIA 80GB", 3},
		{"removed-custom", 2}, {"unrecognised", 4},
	} {
		if got := v3GPUClassIndex(v3GPUMemory(tt.model, models)); got != tt.class {
			t.Errorf("%q: got %d, want %d", tt.model, got, tt.class)
		}
	}
	models["NVIDIA 16GB"] = config.GPUModel{MemoryGB: 96}
	if got := v3GPUMemory("NVIDIA 16GB", models); got != 96 {
		t.Fatalf("correction must override size in model: %d", got)
	}
}

func TestV3GPUClassesRetainRemovedCardHistory(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0).Add(-time.Second)
	inv := &GPUInventory{Nodes: []GPUNode{{Node: "distinct-node", Cards: []GPUCard{
		{Name: "NVIDIA [A2]", Uses: []GPUUse{{VMID: 999777, Name: "distinct-vm"}}},
		{Name: "NVIDIA [L4]"},
	}}}}
	rows := []history.GPUUseDay{
		{Day: "2026-10-01", VMID: 999777, Node: "distinct-node", Group: "distinct-group", Inst: "distinct-instance", Model: "removed-model", Cards: 2, Expected: 120, Valid: 60, Reserved: 12, BusyMinutes: 120, Off: 144},
		{Day: "2026-10-02", Model: "removed-model", Cards: 1, Expected: 60, Valid: 60, Reserved: 18, BusyMinutes: 60},
		{Day: "2026-09-30", Model: "removed-model", Cards: 10, Expected: 60, Valid: 60, BusyMinutes: 300},
		{Day: "2026-11-01", Model: "removed-model", Cards: 10, Expected: 60, Valid: 60, BusyMinutes: 300},
	}
	result := calculateV3GPUClasses(inv, rows, true, map[string]config.GPUModel{"removed-model": {MemoryGB: 48}}, from, to)
	if len(result) != 5 || *result[0].CardsNow != 1 || *result[0].AssignedNow != 1 || *result[1].FreeNow != 1 {
		t.Fatalf("inventory counts: %+v", result)
	}
	removed := result[2]
	if *removed.CardsNow != 0 || *removed.CardDays != 3 || *removed.BusyHours != 5 || *removed.VMOffDays != 1 {
		t.Fatalf("removed card history lost: %+v", removed)
	}
	if *removed.BusyPct != 33.33 || *removed.ReservedPct != 23.33 || *removed.QuietPct != 43.33 || *removed.CoveragePct != 60 {
		t.Fatalf("sample weighting wrong: busy=%v reserved=%v quiet=%v coverage=%v", *removed.BusyPct, *removed.ReservedPct, *removed.QuietPct, *removed.CoveragePct)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"distinct-node", "distinct-vm", "distinct-group", "distinct-instance", "999777", "removed-model"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("identity leaked: %s", forbidden)
		}
	}
}

func TestV3GPUClassesUnknownAndEmpty(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	inv := &GPUInventory{Nodes: []GPUNode{{Cards: []GPUCard{{Name: "unknown card"}}}}}
	rows := []history.GPUUseDay{{Day: "2026-10-01", Model: "unknown card", Cards: 1, Expected: 100, Off: 100}}
	result := calculateV3GPUClasses(inv, rows, true, nil, from, from.AddDate(0, 0, 1))
	unknown := result[4]
	if *unknown.CardsNow != 1 || *unknown.CardDays != 1 || unknown.BusyPct != nil || unknown.QuietPct != nil || unknown.ReservedPct != nil || *unknown.CoveragePct != 0 {
		t.Fatalf("unknown/no valid samples: %+v", unknown)
	}
	if *result[0].CardDays != 0 || result[0].CoveragePct != nil || result[0].BusyPct != nil {
		t.Fatalf("known empty history must have zero count and unknown rates: %+v", result[0])
	}
	if unknown.BusyHours != nil {
		t.Fatal("failed samples cannot establish zero busy hours")
	}
	if result[0].BusyHours != nil || result[0].VMOffDays != nil {
		t.Fatal("no observations cannot establish zero busy/off duration")
	}
	result = calculateV3GPUClasses(nil, nil, false, nil, from, from)
	for _, class := range result {
		if class.CardsNow != nil || class.CardDays != nil || class.BusyHours != nil || class.CoveragePct != nil {
			t.Fatalf("unreadable data must be null: %+v", class)
		}
	}
	inv.Partial = []string{"unreadable-node"}
	result = calculateV3GPUClasses(inv, nil, true, nil, from, from)
	if result[4].CardsNow != nil {
		t.Fatal("partial inventory must not claim complete counts")
	}
}
