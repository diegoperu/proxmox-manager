package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func sampleReport() *ReportSettings {
	cores := 16
	mem, ssd, hdd := 32.0, 100.0, 1000.0
	return &ReportSettings{
		InternalPrices: InternalPrices{TokenEUR: 1, GPUTokenEUR: 35, BaseTokens: 8, BaseCores: 16, BaseMemGiB: 32, BaseSSDGB: 100, CoresBlock: &cores, MemBlockGiB: &mem, SSDBlockGB: &ssd, HDDBlockGB: &hdd, GPUTiers: []GPUTier{{16, 1}, {24, 3}, {40, 10}, {89, 11}}},
		MarketPrices:   []MarketPrice{{Name: "Mercato", Level: "low", Currency: "EUR", VCPUHour: .0151, GPUHour: []GPUPrice{{16, .6}, {24, .75}}, Source: "https://example.org/prezzi?x=<test>", Date: "2026-11-19"}},
		StorageClasses: map[string]string{"datacloud-ssd": "ssd"}, Interruptions: []Interruption{{"2026-11-19T10:00", "2026-11-19T11:00", "external", "Corrente"}},
	}
}

func TestReportNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		section, key string
		lo, hi       float64
		zeroDefault  bool
	}{
		{"internal_prices", "token_eur", 0, 1000, false}, {"internal_prices", "gpu_token_eur", 0, 100000, false},
		{"internal_prices", "base_tokens", 0, 1000, false}, {"internal_prices", "base_cores", 0, 4096, false},
		{"internal_prices", "base_mem_gib", 0, 65536, false}, {"internal_prices", "base_ssd_gb", 0, 1e7, false},
		{"internal_prices", "cores_block", 1, 4096, false}, {"internal_prices", "cores_block_tokens", 0, 1000, false},
		{"internal_prices", "mem_block_gib", 1, 65536, false}, {"internal_prices", "mem_block_tokens", 0, 1000, false},
		{"internal_prices", "ssd_block_gb", 1, 1e7, false}, {"internal_prices", "ssd_block_tokens", 0, 1000, false},
		{"internal_prices", "hdd_block_gb", 1, 1e8, false}, {"internal_prices", "hdd_block_tokens", 0, 1000, false},
		{"", "usd_eur", 0, 10, false},
		{"defaults", "inactive_cpu", 0, .5, true}, {"defaults", "inactive_net", 0, 1e9, true}, {"defaults", "inactive_days", 1, 488, true},
		{"defaults", "oversized_mem", 0, 1, true}, {"defaults", "oversized_cpu", 0, 1, true}, {"defaults", "oversized_days", 1, 488, true},
		{"defaults", "idle_util", 0, 50, true}, {"defaults", "idle_mem", 0, 50, true}, {"defaults", "idle_days", 7, 60, true},
		{"defaults", "abandon_months", 1, 12, true}, {"defaults", "abandon_peak", 1, 90, true}, {"defaults", "abandon_cpu", 0, 50, true},
		{"defaults", "abandon_net", 0, 1048576, true}, {"defaults", "over_mem", 1, 90, true}, {"defaults", "over_cpu", 1, 90, true}, {"defaults", "margin", 0, 200, true},
	} {
		t.Run(tc.section+"."+tc.key, func(t *testing.T) {
			for _, v := range []float64{tc.lo, tc.hi, 0, -1, tc.hi + 1} {
				object := map[string]any{tc.key: v}
				if tc.section != "" {
					object = map[string]any{tc.section: object}
				}
				data, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				var r ReportSettings
				err = json.Unmarshal(data, &r)
				if err == nil {
					err = ValidReportSettings(&r)
				}
				want := v >= tc.lo && v <= tc.hi || v == 0 && tc.zeroDefault
				if (err == nil) != want {
					t.Errorf("value %v: %v, valid=%v", v, err, want)
				}
			}
			if tc.lo > 1 {
				object := fmt.Sprintf(`{"%s":{"%s":1}}`, tc.section, tc.key)
				var r ReportSettings
				err := json.Unmarshal([]byte(object), &r)
				if err == nil {
					err = ValidReportSettings(&r)
				}
				if err == nil {
					t.Error("below minimum accepted")
				}
			}
		})
	}
	// All floating point fields reject non-finite values, including block pointers.
	for _, section := range []string{"InternalPrices", "Defaults"} {
		r := sampleReport()
		v := reflect.ValueOf(r).Elem().FieldByName(section)
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
				switch {
				case field.Kind() == reflect.Float64:
					old := field.Float()
					field.SetFloat(bad)
					if ValidReportSettings(r) == nil {
						t.Errorf("accepted %s.%s nonfinite", section, v.Type().Field(i).Name)
					}
					field.SetFloat(old)
				case field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Float64:
					old := field.Elem().Float()
					field.Elem().SetFloat(bad)
					if ValidReportSettings(r) == nil {
						t.Errorf("accepted %s block nonfinite", section)
					}
					field.Elem().SetFloat(old)
				}
			}
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1, 10.01} {
		r := sampleReport()
		r.UsdEur = bad
		if ValidReportSettings(r) == nil {
			t.Error("bad exchange accepted")
		}
	}
	for _, body := range []string{`{"internal_prices":{"base_cores":1.5}}`, `{"internal_prices":{"cores_block":1.5}}`, `{"defaults":{"idle_days":7.5}}`, `{"defaults":{"abandon_months":1.5}}`} {
		var r ReportSettings
		if json.Unmarshal([]byte(body), &r) == nil {
			t.Errorf("fractional integer accepted: %s", body)
		}
	}
}

func TestReportCollectionsAndText(t *testing.T) {
	if err := ValidReportSettings(sampleReport()); err != nil {
		t.Fatal(err)
	}
	if err := ValidReportSettings(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidReportSettings(&ReportSettings{}); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*ReportSettings){
		"internal tier count": func(r *ReportSettings) { r.InternalPrices.GPUTiers = make([]GPUTier, 9) },
		"internal order":      func(r *ReportSettings) { r.InternalPrices.GPUTiers = []GPUTier{{24, 1}, {16, 1}} },
		"internal duplicate":  func(r *ReportSettings) { r.InternalPrices.GPUTiers = []GPUTier{{16, 1}, {16, 1}} },
		"market count":        func(r *ReportSettings) { r.MarketPrices = make([]MarketPrice, 7) },
		"market tier count":   func(r *ReportSettings) { r.MarketPrices[0].GPUHour = make([]GPUPrice, 9) },
		"market order":        func(r *ReportSettings) { r.MarketPrices[0].GPUHour = []GPUPrice{{24, 1}, {16, 1}} },
		"market duplicate":    func(r *ReportSettings) { r.MarketPrices[0].GPUHour = []GPUPrice{{16, 1}, {16, 1}} },
		"name empty":          func(r *ReportSettings) { r.MarketPrices[0].Name = " " },
		"name long":           func(r *ReportSettings) { r.MarketPrices[0].Name = strings.Repeat("è", 65) },
		"source long":         func(r *ReportSettings) { r.MarketPrices[0].Source = strings.Repeat("a", 301) },
		"currency":            func(r *ReportSettings) { r.MarketPrices[0].Currency = "eur" },
		"level":               func(r *ReportSettings) { r.MarketPrices[0].Level = "middle" },
		"date empty":          func(r *ReportSettings) { r.MarketPrices[0].Date = "" },
		"date leap":           func(r *ReportSettings) { r.MarketPrices[0].Date = "2025-02-29" },
		"date format":         func(r *ReportSettings) { r.MarketPrices[0].Date = "2026-1-19" },
		"storage count": func(r *ReportSettings) {
			r.StorageClasses = map[string]string{}
			for i := 0; i < 65; i++ {
				r.StorageClasses[fmt.Sprint(i)] = "ssd"
			}
		},
		"storage traversal":    func(r *ReportSettings) { r.StorageClasses = map[string]string{"../disk": "ssd"} },
		"storage long":         func(r *ReportSettings) { r.StorageClasses = map[string]string{strings.Repeat("s", 65): "ssd"} },
		"storage empty":        func(r *ReportSettings) { r.StorageClasses = map[string]string{"": "ssd"} },
		"storage class":        func(r *ReportSettings) { r.StorageClasses = map[string]string{"disk": "SSD"} },
		"interruption count":   func(r *ReportSettings) { r.Interruptions = make([]Interruption, 501) },
		"interruption same":    func(r *ReportSettings) { r.Interruptions[0].To = r.Interruptions[0].From },
		"interruption reverse": func(r *ReportSettings) { r.Interruptions[0].To = "2026-11-19T09:00" },
		"interruption long":    func(r *ReportSettings) { r.Interruptions[0].To = "2026-12-20T10:01" },
		"interruption date":    func(r *ReportSettings) { r.Interruptions[0].From = "2026-02-30T10:00" },
		"interruption format":  func(r *ReportSettings) { r.Interruptions[0].From = "2026-11-19" },
		"interruption offset":  func(r *ReportSettings) { r.Interruptions[0].From = "2026-11-19T10:00:00+24:00" },
		"interruption cause":   func(r *ReportSettings) { r.Interruptions[0].Cause = "electric" },
		"note long":            func(r *ReportSettings) { r.Interruptions[0].Note = strings.Repeat("x", 201) },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			r := sampleReport()
			edit(r)
			if err := ValidReportSettings(r); err == nil {
				t.Fatal("accepted invalid report")
			}
		})
	}
	for _, s := range []string{"a\nb", "a\rb", "a\x00b", "a\tb", "a\x1bb", "a\u202eb", "a\u2066b", "a\xffb"} {
		for _, field := range []string{"name", "source", "note"} {
			r := sampleReport()
			switch field {
			case "name":
				r.MarketPrices[0].Name = s
			case "source":
				r.MarketPrices[0].Source = s
			case "note":
				r.Interruptions[0].Note = s
			}
			if err := ValidReportSettings(r); err == nil || strings.Contains(err.Error(), s) {
				t.Errorf("%s: invalid text accepted or echoed: %v", field, err)
			}
		}
	}
	r := sampleReport()
	r.MarketPrices[0].Name = strings.Repeat("è", 64)
	r.MarketPrices[0].Source = strings.Repeat("à", 300)
	r.MarketPrices[0].Date = "2024-02-29"
	r.Interruptions[0].Note = strings.Repeat("è", 200)
	r.Interruptions[0].To = "2026-12-20T10:00"
	if err := ValidReportSettings(r); err != nil {
		t.Fatal(err)
	}
	r.Interruptions[0] = Interruption{"2026-11-19T10:00:00+01:00", "2026-11-19T10:00:00Z", "maintenance", ""}
	if err := ValidReportSettings(r); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []string{"external", "maintenance", "fault"} {
		r.Interruptions[0].Cause = cause
		if err := ValidReportSettings(r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReportPriceAndTierBounds(t *testing.T) {
	for _, v := range []float64{-1, 0, 1000, 1001, math.NaN(), math.Inf(1)} {
		for _, key := range []string{"VCPUHour", "RAMGBHour", "StorageGBMonth"} {
			r := sampleReport()
			reflect.ValueOf(&r.MarketPrices[0]).Elem().FieldByName(key).SetFloat(v)
			if (ValidReportSettings(r) == nil) != (v >= 0 && v <= 1000) {
				t.Errorf("%s=%v accepted incorrectly", key, v)
			}
		}
		for _, market := range []bool{false, true} {
			r := sampleReport()
			if market {
				r.MarketPrices[0].GPUHour = []GPUPrice{{0, v}}
			} else {
				r.InternalPrices.GPUTiers = []GPUTier{{0, v}}
			}
			if (ValidReportSettings(r) == nil) != (v >= 0 && v <= 1000) {
				t.Errorf("tier price %v market=%v", v, market)
			}
		}
	}
	for _, v := range []float64{-1, 0, 1024, 1025, math.NaN(), math.Inf(1)} {
		r := sampleReport()
		r.InternalPrices.GPUTiers = []GPUTier{{v, 0}}
		if (ValidReportSettings(r) == nil) != (v >= 0 && v <= 1024) {
			t.Errorf("internal tier %v", v)
		}
		r = sampleReport()
		r.MarketPrices[0].GPUHour = []GPUPrice{{v, 0}}
		if (ValidReportSettings(r) == nil) != (v >= 0 && v <= 1024) {
			t.Errorf("market tier %v", v)
		}
	}
	// Inclusive collection limits remain usable.
	r := sampleReport()
	r.MarketPrices = nil
	for i := 0; i < 6; i++ {
		r.MarketPrices = append(r.MarketPrices, sampleReport().MarketPrices[0])
		r.MarketPrices[i].GPUHour = nil
		for j := 0; j < 8; j++ {
			r.MarketPrices[i].GPUHour = append(r.MarketPrices[i].GPUHour, GPUPrice{float64(j), 1000})
		}
	}
	r.InternalPrices.GPUTiers = nil
	for i := 0; i < 8; i++ {
		r.InternalPrices.GPUTiers = append(r.InternalPrices.GPUTiers, GPUTier{float64(i), 1000})
	}
	r.StorageClasses = map[string]string{strings.Repeat("s", 64): "hdd"}
	for i := 0; i < 63; i++ {
		r.StorageClasses[fmt.Sprintf("s.%d_x-y", i)] = "other"
	}
	for len(r.Interruptions) < 500 {
		r.Interruptions = append(r.Interruptions, r.Interruptions[0])
	}
	if err := ValidReportSettings(r); err != nil {
		t.Fatal(err)
	}
}

func TestReportCopiesAndPersistence(t *testing.T) {
	withTempConfig(t)
	original := sampleReport()
	if err := SetReportSettings(0, original); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := os.Stat(cfgPath)
	if err != nil || mode.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", mode, err)
	}
	mutate := func(r *ReportSettings) {
		r.InternalPrices.TokenEUR = 900
		*r.InternalPrices.CoresBlock = 4000
		*r.InternalPrices.MemBlockGiB = 1
		*r.InternalPrices.SSDBlockGB = 1
		*r.InternalPrices.HDDBlockGB = 1
		r.InternalPrices.GPUTiers[0].Tokens = 99
		r.MarketPrices[0].Name = "changed"
		r.MarketPrices[0].GPUHour[0].Price = 99
		r.StorageClasses["datacloud-ssd"] = "hdd"
		r.Interruptions[0].Note = "changed"
		r.Defaults.Margin = 100
	}
	mutate(original)
	c := Get()
	mutate(c.Clusters[0].Report)
	c1, _ := GetCluster(0)
	mutate(c1.Report)
	c2, _ := GetDefaultCluster()
	mutate(c2.Report)
	if !reflect.DeepEqual(Get().Clusters[0].Report, sampleReport()) {
		t.Fatal("nested data aliases live config")
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Get().Clusters[0].Report, sampleReport()) {
		t.Fatal("round trip failed")
	}
	// Rename failure must leave both live settings and the old file intact.
	path := cfgPath
	cfgPath = t.TempDir()
	if err := SetReportSettings(0, &ReportSettings{}); err == nil {
		t.Fatal("write failure ignored")
	}
	cfgPath = path
	after, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Equal(data, after) || !reflect.DeepEqual(Get().Clusters[0].Report, sampleReport()) {
		t.Fatal("save failure damaged settings")
	}
	if err := SetReportSettings(8, &ReportSettings{}); err != ErrReportCluster {
		t.Fatalf("invalid index: %v", err)
	}
}

func TestReportAudit(t *testing.T) {
	withTempConfig(t)
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(old) })
	if err := SetReportSettings(0, sampleReport()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`cluster "X"`, "internal_prices", "market_prices", "storage_classes", "interruptions"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(text, "\n") != 1 || strings.Contains(text, "Corrente") || strings.Contains(text, "example.org") || strings.Contains(text, "secret") {
		t.Fatal("audit leaked values or split lines")
	}
	out.Reset()
	if err := SetReportSettings(0, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("unchanged settings logged as a change")
	}
}

func TestEconomicEnabled(t *testing.T) {
	var absent *ReportSettings
	if absent.EconomicEnabled() || (&ReportSettings{}).EconomicEnabled() {
		t.Fatal("zero prices enable economics")
	}
	r := &ReportSettings{InternalPrices: InternalPrices{BaseCores: 16, BaseMemGiB: 32, BaseSSDGB: 100, GPUTiers: []GPUTier{{16, 0}}}, UsdEur: 1, MarketPrices: []MarketPrice{{GPUHour: []GPUPrice{{16, 0}}}}}
	if r.EconomicEnabled() {
		t.Fatal("resource sizes or exchange enable economics")
	}
	for _, key := range []string{"TokenEUR", "GPUTokenEUR", "BaseTokens", "CoresBlockTokens", "MemBlockTokens", "SSDBlockTokens", "HDDBlockTokens"} {
		r := &ReportSettings{}
		reflect.ValueOf(&r.InternalPrices).Elem().FieldByName(key).SetFloat(1)
		if !r.EconomicEnabled() {
			t.Errorf("ignored %s", key)
		}
	}
	for _, key := range []string{"VCPUHour", "RAMGBHour", "StorageGBMonth"} {
		r := &ReportSettings{MarketPrices: []MarketPrice{{}}}
		reflect.ValueOf(&r.MarketPrices[0]).Elem().FieldByName(key).SetFloat(1)
		if !r.EconomicEnabled() {
			t.Errorf("ignored %s", key)
		}
	}
	r = &ReportSettings{InternalPrices: InternalPrices{GPUTiers: []GPUTier{{89, 11}}}}
	if !r.EconomicEnabled() {
		t.Fatal("internal GPU ignored")
	}
	r = &ReportSettings{MarketPrices: []MarketPrice{{GPUHour: []GPUPrice{{89, 1}}}}}
	if !r.EconomicEnabled() {
		t.Fatal("market GPU ignored")
	}
}

func TestReportConcurrentClusterUpdates(t *testing.T) {
	withTempConfig(t)
	current.Clusters = append(current.Clusters, ClusterConfig{Label: "Y", APIToken: "other-secret"})
	done := make(chan error, 2)
	for idx := 0; idx < 2; idx++ {
		go func(idx int) {
			for i := 1; i <= 12; i++ {
				r := sampleReport()
				r.InternalPrices.TokenEUR = float64(idx*100 + i)
				if err := SetReportSettings(idx, r); err != nil {
					done <- err
					return
				}
				c := Get()
				c.Clusters[idx].Report.Interruptions[0].Note = "copy only"
				c.Clusters[idx].Report.StorageClasses["new"] = "hdd"
			}
			done <- nil
		}(idx)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for idx, c := range Get().Clusters {
		if c.Report.InternalPrices.TokenEUR != float64(idx*100+12) || c.Report.Interruptions[0].Note != "Corrente" || len(c.Report.StorageClasses) != 1 {
			t.Fatalf("cluster %d changed by another writer or a copy", idx)
		}
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	for idx, c := range Get().Clusters {
		if c.Report.InternalPrices.TokenEUR != float64(idx*100+12) {
			t.Fatalf("cluster %d lost a concurrent update on disk", idx)
		}
	}
}

func TestReportUnknownFieldsRejected(t *testing.T) {
	for _, body := range []string{
		`{"unknown":1}`, `{"internal_prices":{"unknown":1}}`,
		`{"internal_prices":{"gpu_tiers":[{"min_gib":0,"tokens":0,"unknown":1}]}}`,
		`{"market_prices":[{"unknown":1}]}`, `{"market_prices":[{"gpu_hour":[{"unknown":1}]}]}`,
		`{"defaults":{"unknown":1}}`, `{"interruptions":[{"unknown":1}]}`,
	} {
		var r ReportSettings
		if err := json.Unmarshal([]byte(body), &r); err == nil {
			t.Errorf("unknown report field accepted: %s", body)
		}
	}
	withTempConfig(t)
	if err := os.WriteFile(cfgPath, []byte(`{"clusters":[{"label":"X","report":{"defaults":{"unknown":1}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err == nil {
		t.Fatal("unknown report field in config.json accepted")
	}
	// Legacy fields outside report settings retain their existing load semantics.
	if err := os.WriteFile(cfgPath, []byte(`{"legacy_field":true,"clusters":[{"label":"X","report":{}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
}
