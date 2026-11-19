package handlers

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"proxmox-manager/internal/config"
)

func v3EconomicTestNumber(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > .000001 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestV3EconomicInternalBlocksAndGPUTiers(t *testing.T) {
	settings := &config.ReportSettings{InternalPrices: config.InternalPrices{
		TokenEUR: 1, GPUTokenEUR: 35, BaseTokens: 8, BaseCores: 16, BaseMemGiB: 32, BaseSSDGB: 100,
		CoresBlockTokens: 1, MemBlockTokens: 4, SSDBlockTokens: 3, HDDBlockTokens: 2,
		// Unsorted inputs still choose the highest inclusive threshold.
		GPUTiers: []config.GPUTier{{MinGiB: 89, Tokens: 11}, {MinGiB: 0, Tokens: 1}, {MinGiB: 24, Tokens: 3}, {MinGiB: 40, Tokens: 10}},
	}}
	guests := []v3EconomicGuest{{Cores: 20, MemBytes: 33 << 30, SSDGB: 101, HDDGB: 1001, OtherGB: 100000,
		GPUMemoryGiB: []float64{16, 24, 48, 80, 89, 96}}}
	rep := calculateV3Economic(settings, guests, 0)
	// VM: 8 + 1 + 4 + 3 + 4 = 20. GPU: (1+3+10+10+11+11)*35 = 1610.
	v3EconomicTestNumber(t, rep.InternalGPUEURMonth, 1610)
	v3EconomicTestNumber(t, rep.InternalEURMonth, 1630)
	if rep.LowEURMonth != nil || rep.HighEURMonth != nil {
		t.Fatal("missing market levels must remain unknown")
	}
	cores, mem, ssd, hdd := 4, 8.0, 10.0, 500.0
	settings.InternalPrices.CoresBlock, settings.InternalPrices.MemBlockGiB = &cores, &mem
	settings.InternalPrices.SSDBlockGB, settings.InternalPrices.HDDBlockGB = &ssd, &hdd
	guests[0].Cores, guests[0].MemBytes, guests[0].SSDGB, guests[0].HDDGB = 21, 41<<30, 111, 1001
	// Custom blocks: 8 + 2 + 8 + 6 + 6 = 30.
	v3EconomicTestNumber(t, calculateV3Economic(settings, guests, 0).InternalEURMonth, 1640)
}

func TestV3EconomicMarketConversionAndLevelAverage(t *testing.T) {
	settings := &config.ReportSettings{UsdEur: .8, MarketPrices: []config.MarketPrice{
		{Name: "provider-b", Level: "low", Currency: "USD", VCPUHour: 2, RAMGBHour: 1, StorageGBMonth: .5,
			GPUHour: []config.GPUPrice{{MinGiB: 0, Price: 2}, {MinGiB: 89, Price: 4}}, Date: "2026-11-01"},
		{Name: "provider-a", Level: "low", Currency: "EUR", VCPUHour: 1, RAMGBHour: .5, StorageGBMonth: .2,
			GPUHour: []config.GPUPrice{{MinGiB: 89, Price: 3}, {MinGiB: 0, Price: 1}}, Date: "2026-11-19"},
		{Name: "provider-b", Level: "high", Currency: "EUR", VCPUHour: 3, RAMGBHour: 2, StorageGBMonth: 1,
			GPUHour: []config.GPUPrice{{MinGiB: 0, Price: 5}}, Date: "2026-11-18"},
	}}
	hours := 10.0
	guests := []v3EconomicGuest{{Cores: 2, MemBytes: 4 << 30, SSDGB: 100, HDDGB: 10, OtherGB: 40, Hours: &hours, GPUMemoryGiB: []float64{89}}}
	rep := calculateV3Economic(settings, guests, 10)
	// USD: compute 80*.8=64, GPU 40*.8=32, storage 75*.8=60. EUR: 40,30,30.
	v3EconomicTestNumber(t, rep.Breakdown.Low.Compute, 52)
	v3EconomicTestNumber(t, rep.Breakdown.Low.GPU, 31)
	v3EconomicTestNumber(t, rep.Breakdown.Low.Storage, 45)
	v3EconomicTestNumber(t, rep.LowEURMonth, 128)
	v3EconomicTestNumber(t, rep.HighEURMonth, 340)
	if rep.InternalEURMonth != nil || rep.InternalGPUEURMonth != nil || rep.PricesDate != "2026-11-19" || rep.DaysUsed != 10 {
		t.Fatalf("incorrect metadata/internal price: %+v", rep)
	}
	if strings.Join(rep.Sources, ",") != "provider-a,provider-b" {
		t.Fatalf("sources = %v", rep.Sources)
	}
}

func TestV3EconomicUnknownDataAndDisabledPrices(t *testing.T) {
	for _, settings := range []*config.ReportSettings{nil, {}, {MarketPrices: []config.MarketPrice{{Level: "low", Currency: "EUR"}}}} {
		if calculateV3Economic(settings, nil, 0) != nil {
			t.Fatal("zero prices must omit economic data")
		}
	}
	hours := 10.0
	settings := &config.ReportSettings{InternalPrices: config.InternalPrices{TokenEUR: 1, BaseTokens: 8, SSDBlockTokens: 3}, MarketPrices: []config.MarketPrice{{
		Name: "provider", Level: "low", Currency: "EUR", VCPUHour: 1, StorageGBMonth: 1,
		GPUHour: []config.GPUPrice{{MinGiB: 0, Price: 2}},
	}}}
	guest := v3EconomicGuest{Cores: 2, MemBytes: 4 << 30, SSDGB: 100, Hours: nil, GPUMemoryGiB: []float64{-1}}
	rep := calculateV3Economic(settings, []v3EconomicGuest{guest}, 0)
	if rep.LowEURMonth != nil || rep.Breakdown.Low.Compute != nil || rep.Breakdown.Low.GPU != nil {
		t.Fatal("unknown running hours/card memory must not become zero")
	}
	v3EconomicTestNumber(t, rep.Breakdown.Low.Storage, 100)
	guest.Hours = &hours
	guest.GPUMemoryGiB = nil
	rep = calculateV3Economic(settings, []v3EconomicGuest{guest}, 1)
	v3EconomicTestNumber(t, rep.LowEURMonth, 120)
	v3EconomicTestNumber(t, rep.InternalGPUEURMonth, 0)
	guest.SSDGB = -1
	rep = calculateV3Economic(settings, []v3EconomicGuest{guest}, 1)
	if rep.InternalEURMonth != nil || rep.Breakdown.Low.Storage != nil || rep.LowEURMonth != nil {
		t.Fatal("unreadable disks must not become free disks")
	}
	settings.MarketPrices[0].Currency = "USD"
	guest.SSDGB = 100
	rep = calculateV3Economic(settings, []v3EconomicGuest{guest}, 1)
	if rep.LowEURMonth != nil {
		t.Fatal("USD without exchange rate must be unknown")
	}
	if raw, err := json.Marshal(rep); err != nil || strings.Contains(string(raw), "NaN") {
		t.Fatalf("invalid JSON economic output: %s, %v", raw, err)
	}
}

func TestV3EconomicAverageBeforeRounding(t *testing.T) {
	hours := 1.0
	rep := calculateV3Economic(&config.ReportSettings{MarketPrices: []config.MarketPrice{
		{Level: "low", Currency: "EUR", VCPUHour: .0049},
		{Level: "low", Currency: "EUR", VCPUHour: .0051},
	}}, []v3EconomicGuest{{Cores: 1, Hours: &hours}}, 1)
	v3EconomicTestNumber(t, rep.LowEURMonth, .01)
}

func TestV3EconomicDiskParsing(t *testing.T) {
	cfg := map[string]interface{}{
		"scsi0":   "fast:vm-100-disk-0,size=100G",
		"virtio1": "slow:vm-100-disk-1,size=1T",
		"sata2":   "other:disk,size=1024M",
		"ide1":    "none,media=cdrom",
		"scsi3":   "unmapped:disk,size=1073741824",
		"scsi4":   "fast:disk,size=1048576K",
		"unused0": "fast:disk,size=100G",
		"scsihw":  "virtio-scsi-single",
	}
	ssd, hdd, other, known := parseV3Disks(cfg, map[string]string{"fast": "ssd", "slow": "hdd", "other": "other"})
	if !known || ssd != 101 || hdd != 1024 || other != 2 {
		t.Fatalf("disk totals = %v, %v, %v, %v", ssd, hdd, other, known)
	}
	for _, bad := range []interface{}{"fast:disk", "fast:disk,size=NaNG", "fast:disk,size=1G,size=2G", "fast:disk,size=1e999G", 42} {
		if _, _, _, known := parseV3Disks(map[string]interface{}{"scsi0": bad}, nil); known {
			t.Fatalf("invalid disk accepted: %v", bad)
		}
	}
	if _, _, _, known := parseV3Disks(nil, nil); known {
		t.Fatal("unreadable config treated as empty disks")
	}
}
