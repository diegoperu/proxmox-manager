package handlers

import (
	"math"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// Manager GPU data owns only memory classes and numbers, never inventory identities.
type v3GPUClass struct {
	Class       string `json:"class"`
	CardsNow    *int   `json:"cards_now"`
	FreeNow     *int   `json:"free_now"`
	AssignedNow *int   `json:"assigned_now"`

	CardDays    *float64 `json:"card_days"`
	BusyPct     *float64 `json:"busy_pct"`
	ReservedPct *float64 `json:"reserved_pct"`
	QuietPct    *float64 `json:"quiet_pct"`
	BusyHours   *float64 `json:"busy_hours"`
	VMOffDays   *float64 `json:"vm_off_days"`
	CoveragePct *float64 `json:"coverage_pct"`
}

func v3GPUMemory(model string, models map[string]config.GPUModel) int {
	if correction, ok := models[model]; ok && correction.MemoryGB > 0 {
		return correction.MemoryGB
	}
	return gpuMemoryGB(model)
}

func v3GPUClassIndex(memory int) int {
	switch {
	case memory <= 0:
		return 4
	case memory <= 16:
		return 0
	case memory <= 24:
		return 1
	case memory <= 48:
		return 2
	default:
		return 3
	}
}

// The inventory adapter expands multi-card mappings into each physical card's Uses
// before calling this function, so one assigned card always contributes exactly once.
func calculateV3GPUClasses(inv *GPUInventory, rows []history.GPUUseDay, historyKnown bool, models map[string]config.GPUModel, from, to time.Time) []v3GPUClass {
	out := []v3GPUClass{{Class: "16"}, {Class: "24"}, {Class: "48"}, {Class: "80+"}, {Class: "unknown"}}
	if inv != nil && len(inv.Partial) == 0 {
		for i := range out {
			out[i].CardsNow, out[i].FreeNow, out[i].AssignedNow = new(int), new(int), new(int)
		}
		for _, node := range inv.Nodes {
			for _, card := range node.Cards {
				model := card.Model
				if model == "" {
					model = card.Name
				}
				memory := v3GPUMemory(model, models)
				row := &out[v3GPUClassIndex(memory)]
				*row.CardsNow++
				if len(card.Uses) > 0 {
					*row.AssignedNow++
				} else {
					*row.FreeNow++
				}
			}
		}
	}
	if !historyKnown {
		return out
	}
	type samples struct{ valid, expected, busy, reserved, observed float64 }
	counts := [5]samples{}
	for i := range out {
		out[i].CardDays, out[i].BusyHours, out[i].VMOffDays = new(float64), new(float64), new(float64)
	}
	first, last := from.Format("2006-01-02"), to.Format("2006-01-02")
	for _, stored := range rows {
		if stored.Day < first || stored.Day > last || stored.Cards <= 0 {
			continue
		}
		class := v3GPUClassIndex(v3GPUMemory(stored.Model, models))
		row, count := &out[class], &counts[class]
		cards := float64(stored.Cards)
		*row.CardDays += cards
		// The collector stores the busiest card's sample for a multi-card VM. Weighting
		// by Cards estimates card usage consistently; it is not a per-card measurement.
		valid := float64(max(0, stored.Valid))
		expected := math.Max(valid, float64(max(0, stored.Expected)))
		busy := math.Min(valid, float64(max(0, stored.BusyMinutes))/5)
		reserved := math.Min(valid-busy, float64(max(0, stored.Reserved)))
		count.valid += valid * cards
		count.expected += expected * cards
		count.observed += (expected + float64(max(0, stored.Off))) * cards
		count.busy += busy * cards
		count.reserved += reserved * cards
		*row.BusyHours += busy * 5 / 60 * cards
		*row.VMOffDays += float64(max(0, stored.Off)) * 5 / 1440 * cards
	}
	for i := range out {
		count, row := counts[i], &out[i]
		if count.valid == 0 {
			row.BusyHours = nil // failed samples cannot establish zero hours of use
		}
		if count.observed == 0 {
			row.VMOffDays = nil // no observations do not prove that VMs were never off
		}
		if count.valid > 0 {
			row.BusyPct = v3GPUNumber(count.busy / count.valid * 100)
			row.ReservedPct = v3GPUNumber(count.reserved / count.valid * 100)
			row.QuietPct = v3GPUNumber(math.Max(0, count.valid-count.busy-count.reserved) / count.valid * 100)
		}
		if count.expected > 0 {
			row.CoveragePct = v3GPUNumber(count.valid / count.expected * 100)
		}
	}
	return out
}

func v3GPUNumber(value float64) *float64 {
	value = round2(value)
	return &value
}
