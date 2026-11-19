package handlers

import (
	"encoding/json"
	"os"
	"testing"

	"proxmox-manager/internal/config"
)

func TestInternalPriceSharedCases(t *testing.T) {
	data, err := os.ReadFile("testdata/internal_price_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Prices config.InternalPrices `json:"prices"`
		Cases  []struct {
			Name   string          `json:"name"`
			Prices json.RawMessage `json:"prices"`
			Guest  struct {
				Cores        int64     `json:"cores"`
				MemGiB       float64   `json:"mem_gib"`
				SSDGB        float64   `json:"ssd_gb"`
				HDDGB        float64   `json:"hdd_gb"`
				GPUMemoryGiB []float64 `json:"gpu_memory_gib"`
			} `json:"guest"`
			EUR    *float64 `json:"eur"`
			GPUEUR *float64 `json:"gpu_eur"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 5 {
		t.Fatal("shared cases missing")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			prices := fixture.Prices
			prices.GPUTiers = append([]config.GPUTier(nil), prices.GPUTiers...)
			if len(tc.Prices) != 0 {
				if err := json.Unmarshal(tc.Prices, &prices); err != nil {
					t.Fatal(err)
				}
			}
			if err := config.ValidReportSettings(&config.ReportSettings{InternalPrices: prices}); err != nil {
				t.Fatal(err)
			}
			g := tc.Guest
			got, gpu := v3InternalEconomic(prices, []v3EconomicGuest{{
				Cores: g.Cores, MemBytes: int64(g.MemGiB * (1 << 30)),
				SSDGB: g.SSDGB, HDDGB: g.HDDGB, GPUMemoryGiB: g.GPUMemoryGiB,
			}})
			for _, pair := range []struct {
				label     string
				got, want *float64
			}{{"EUR", got, tc.EUR}, {"GPU EUR", gpu, tc.GPUEUR}} {
				if pair.got == nil || pair.want == nil {
					if (pair.got == nil) != (pair.want == nil) {
						t.Fatalf("%s: unknown mismatch: got %v, want %v", pair.label, pair.got, pair.want)
					}
				} else if *pair.got != *pair.want {
					t.Errorf("%s: got %g, want %g", pair.label, *pair.got, *pair.want)
				}
			}
		})
	}
}
