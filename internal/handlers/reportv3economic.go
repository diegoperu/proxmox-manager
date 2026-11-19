package handlers

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"proxmox-manager/internal/config"
)

// These inputs contain only quantities. Inventory identities never cross this
// boundary; negative quantities and nil hours mean unavailable measurements.
type v3EconomicGuest struct {
	Cores, MemBytes       int64
	SSDGB, HDDGB, OtherGB float64
	Hours                 *float64
	GPUMemoryGiB          []float64
}

type v3MarketBreakdown struct {
	Compute *float64 `json:"compute"`
	GPU     *float64 `json:"gpu"`
	Storage *float64 `json:"storage"`
}

type v3Economic struct {
	InternalEURMonth    *float64 `json:"internal_eur_month"`
	InternalGPUEURMonth *float64 `json:"internal_gpu_eur_month"`
	LowEURMonth         *float64 `json:"low_eur_month"`
	HighEURMonth        *float64 `json:"high_eur_month"`
	Breakdown           struct {
		Low  *v3MarketBreakdown `json:"low"`
		High *v3MarketBreakdown `json:"high"`
	} `json:"breakdown"`
	PricesDate string   `json:"prices_date"`
	Sources    []string `json:"sources"`
	DaysUsed   int      `json:"days_used"`
}

type v3EconomicAmount struct {
	value   float64
	unknown bool
}

func v3EconomicValid(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (a *v3EconomicAmount) add(value, price float64) {
	if price == 0 {
		return
	}
	if !v3EconomicValid(value) || !v3EconomicValid(price) || !v3EconomicValid(value*price) {
		a.unknown = true
		return
	}
	a.value += value * price
	if !v3EconomicValid(a.value) {
		a.unknown = true
	}
}

func (a v3EconomicAmount) number() *float64 {
	if a.unknown || !v3EconomicValid(a.value) || a.value > math.MaxFloat64/100 {
		return nil
	}
	v := a.value
	return &v
}

func v3EconomicMoney(value *float64) *float64 {
	if value == nil {
		return nil
	}
	v := round2(*value)
	return &v
}

func v3EconomicTotal(values ...*float64) *float64 {
	a := v3EconomicAmount{}
	for _, value := range values {
		if value == nil {
			return nil
		}
		a.add(*value, 1)
	}
	return a.number()
}

func v3EconomicBlock(value, base, size float64) float64 {
	if !v3EconomicValid(value) || !v3EconomicValid(base) || size <= 0 || !v3EconomicValid(size) {
		return -1
	}
	return math.Ceil(math.Max(0, value-base) / size)
}

func v3EconomicBlockSize(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func v3InternalGPUTokens(memory float64, tiers []config.GPUTier) float64 {
	if !v3EconomicValid(memory) {
		return -1
	}
	best, result := -1.0, -1.0
	for _, tier := range tiers {
		if memory >= tier.MinGiB && tier.MinGiB > best {
			best, result = tier.MinGiB, tier.Tokens
		}
	}
	return result
}

func v3MarketGPUPrice(memory float64, tiers []config.GPUPrice) float64 {
	if !v3EconomicValid(memory) {
		return -1
	}
	best, result := -1.0, -1.0
	for _, tier := range tiers {
		if memory >= tier.MinGiB && tier.MinGiB > best {
			best, result = tier.MinGiB, tier.Price
		}
	}
	return result
}

func v3InternalEconomic(p config.InternalPrices, guests []v3EconomicGuest) (*float64, *float64) {
	compute, gpu := v3EconomicAmount{}, v3EconomicAmount{}
	if p.TokenEUR == 0 && len(guests) != 0 {
		compute.unknown = true // this cluster has no internal VM tariff
	}
	coresBlock := 16.0
	if p.CoresBlock != nil {
		coresBlock = float64(*p.CoresBlock)
	}
	for _, g := range guests {
		tokens := v3EconomicAmount{}
		tokens.add(p.BaseTokens, 1)
		tokens.add(v3EconomicBlock(float64(g.Cores), float64(p.BaseCores), coresBlock), p.CoresBlockTokens)
		tokens.add(v3EconomicBlock(float64(g.MemBytes)/(1<<30), p.BaseMemGiB, v3EconomicBlockSize(p.MemBlockGiB, 32)), p.MemBlockTokens)
		tokens.add(v3EconomicBlock(g.SSDGB, p.BaseSSDGB, v3EconomicBlockSize(p.SSDBlockGB, 100)), p.SSDBlockTokens)
		tokens.add(v3EconomicBlock(g.HDDGB, 0, v3EconomicBlockSize(p.HDDBlockGB, 1000)), p.HDDBlockTokens)
		if tokens.unknown && p.TokenEUR != 0 {
			compute.unknown = true
		} else {
			compute.add(tokens.value, p.TokenEUR)
		}
		for _, memory := range g.GPUMemoryGiB {
			if p.GPUTokenEUR == 0 || len(p.GPUTiers) == 0 {
				gpu.unknown = true
				continue
			}
			priced := false
			for _, tier := range p.GPUTiers {
				priced = priced || tier.Tokens != 0
			}
			if !priced {
				continue
			}
			gpu.add(v3InternalGPUTokens(memory, p.GPUTiers), p.GPUTokenEUR)
		}
	}
	return v3EconomicMoney(v3EconomicTotal(compute.number(), gpu.number())), v3EconomicMoney(gpu.number())
}

// A provider entry is evaluated independently before averaging its level. This
// preserves GPU tier semantics when providers have different tier boundaries.
func v3ProviderEconomic(p config.MarketPrice, guests []v3EconomicGuest, usdEUR float64) v3MarketBreakdown {
	compute, gpu, storage := v3EconomicAmount{}, v3EconomicAmount{}, v3EconomicAmount{}
	factor := 1.0
	if p.Currency == "USD" {
		factor = usdEUR
		if factor == 0 {
			factor = -1 // an absent conversion rate is not a free USD price
		}
	} else if p.Currency != "EUR" {
		factor = -1
	}
	for _, g := range guests {
		hours := -1.0
		if g.Hours != nil {
			hours = *g.Hours
		}
		compute.add(v3EconomicProduct(float64(g.Cores), hours), p.VCPUHour*factor)
		compute.add(v3EconomicProduct(float64(g.MemBytes)/(1<<30), hours), p.RAMGBHour*factor)
		storage.add(g.SSDGB, p.StorageGBMonth*factor)
		storage.add(g.HDDGB, p.StorageGBMonth*factor)
		storage.add(g.OtherGB, p.StorageGBMonth*factor)
		for _, memory := range g.GPUMemoryGiB {
			priced := false
			for _, tier := range p.GPUHour {
				priced = priced || tier.Price != 0
			}
			if len(p.GPUHour) != 0 && !priced {
				continue
			}
			price := v3MarketGPUPrice(memory, p.GPUHour)
			if price < 0 {
				gpu.unknown = true
				continue
			}
			gpu.add(hours, price*factor)
		}
	}
	return v3MarketBreakdown{Compute: compute.number(), GPU: gpu.number(), Storage: storage.number()}
}

func v3EconomicProduct(a, b float64) float64 {
	if !v3EconomicValid(a) || !v3EconomicValid(b) {
		return -1
	}
	return a * b
}

func v3EconomicAverage(values []*float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	a := v3EconomicAmount{}
	for _, value := range values {
		if value == nil {
			return nil
		}
		a.add(*value, 1/float64(len(values)))
	}
	return a.number()
}

func calculateV3Economic(settings *config.ReportSettings, guests []v3EconomicGuest, daysUsed int) *v3Economic {
	if !settings.EconomicEnabled() {
		return nil
	}
	out := &v3Economic{Sources: []string{}, DaysUsed: daysUsed}
	out.InternalEURMonth, out.InternalGPUEURMonth = v3InternalEconomic(settings.InternalPrices, guests)
	byLevel := map[string][]v3MarketBreakdown{}
	sources := map[string]bool{}
	for _, price := range settings.MarketPrices {
		if price.Level != "low" && price.Level != "high" {
			continue
		}
		byLevel[price.Level] = append(byLevel[price.Level], v3ProviderEconomic(price, guests, settings.UsdEur))
		if price.Date > out.PricesDate {
			out.PricesDate = price.Date
		}
		if price.Name != "" {
			sources[price.Name] = true
		}
	}
	for name := range sources {
		out.Sources = append(out.Sources, name)
	}
	sort.Strings(out.Sources)
	for _, level := range []string{"low", "high"} {
		entries := byLevel[level]
		if len(entries) == 0 {
			continue
		}
		compute, gpu, storage, totals := []*float64{}, []*float64{}, []*float64{}, []*float64{}
		for _, entry := range entries {
			compute, gpu, storage = append(compute, entry.Compute), append(gpu, entry.GPU), append(storage, entry.Storage)
			totals = append(totals, v3EconomicTotal(entry.Compute, entry.GPU, entry.Storage))
		}
		b := &v3MarketBreakdown{Compute: v3EconomicMoney(v3EconomicAverage(compute)), GPU: v3EconomicMoney(v3EconomicAverage(gpu)), Storage: v3EconomicMoney(v3EconomicAverage(storage))}
		total := v3EconomicMoney(v3EconomicAverage(totals))
		if level == "low" {
			out.Breakdown.Low, out.LowEURMonth = b, total
		} else {
			out.Breakdown.High, out.HighEURMonth = b, total
		}
	}
	return out
}

var v3DiskKey = regexp.MustCompile(`^(?:scsi|virtio|sata|ide)[0-9]{1,3}$`)
var v3DiskSize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([KMGTPE]?)$`)

// PVE's size=G uses binary units. The report retains that convention when
// comparing configured disk capacities with its storage GB price list.
func parseV3Disks(cfg map[string]interface{}, classes map[string]string) (ssd, hdd, other float64, known bool) {
	known = cfg != nil
	for key, raw := range cfg {
		if !v3DiskKey.MatchString(key) {
			continue
		}
		text, ok := raw.(string)
		if !ok || len(text) > 4096 {
			known = false
			continue
		}
		parts := strings.Split(text, ",")
		cdrom, size, sizes := false, "", 0
		for _, part := range parts[1:] {
			if part == "media=cdrom" {
				cdrom = true
			}
			if strings.HasPrefix(part, "size=") {
				size = strings.TrimPrefix(part, "size=")
				sizes++
			}
		}
		if cdrom {
			continue
		}
		m := v3DiskSize.FindStringSubmatch(size)
		if sizes != 1 || len(m) != 3 {
			known = false
			continue
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			known = false
			continue
		}
		factor := map[string]float64{"": 1.0 / (1 << 30), "K": 1.0 / (1 << 20), "M": 1.0 / 1024, "G": 1, "T": 1024, "P": 1 << 20, "E": 1 << 30}[m[2]]
		gb := n * factor
		if !v3EconomicValid(gb) || gb > 1e9 {
			known = false
			continue
		}
		storage, _, _ := strings.Cut(parts[0], ":")
		switch classes[storage] {
		case "ssd":
			ssd += gb
		case "hdd":
			hdd += gb
		default:
			other += gb
		}
	}
	return
}
