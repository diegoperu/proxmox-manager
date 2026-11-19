package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// ReportSettings belongs to one cluster. Zero prices disable its economic section.
type ReportSettings struct {
	InternalPrices InternalPrices    `json:"internal_prices"`
	MarketPrices   []MarketPrice     `json:"market_prices"`
	UsdEur         float64           `json:"usd_eur"`
	StorageClasses map[string]string `json:"storage_classes"`
	Defaults       ReportDefaults    `json:"defaults"`
	Interruptions  []Interruption    `json:"interruptions"`
}

// UnmarshalJSON also rejects unknown report fields when reading config.json, while
// leaving the surrounding legacy config format compatible with older installations.
func (r *ReportSettings) UnmarshalJSON(data []byte) error {
	type reportJSON ReportSettings
	var next reportJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return err
	}
	*r = ReportSettings(next)
	return nil
}

type InternalPrices struct {
	TokenEUR    float64 `json:"token_eur"`
	GPUTokenEUR float64 `json:"gpu_token_eur"`
	BaseTokens  float64 `json:"base_tokens"`
	BaseCores   int     `json:"base_cores"`
	BaseMemGiB  float64 `json:"base_mem_gib"`
	BaseSSDGB   float64 `json:"base_ssd_gb"`
	// Absent block sizes use the built-in size; explicit zero is invalid.
	CoresBlock       *int      `json:"cores_block,omitempty"`
	CoresBlockTokens float64   `json:"cores_block_tokens"`
	MemBlockGiB      *float64  `json:"mem_block_gib,omitempty"`
	MemBlockTokens   float64   `json:"mem_block_tokens"`
	SSDBlockGB       *float64  `json:"ssd_block_gb,omitempty"`
	SSDBlockTokens   float64   `json:"ssd_block_tokens"`
	HDDBlockGB       *float64  `json:"hdd_block_gb,omitempty"`
	HDDBlockTokens   float64   `json:"hdd_block_tokens"`
	GPUTiers         []GPUTier `json:"gpu_tiers"`
}

// GPUTier is an inclusive lower bound. The highest matching tier wins.
type GPUTier struct {
	MinGiB float64 `json:"min_gib"`
	Tokens float64 `json:"tokens"`
}
type GPUPrice struct {
	MinGiB float64 `json:"min_gib"`
	Price  float64 `json:"price"`
}
type MarketPrice struct {
	Name           string     `json:"name"`
	Level          string     `json:"level"`
	Currency       string     `json:"currency"`
	VCPUHour       float64    `json:"vcpu_hour"`
	RAMGBHour      float64    `json:"ram_gb_hour"`
	StorageGBMonth float64    `json:"storage_gb_month"`
	GPUHour        []GPUPrice `json:"gpu_hour"`
	Source         string     `json:"source"`
	Date           string     `json:"date"`
}

// Zero/absent thresholds preserve the report's built-in default.
type ReportDefaults struct {
	InactiveCPU   float64 `json:"inactive_cpu"`
	InactiveNet   float64 `json:"inactive_net"`
	InactiveDays  float64 `json:"inactive_days"`
	OversizedMem  float64 `json:"oversized_mem"`
	OversizedCPU  float64 `json:"oversized_cpu"`
	OversizedDays float64 `json:"oversized_days"`
	IdleUtil      float64 `json:"idle_util"`
	IdleMem       float64 `json:"idle_mem"`
	IdleDays      int     `json:"idle_days"`
	AbandonMonths int     `json:"abandon_months"`
	AbandonPeak   float64 `json:"abandon_peak"`
	AbandonCPU    float64 `json:"abandon_cpu"`
	AbandonNet    float64 `json:"abandon_net"`
	OverMem       float64 `json:"over_mem"`
	OverCPU       float64 `json:"over_cpu"`
	Margin        float64 `json:"margin"`
}
type Interruption struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Cause string `json:"cause"`
	Note  string `json:"note"`
}

func (r *ReportSettings) EconomicEnabled() bool {
	if r == nil {
		return false
	}
	p := r.InternalPrices
	if p.TokenEUR != 0 || p.GPUTokenEUR != 0 || p.BaseTokens != 0 || p.CoresBlockTokens != 0 || p.MemBlockTokens != 0 || p.SSDBlockTokens != 0 || p.HDDBlockTokens != 0 {
		return true
	}
	for _, t := range p.GPUTiers {
		if t.Tokens != 0 {
			return true
		}
	}
	for _, p := range r.MarketPrices {
		if p.VCPUHour != 0 || p.RAMGBHour != 0 || p.StorageGBMonth != 0 {
			return true
		}
		for _, t := range p.GPUHour {
			if t.Price != 0 {
				return true
			}
		}
	}
	return false
}

var reportStorageName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func reportRange(field string, value, lo, hi float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < lo || value > hi {
		return fmt.Errorf("%s: valore non valido (da %g a %g)", field, lo, hi)
	}
	return nil
}

var reportRFC3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)
var reportLocalTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)

// ReportTime accepts an explicit RFC3339 offset or the server's local wall clock.
func ReportTime(s string) (time.Time, error) {
	if reportLocalTime.MatchString(s) {
		t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local)
		if err != nil || t.Format("2006-01-02T15:04") != s {
			return time.Time{}, errors.New("data locale non valida")
		}
		return t, nil
	}
	if !reportRFC3339.MatchString(s) {
		return time.Time{}, errors.New("data non valida")
	}
	return time.Parse(time.RFC3339, s)
}

// ValidReportSettings checks every persisted value without including its contents in errors.
func ValidReportSettings(r *ReportSettings) error {
	if r == nil {
		return nil
	}
	for _, err := range []error{
		validInternalPrices(r.InternalPrices), validMarketPrices(r.MarketPrices),
		reportRange("usd_eur", r.UsdEur, 0, 10), validStorageClasses(r.StorageClasses),
		validReportDefaults(r.Defaults), validInterruptions(r.Interruptions),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

func validInternalPrices(p InternalPrices) error {
	for _, f := range []struct {
		name      string
		v, lo, hi float64
	}{
		{"internal_prices.token_eur", p.TokenEUR, 0, 1000}, {"internal_prices.gpu_token_eur", p.GPUTokenEUR, 0, 100000},
		{"internal_prices.base_tokens", p.BaseTokens, 0, 1000}, {"internal_prices.base_cores", float64(p.BaseCores), 0, 4096},
		{"internal_prices.base_mem_gib", p.BaseMemGiB, 0, 65536}, {"internal_prices.base_ssd_gb", p.BaseSSDGB, 0, 1e7},
		{"internal_prices.cores_block_tokens", p.CoresBlockTokens, 0, 1000}, {"internal_prices.mem_block_tokens", p.MemBlockTokens, 0, 1000},
		{"internal_prices.ssd_block_tokens", p.SSDBlockTokens, 0, 1000}, {"internal_prices.hdd_block_tokens", p.HDDBlockTokens, 0, 1000},
	} {
		if err := reportRange(f.name, f.v, f.lo, f.hi); err != nil {
			return err
		}
	}
	if p.CoresBlock != nil {
		if err := reportRange("internal_prices.cores_block", float64(*p.CoresBlock), 1, 4096); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		name string
		v    *float64
		hi   float64
	}{
		{"internal_prices.mem_block_gib", p.MemBlockGiB, 65536}, {"internal_prices.ssd_block_gb", p.SSDBlockGB, 1e7}, {"internal_prices.hdd_block_gb", p.HDDBlockGB, 1e8},
	} {
		if f.v != nil {
			if err := reportRange(f.name, *f.v, 1, f.hi); err != nil {
				return err
			}
		}
	}
	if len(p.GPUTiers) > 8 {
		return errors.New("internal_prices.gpu_tiers: massimo 8 fasce")
	}
	for i, t := range p.GPUTiers {
		field := fmt.Sprintf("internal_prices.gpu_tiers[%d]", i)
		if err := reportRange(field+".min_gib", t.MinGiB, 0, 1024); err != nil {
			return err
		}
		if err := reportRange(field+".tokens", t.Tokens, 0, 1000); err != nil {
			return err
		}
		if i > 0 && t.MinGiB <= p.GPUTiers[i-1].MinGiB {
			return errors.New("internal_prices.gpu_tiers: min_gib deve essere strettamente crescente")
		}
	}
	return nil
}

func validMarketPrices(prices []MarketPrice) error {
	if len(prices) > 6 {
		return errors.New("market_prices: massimo 6 listini")
	}
	for i, m := range prices {
		f := fmt.Sprintf("market_prices[%d]", i)
		if strings.TrimSpace(m.Name) == "" || !printableText(m.Name, 64) {
			return fmt.Errorf("%s.name: testo semplice richiesto, massimo 64 caratteri", f)
		}
		if m.Level != "low" && m.Level != "high" {
			return fmt.Errorf("%s.level: scegliere low o high", f)
		}
		if m.Currency != "EUR" && m.Currency != "USD" {
			return fmt.Errorf("%s.currency: scegliere EUR o USD", f)
		}
		if !printableText(m.Source, 300) {
			return fmt.Errorf("%s.source: testo semplice, massimo 300 caratteri", f)
		}
		if len(m.Date) != 10 {
			return fmt.Errorf("%s.date: data non valida (AAAA-MM-GG)", f)
		}
		if _, err := time.Parse("2006-01-02", m.Date); err != nil {
			return fmt.Errorf("%s.date: data non valida (AAAA-MM-GG)", f)
		}
		for _, v := range []struct {
			name string
			v    float64
		}{{"vcpu_hour", m.VCPUHour}, {"ram_gb_hour", m.RAMGBHour}, {"storage_gb_month", m.StorageGBMonth}} {
			if err := reportRange(f+"."+v.name, v.v, 0, 1000); err != nil {
				return err
			}
		}
		if len(m.GPUHour) > 8 {
			return fmt.Errorf("%s.gpu_hour: massimo 8 fasce", f)
		}
		for j, t := range m.GPUHour {
			tf := fmt.Sprintf("%s.gpu_hour[%d]", f, j)
			if err := reportRange(tf+".min_gib", t.MinGiB, 0, 1024); err != nil {
				return err
			}
			if err := reportRange(tf+".price", t.Price, 0, 1000); err != nil {
				return err
			}
			if j > 0 && t.MinGiB <= m.GPUHour[j-1].MinGiB {
				return fmt.Errorf("%s.gpu_hour: min_gib deve essere strettamente crescente", f)
			}
		}
	}
	return nil
}

func validStorageClasses(classes map[string]string) error {
	if len(classes) > 64 {
		return errors.New("storage_classes: massimo 64 storage")
	}
	for k, v := range classes {
		if !reportStorageName.MatchString(k) {
			return errors.New("storage_classes: nome storage non valido (1-64 caratteri: lettere, cifre, punto, trattino, underscore)")
		}
		if v != "ssd" && v != "hdd" && v != "other" {
			return errors.New("storage_classes: scegliere ssd, hdd o other")
		}
	}
	return nil
}

func validReportDefaults(d ReportDefaults) error {
	for _, f := range []struct {
		name      string
		v, lo, hi float64
	}{
		{"inactive_cpu", d.InactiveCPU, 0, .5}, {"inactive_net", d.InactiveNet, 0, 1e9}, {"inactive_days", d.InactiveDays, 1, 488},
		{"oversized_mem", d.OversizedMem, 0, 1}, {"oversized_cpu", d.OversizedCPU, 0, 1}, {"oversized_days", d.OversizedDays, 1, 488},
		{"idle_util", d.IdleUtil, 0, 50}, {"idle_mem", d.IdleMem, 0, 50}, {"idle_days", float64(d.IdleDays), 7, 60},
		{"abandon_months", float64(d.AbandonMonths), 1, 12}, {"abandon_peak", d.AbandonPeak, 1, 90}, {"abandon_cpu", d.AbandonCPU, 0, 50},
		{"abandon_net", d.AbandonNet, 0, 1048576}, {"over_mem", d.OverMem, 1, 90}, {"over_cpu", d.OverCPU, 1, 90}, {"margin", d.Margin, 0, 200},
	} {
		if f.v != 0 {
			if err := reportRange("defaults."+f.name, f.v, f.lo, f.hi); err != nil {
				return err
			}
		}
	}
	return nil
}

func validInterruptions(rows []Interruption) error {
	if len(rows) > 500 {
		return errors.New("interruptions: massimo 500 interruzioni")
	}
	for i, v := range rows {
		f := fmt.Sprintf("interruptions[%d]", i)
		from, err := ReportTime(v.From)
		if err != nil {
			return fmt.Errorf("%s.from: data non valida (RFC3339 o AAAA-MM-GGTHH:MM locale)", f)
		}
		to, err := ReportTime(v.To)
		if err != nil {
			return fmt.Errorf("%s.to: data non valida (RFC3339 o AAAA-MM-GGTHH:MM locale)", f)
		}
		if !to.After(from) || to.Sub(from) > 31*24*time.Hour {
			return fmt.Errorf("%s.to: deve seguire from, durata massima 31 giorni", f)
		}
		if v.Cause != "external" && v.Cause != "maintenance" && v.Cause != "fault" {
			return fmt.Errorf("%s.cause: scegliere external, maintenance o fault", f)
		}
		if !printableText(v.Note, 200) {
			return fmt.Errorf("%s.note: testo semplice, massimo 200 caratteri", f)
		}
	}
	return nil
}

func copyReport(r *ReportSettings) *ReportSettings {
	if r == nil {
		return nil
	}
	c := *r
	p := &c.InternalPrices
	if p.CoresBlock != nil {
		v := *p.CoresBlock
		p.CoresBlock = &v
	}
	if p.MemBlockGiB != nil {
		v := *p.MemBlockGiB
		p.MemBlockGiB = &v
	}
	if p.SSDBlockGB != nil {
		v := *p.SSDBlockGB
		p.SSDBlockGB = &v
	}
	if p.HDDBlockGB != nil {
		v := *p.HDDBlockGB
		p.HDDBlockGB = &v
	}
	p.GPUTiers = append([]GPUTier(nil), p.GPUTiers...)
	c.MarketPrices = append([]MarketPrice(nil), r.MarketPrices...)
	for i := range c.MarketPrices {
		c.MarketPrices[i].GPUHour = append([]GPUPrice(nil), r.MarketPrices[i].GPUHour...)
	}
	if r.StorageClasses != nil {
		c.StorageClasses = make(map[string]string, len(r.StorageClasses))
		for k, v := range r.StorageClasses {
			c.StorageClasses[k] = v
		}
	}
	c.Interruptions = append([]Interruption(nil), r.Interruptions...)
	return &c
}

var ErrReportCluster = errors.New("cluster non trovato")

// SetReportSettings replaces only this cluster's report under the configuration lock.
// A failed write restores the previous in-memory settings. Logs contain section names only.
func SetReportSettings(idx int, r *ReportSettings) error {
	if err := ValidReportSettings(r); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if idx < 0 || idx >= len(current.Clusters) {
		return ErrReportCluster
	}
	old := current.Clusters[idx].Report
	current.Clusters[idx].Report = copyReport(r)
	if err := save(); err != nil {
		current.Clusters[idx].Report = old
		return err
	}
	before := ReportSettings{}
	if old != nil {
		before = *old
	}
	after := ReportSettings{}
	if r != nil {
		after = *r
	}
	var sections []string
	a, b := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < a.NumField(); i++ {
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			sections = append(sections, a.Type().Field(i).Tag.Get("json"))
		}
	}
	if len(sections) > 0 {
		logReportChange(current.Clusters[idx].Label, sections)
	}
	return nil
}

func logReportChange(label string, sections []string) {
	log.Printf("report cluster %q: sezioni modificate: %s", label, strings.Join(sections, ", "))
}
