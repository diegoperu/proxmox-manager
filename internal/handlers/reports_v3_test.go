package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

const v3SecretVMID = 998876543
const v3SecretNode = "distinct-secret-node"
const v3SecretVM = "distinct-secret-guest"
const v3SecretGroup = "distinctsecretgroup"
const v3SecretInst = "u:abcdefab-1234-4567-89ab-abcdefabcdef"

type fakeV3 struct {
	fakeRpt
	now                                          time.Time
	failConfig, failNode, failTasks, failStorage bool
	mu                                           sync.Mutex
	contentCalls                                 int
}

func (f *fakeV3) GetClusterResources(string) (json.RawMessage, error) {
	return json.Marshal([]map[string]interface{}{
		{"type": "node", "node": v3SecretNode, "status": "online", "maxcpu": 64, "maxmem": 128 * gib},
		{"type": "qemu", "node": v3SecretNode, "vmid": v3SecretVMID, "name": v3SecretVM, "tags": "gruppo-" + v3SecretGroup, "status": "running", "maxcpu": 4, "maxmem": 8 * gib},
		{"type": "qemu", "node": v3SecretNode, "vmid": v3SecretVMID + 1, "name": "distinct-secret-template", "template": 1, "maxcpu": 100, "maxmem": 100 * gib},
	})
}
func (f *fakeV3) GetNodes() (json.RawMessage, error) {
	return json.Marshal([]map[string]string{{"node": v3SecretNode, "status": "online"}})
}
func (f *fakeV3) GetVMs(string) (json.RawMessage, error) {
	return json.Marshal([]map[string]interface{}{{"vmid": v3SecretVMID, "name": v3SecretVM, "status": "running"}})
}
func (f *fakeV3) GetVMConfig(string, int) (json.RawMessage, error) {
	if f.failConfig {
		return nil, errors.New("unreadable configuration")
	}
	return json.Marshal(map[string]string{"smbios1": "uuid=" + strings.TrimPrefix(v3SecretInst, "u:"), "scsi0": "distinct-secret-storage:vm-disk,size=101G", "hostpci0": "0000:01:00.0"})
}
func (f *fakeV3) GetPCIMappings() (json.RawMessage, error) { return json.RawMessage(`[]`), nil }
func (f *fakeV3) GetNodePCI(string) (json.RawMessage, error) {
	return json.RawMessage(`[{"id":"0000:01:00.0","class":"0x030200","vendor":"0x10de","vendor_name":"NVIDIA Corporation","device_name":"[A2]","iommugroup":5}]`), nil
}
func (f *fakeV3) GetNodeRRDcf(string, string, string) (json.RawMessage, error) {
	if f.failNode {
		return nil, errors.New("node unavailable")
	}
	return json.Marshal([]map[string]interface{}{
		{"time": f.now.Add(-48 * time.Hour).Unix(), "cpu": .2, "maxcpu": 64, "memused": 32 * gib, "memtotal": 128 * gib},
		{"time": f.now.Add(-24 * time.Hour).Unix(), "cpu": .3, "maxcpu": 64, "memused": 64 * gib, "memtotal": 128 * gib},
	})
}
func (f *fakeV3) GetNodeTasks(string, int) (json.RawMessage, error) {
	if f.failTasks {
		return nil, errors.New("tasks unavailable")
	}
	return json.Marshal([]map[string]interface{}{{"starttime": f.now.Add(-time.Hour).Unix(), "type": "vzdump", "status": "could not activate storage 'distinct-secret-storage'"}})
}
func (f *fakeV3) GetStorage() (json.RawMessage, error) {
	return json.RawMessage(`[{"storage":"distinct-secret-backup","type":"pbs"},{"storage":"../unsafe","type":"pbs"}]`), nil
}
func (f *fakeV3) GetStorageContent(node, storage, content string) (json.RawMessage, error) {
	f.mu.Lock()
	f.contentCalls++
	f.mu.Unlock()
	if f.failStorage {
		return nil, errors.New("backup storage unavailable")
	}
	if node != v3SecretNode || storage != "distinct-secret-backup" || content != "backup" {
		return nil, errors.New("unvalidated content query")
	}
	return json.Marshal([]map[string]interface{}{{"vmid": v3SecretVMID, "ctime": f.now.Add(-time.Hour).Unix(), "volid": "secret-volume"}})
}

func v3TestHistory(t *testing.T, now time.Time) *history.Store {
	t.Helper()
	h, err := history.New(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, day := range []string{"2026-09-16", "2026-10-10", "2026-11-18"} {
		row := history.VMUseDay{Day: day, Cluster: "C", VMID: v3SecretVMID, Inst: v3SecretInst, Kind: "qemu", Name: v3SecretVM, Group: v3SecretGroup, Node: v3SecretNode,
			Cores: 4, MemBytes: 8 * gib, Points: 48, StepS: 1800, CPUAvg: 20, CPUMax: 40, MemAvg: 50, MemMax: 75}
		if err := h.SaveVMUseDay(day, "C", []history.VMUseDay{row}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.UpsertGPUUse([]history.GPUUseDay{{Day: "2026-10-10", Cluster: "C", VMID: v3SecretVMID, Inst: v3SecretInst, Node: v3SecretNode, Group: v3SecretGroup,
		Model: "distinct-secret-removed-card", Cards: 1, Expected: 100, Valid: 100, BusyMinutes: 250, TempMax: -1, PowerAvg: -1}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestV3ServiceIntegrationAnonymous(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f := &fakeV3{fakeRpt: newRpt(), now: now}
	h := v3TestHistory(t, now)
	p := defaultReportParams()
	p.From, p.To, p.TF, p.Step = time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), now, "year", 21600
	s := &config.ReportSettings{InternalPrices: config.InternalPrices{TokenEUR: 1, BaseTokens: 8, GPUTokenEUR: 35, GPUTiers: []config.GPUTier{{MinGiB: 16, Tokens: 1}}},
		MarketPrices:   []config.MarketPrice{{Name: "Provider", Level: "low", Currency: "EUR", VCPUHour: .1, RAMGBHour: .01, StorageGBMonth: .02, GPUHour: []config.GPUPrice{{MinGiB: 16, Price: .5}}, Date: "2026-11-01"}},
		StorageClasses: map[string]string{"distinct-secret-storage": "ssd"}, Interruptions: []config.Interruption{{From: "2026-10-31T23:00:00Z", To: "2026-11-01T01:00:00Z", Cause: "external", Note: "distinct-secret-note"}}}
	rep, err := buildServiceReport(f, h, "C", p, now, serviceV3Settings{Report: s, Models: map[string]config.GPUModel{"distinct-secret-removed-card": {MemoryGB: 24}}})
	if err != nil {
		t.Fatal(err)
	}
	v := rep.V3
	if len(v.Growth) != 3 || !v.Growth[0].Partial || !v.Growth[2].Partial || v.Growth[1].Partial || v.Concentration.Groups != 1 || v.Economic == nil || v.Economic.DaysUsed != 1 {
		t.Fatalf("v3 period/settings %+v", v)
	}
	if v.GPUClasses[1].CardsNow == nil || *v.GPUClasses[1].CardsNow != 0 || math.Abs(*v.GPUClasses[1].BusyHours-250.0/60) > .001 || *v.GPUClasses[0].AssignedNow != 1 {
		t.Fatalf("GPU classes %+v", v.GPUClasses)
	}
	v3AssertPct(t, v.Backups.CoveragePct, 100)
	if v.Backups.Causes.StorageUnreachable != 1 || f.contentCalls != 1 {
		t.Fatalf("backups %+v calls %d", v.Backups, f.contentCalls)
	}
	if v.Economic.LowEURMonth == nil || v.Economic.InternalEURMonth == nil {
		t.Fatalf("economics %+v", v.Economic)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{v3SecretVM, v3SecretNode, v3SecretGroup, v3SecretInst, "998876543", "998876544", "distinct-secret-template", "distinct-secret-storage", "distinct-secret-removed-card", "distinct-secret-note", "secret-volume"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("v3 leaks %q: %s", forbidden, b)
		}
	}
	// Existing top-level fields remain populated alongside v3.
	if rep.Meta.Cluster != "C" || rep.KPI.Nodes != 1 || rep.VMTrend.Oversized < 0 || len(rep.Series.Mem) == 0 {
		t.Fatalf("legacy fields lost: %+v", rep)
	}
}

func TestV3AdapterReadFailuresStayUnknown(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f := &fakeV3{fakeRpt: newRpt(), now: now, failNode: true, failTasks: true, failStorage: true}
	p := defaultReportParams()
	p.From, p.To = now.AddDate(0, -2, 0), now
	v, err := buildServiceV3(f, nil, "C", p, now, serviceV3Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Availability.TotalPct != nil || v.Availability.Months[0].TotalPct != nil || v.Backups.Nights != nil || v.Backups.Causes != nil || v.Backups.CoveragePct != nil || v.Backups.CoverageReason != "unreadable" || v.Economic != nil {
		t.Fatalf("unknown data replaced with zeros: %+v", v)
	}
	for _, month := range v.Growth {
		if month.NodeMemUsedPct != nil {
			t.Fatal("partial node RAM became cluster total")
		}
	}
	for _, class := range v.GPUClasses {
		if class.CardDays != nil {
			t.Fatal("nil history became known empty history")
		}
	}
}

func TestV3EconomicAdapterUnknownResourcesAndConfig(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	h := v3TestHistory(t, now)
	f := &fakeV3{now: now, failConfig: true}
	guests, days, err := loadV3EconomicGuests(f, h, "C", []resGuest{{VMID: v3SecretVMID, Name: v3SecretVM, Node: v3SecretNode, Kind: "qemu"}}, &GPUInventory{}, serviceV3Settings{Report: &config.ReportSettings{}}, now)
	if err != nil || days != 1 || len(guests) != 1 {
		t.Fatalf("%+v %d %v", guests, days, err)
	}
	g := guests[0]
	if g.Cores != -1 || g.MemBytes != -1 || g.Hours != nil || g.SSDGB != -1 {
		t.Fatalf("unreadable config inherited history or zero resources: %+v", g)
	}
}

func TestV3GroupMappingsUseOneAlternativePerSlot(t *testing.T) {
	inv := GPUInventory{Nodes: []GPUNode{{Node: "n", Cards: []GPUCard{{Addr: "0000:01:00", Name: "NVIDIA 24GB"}, {Addr: "0000:02:00", Name: "NVIDIA 24GB"}},
		Groups: []GPUGroupMapping{{ID: "pool", Cards: 2, Uses: []GPUUse{{VMID: 100, Slot: "hostpci0"}}}}}}}
	mappings := MappingSet{"pool": {{Node: "n", Addr: "01:00"}, {Node: "n", Addr: "02:00"}}}
	got := expandV3GPUAssignments(inv, mappings, nil)
	classes := calculateV3GPUClasses(&got, nil, true, nil, time.Now(), time.Now())
	if len(got.Partial) != 0 || *classes[1].AssignedNow != 1 || *classes[1].FreeNow != 1 || len(inv.Nodes[0].Cards[0].Uses) != 0 {
		t.Fatalf("pool alternatives or input modified: %+v", got)
	}
	inv.Nodes[0].Cards[1].Name = "NVIDIA 48GB"
	got = expandV3GPUAssignments(inv, mappings, nil)
	classes = calculateV3GPUClasses(&got, nil, true, nil, time.Now(), time.Now())
	if len(got.Partial) == 0 || classes[1].AssignedNow != nil {
		t.Fatal("heterogeneous pool assignment must remain unknown")
	}
}

func TestV3CacheIdentityIncludesSettingsAndCorrections(t *testing.T) {
	base := serviceV3Settings{}
	a := serviceV3SettingsKey(base)
	if a != serviceV3SettingsKey(base) {
		t.Fatal("unstable cache identity")
	}
	base.Report = &config.ReportSettings{Defaults: config.ReportDefaults{Margin: 30}}
	if a == serviceV3SettingsKey(base) {
		t.Fatal("threshold change reused stale report")
	}
	a = serviceV3SettingsKey(base)
	base.Models = map[string]config.GPUModel{"removed": {MemoryGB: 48}}
	if a == serviceV3SettingsKey(base) {
		t.Fatal("GPU correction reused stale report")
	}
}

type fakeV3NullInventory struct {
	*fakeV3
	nullList bool
}

func (f fakeV3NullInventory) GetVMs(node string) (json.RawMessage, error) {
	if f.nullList {
		return json.RawMessage(`null`), nil
	}
	return f.fakeV3.GetVMs(node)
}
func (f fakeV3NullInventory) GetVMConfig(string, int) (json.RawMessage, error) {
	return json.RawMessage(`null`), nil
}

func TestV3GPUAdapterNullDataCannotProveFreeCards(t *testing.T) {
	for _, nullList := range []bool{true, false} {
		f := fakeV3NullInventory{fakeV3: &fakeV3{}, nullList: nullList}
		inv := loadV3GPUInventory(f, []resNode{{Name: v3SecretNode, Online: true}}, time.Now(), nil)
		if inv == nil || len(inv.Partial) == 0 {
			t.Fatalf("null inventory reported complete: %+v", inv)
		}
		classes := calculateV3GPUClasses(inv, nil, false, nil, time.Now(), time.Now())
		if classes[0].FreeNow != nil || classes[0].AssignedNow != nil {
			t.Fatal("null data became known free cards")
		}
	}
}

func TestV3UsesBoundedTrendWorkers(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f := &fakeTrend{fakeRpt: newRpt()}
	for i := 0; i < 24; i++ {
		f.guests = append(f.guests, map[string]interface{}{"type": "qemu", "node": "n1", "vmid": 100 + i, "name": "guest", "status": "running", "maxcpu": 4, "maxmem": 8 * gib})
	}
	p := defaultReportParams()
	p.From, p.To = now.AddDate(0, -2, 0), now
	if _, err := buildServiceV3(f, nil, "C", p, now, serviceV3Settings{}); err != nil {
		t.Fatal(err)
	}
	if f.peak <= 1 || f.peak > 8 {
		t.Fatalf("peak Proxmox concurrency %d", f.peak)
	}
}

// D130: thresholds saved in Settings apply when the request does not set them; a request value wins.
func TestReportDefaultsFromSettings(t *testing.T) {
	rs := &config.ReportSettings{Defaults: config.ReportDefaults{IdleUtil: 7, IdleDays: 21, AbandonPeak: 15, InactiveCPU: 0.03}}
	base := map[string]string{"idle_util": "", "from": "2026-10-01", "to": "2026-10-31"}
	q := withReportDefaults(func(k string) string { return base[k] }, rs)
	if q("idle_util") != "7" || q("idle_days") != "21" || q("abandon_peak") != "15" || q("inactive_cpu") != "0.03" || q("over_mem") != "" || q("from") != "2026-10-01" {
		t.Fatalf("defaults not applied: %q %q %q %q %q", q("idle_util"), q("idle_days"), q("abandon_peak"), q("inactive_cpu"), q("over_mem"))
	}
	base["idle_util"] = "3"
	if q("idle_util") != "3" {
		t.Fatal("request value must win over the saved default")
	}
	p, err := parseReportParams(q, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC))
	if err != nil || p.GPUIdle.Util != 3 || p.GPUIdle.Days != 21 || p.TH.InactiveCPU != 0.03 {
		t.Fatalf("parsed with defaults: %+v %v", p.GPUIdle, err)
	}
	tp, err := parseTrendParams(q, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC))
	if err != nil || tp.Trend.AbandonPeak != 15 {
		t.Fatalf("trend with defaults: %+v %v", tp.Trend, err)
	}
	if withReportDefaults(func(string) string { return "" }, nil)("idle_util") != "" {
		t.Fatal("nil settings must not invent values")
	}
}
