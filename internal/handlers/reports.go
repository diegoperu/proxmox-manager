package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/history"
)

// Report data (read only): server-side aggregation of Proxmox RRD, the daily inventory snapshot, tasks and the GPU
// inventory. One endpoint per model; the browser only draws. Parameters are whitelisted, work against Proxmox is
// bounded (8 workers, size caps), results are cached for a minute and only one report runs per cluster at a time.

const errTooManyGuests = "troppe risorse nel report: restringi i filtri (nodo, tipo, gruppo)"

const (
	reportWorkers   = 8
	reportCacheTTL  = 60 * time.Second
	maxReportGuests = 600
	maxReportStores = 80
	maxTaskLimit    = 5000
	sparkPoints     = 30
	seriesPoints    = 120
	p95WindowDays   = 28
)

type reportAPI interface {
	gpuAPI
	GetClusterResources(resType string) (json.RawMessage, error)
	GetNodeRRDcf(node, tf, cf string) (json.RawMessage, error)
	GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error)
	GetStorageRRD(node, storage, tf, cf string) (json.RawMessage, error)
	GetNodeTasks(node string, limit int) (json.RawMessage, error)
}

var _ reportAPI = (*api.Client)(nil)

// ── parameters ──────────────────────────────────────────────────────────────

type reportParams struct {
	From, To time.Time
	TF       string
	Step     int64
	Nodes    map[string]bool // nil = all
	Kind     string          // all | qemu | lxc
	Group    string          // "" = all
	Query    string
	TH       VMThresholds
}

func (p reportParams) days() float64 { return p.To.Sub(p.From).Hours() / 24 }

func (p reportParams) key() string {
	nodes := make([]string, 0, len(p.Nodes))
	for n := range p.Nodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%+v", p.From.Format("2006-01-02"), p.To.Format("2006-01-02"), strings.Join(nodes, ","), p.Kind, p.Group, p.Query, p.TH)
}

func floatParam(v string, def, lo, hi float64) (float64, error) {
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < lo || f > hi {
		return 0, errors.New("soglia non valida")
	}
	return f, nil
}

// parseReportParams validates every query parameter against a whitelist.
func parseReportParams(q func(string) string, now time.Time) (reportParams, error) {
	p := reportParams{Kind: "all", TH: defaultVMThresholds()}
	loc := now.Location()
	to := now
	if s := q("to"); s != "" {
		if !history.ValidDay(s) {
			return p, errors.New("data finale non valida (AAAA-MM-GG)")
		}
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return p, errors.New("data finale non valida")
		}
		to = t.Add(24*time.Hour - time.Second)
		if to.After(now) {
			to = now
		}
	}
	s := q("from")
	if !history.ValidDay(s) {
		return p, errors.New("data iniziale non valida (AAAA-MM-GG)")
	}
	from, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return p, errors.New("data iniziale non valida")
	}
	if to.Sub(from) < 20*time.Hour {
		return p, errors.New("il periodo deve essere di almeno un giorno")
	}
	tf, step, err := pickTimeframe(now, from)
	if err != nil {
		return p, err
	}
	p.From, p.To, p.TF, p.Step = from, to, tf, step
	if s := q("nodes"); s != "" {
		p.Nodes = map[string]bool{}
		for _, n := range strings.Split(s, ",") {
			if !nodeNamePattern.MatchString(n) || len(p.Nodes) >= 64 {
				return p, errors.New("nodo non valido")
			}
			p.Nodes[n] = true
		}
	}
	switch k := q("type"); k {
	case "", "all":
	case "qemu", "lxc":
		p.Kind = k
	default:
		return p, errors.New("tipo non valido")
	}
	if g := q("group"); g != "" {
		if !groupPattern.MatchString(g) && g != otherGroup {
			return p, errors.New("gruppo non valido")
		}
		p.Group = g
	}
	if s := q("q"); s != "" {
		if len(s) > 64 || strings.ContainsAny(s, "\x00\r\n") {
			return p, errors.New("ricerca non valida")
		}
		p.Query = strings.ToLower(s)
	}
	th := &p.TH
	for _, f := range []struct {
		name   string
		dst    *float64
		lo, hi float64
	}{{"inactive_cpu", &th.InactiveCPU, 0, 0.5}, {"inactive_net", &th.InactiveNetBps, 0, 1e9}, {"inactive_days", &th.InactiveMinDays, 1, 488},
		{"oversized_mem", &th.OversizedMem, 0, 1}, {"oversized_cpu", &th.OversizedCPU, 0, 1}, {"oversized_days", &th.OversizedMinDays, 1, 488}} {
		v, err := floatParam(q(f.name), *f.dst, f.lo, f.hi)
		if err != nil {
			return p, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.dst = v
	}
	return p, nil
}

type reportMeta struct {
	Cluster     string   `json:"cluster"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Days        int      `json:"days"`
	Timeframe   string   `json:"timeframe"`
	StepSeconds int64    `json:"step_seconds"`
	P95Window   string   `json:"p95_window"` // "period" | "last 28 days" | ""
	Generated   int64    `json:"generated"`
	Notes       []string `json:"notes"`
}

func metaFor(cluster string, p reportParams, now time.Time) reportMeta {
	m := reportMeta{Cluster: cluster, From: p.From.Format("2006-01-02"), To: p.To.Format("2006-01-02"), Days: int(math.Round(p.days())),
		Timeframe: p.TF, StepSeconds: p.Step, Generated: now.Unix(), Notes: []string{}}
	if p.Step <= fineStepMax {
		m.P95Window = "period"
	} else {
		m.P95Window = "last 28 days"
		m.Notes = append(m.Notes, "Oltre 30 giorni Proxmox conserva medie su 6 ore (circa gli ultimi 90 giorni) e poi settimanali: media e massimo sono sul periodo intero, il 95° percentile e' calcolato sulle ultime 4 settimane, le tendenze sulle medie settimanali.")
	}
	return m
}

// ── resources ───────────────────────────────────────────────────────────────

type resNode struct {
	Name   string
	Online bool
	Cores  int64
	Mem    int64
}
type resGuest struct {
	Node, Kind, Name, Status string
	VMID                     int
	Cores, Mem, Disk         int64
}
type resStorage struct {
	Name, Node, Type string
	Shared           bool
	Online           bool
	Total, Used      int64
}
type resources struct {
	Nodes    []resNode
	Guests   []resGuest
	Storages []resStorage
}

func loadResources(c reportAPI, p reportParams) (resources, error) {
	raw, err := c.GetClusterResources("")
	if err != nil {
		return resources{}, err
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return resources{}, err
	}
	var out resources
	for _, r := range list {
		typ, _ := r["type"].(string)
		node, _ := r["node"].(string)
		if !nodeNamePattern.MatchString(node) || (p.Nodes != nil && !p.Nodes[node]) {
			continue
		}
		status, _ := r["status"].(string)
		switch typ {
		case "node":
			out.Nodes = append(out.Nodes, resNode{Name: node, Online: status == "online", Cores: intField(r, "maxcpu"), Mem: intField(r, "maxmem")})
		case "qemu", "lxc":
			if isTemplateRes(r) || (p.Kind != "all" && p.Kind != typ) {
				continue
			}
			name, _ := r["name"].(string)
			if p.Group != "" && vmGroup(name) != p.Group {
				continue
			}
			if p.Query != "" && !strings.Contains(strings.ToLower(name), p.Query) {
				continue
			}
			vmid := int(intField(r, "vmid"))
			if vmid <= 0 {
				continue
			}
			out.Guests = append(out.Guests, resGuest{Node: node, Kind: typ, Name: clampText(name, 64), Status: status, VMID: vmid,
				Cores: intField(r, "maxcpu"), Mem: intField(r, "maxmem"), Disk: intField(r, "maxdisk")})
		case "storage":
			sn, _ := r["storage"].(string)
			pt, _ := r["plugintype"].(string)
			if sn == "" {
				continue
			}
			out.Storages = append(out.Storages, resStorage{Name: clampText(sn, 64), Node: node, Type: clampText(pt, 16), Shared: parseBoolish(r["shared"]),
				Online: status == "available", Total: intField(r, "maxdisk"), Used: intField(r, "disk")})
		}
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return naturalLess(out.Nodes[i].Name, out.Nodes[j].Name) })
	sort.Slice(out.Guests, func(i, j int) bool { return out.Guests[i].VMID < out.Guests[j].VMID })
	return out, nil
}

func runLimited(jobs []func()) {
	sem := make(chan struct{}, reportWorkers)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j func()) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			j()
		}(j)
	}
	wg.Wait()
}

func scaleNaN(v []float64, k float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x * k
	}
	return out
}

func ratioNaN(num, den []float64) []float64 {
	out := make([]float64, len(num))
	for i := range num {
		if i < len(den) && den[i] > 0 && !math.IsNaN(num[i]) && !math.IsNaN(den[i]) {
			out[i] = num[i] / den[i] * 100
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}

func orNaN(v []float64, n int) []float64 {
	if v != nil {
		return v
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

func pctPtr(v *float64) *float64 { return v }

// ── nodes ───────────────────────────────────────────────────────────────────

type nodeUsage struct {
	Node         string  `json:"node"`
	Online       bool    `json:"online"`
	Cores        int64   `json:"cores"`
	MemTotal     int64   `json:"mem_total"`
	AllocCores   int64   `json:"alloc_cores"`
	AllocMem     int64   `json:"alloc_mem"`
	CPU          Stat    `json:"cpu"`
	Mem          Stat    `json:"mem"`
	PSICPU       float64 `json:"psi_cpu_avg"`
	PSIIO        float64 `json:"psi_io_avg"`
	IOWait       float64 `json:"iowait_avg"`
	MemTrend     Trend   `json:"mem_trend"`
	MemDaysTo85  *int    `json:"mem_days_to_85"`
	MemDaysTo100 *int    `json:"mem_days_to_100"`
	Saturated    bool    `json:"saturated"`
	Coverage     float64 `json:"coverage_pct"` // % of expected samples present
	Spark        []Point `json:"spark"`
}

type nodeRRDs struct {
	avg, max, month rrdSeries
	ok              bool
}

func fetchNodeRRDs(c reportAPI, node string, p reportParams) nodeRRDs {
	fields := []string{"cpu", "maxcpu", "memused", "memtotal", "iowait", "pressurecpusome", "pressureiosome"}
	var out nodeRRDs
	get := func(tf, cf string) (rrdSeries, bool) {
		raw, err := c.GetNodeRRDcf(node, tf, cf)
		if err != nil {
			return rrdSeries{}, false
		}
		s, err := parseRRD(raw, fields...)
		return s, err == nil
	}
	var ok1, ok2 bool
	out.avg, ok1 = get(p.TF, "AVERAGE")
	out.max, ok2 = get(p.TF, "MAX")
	out.ok = ok1
	_ = ok2
	if p.Step > fineStepMax {
		if m, ok := get("month", "AVERAGE"); ok {
			out.month = m
		}
	}
	from, to := p.From.Unix(), p.To.Unix()
	out.avg, out.max = out.avg.clip(from, to), out.max.clip(from, to)
	return out
}

func nodeReport(r nodeRRDs, rn resNode, alloc [2]int64, p reportParams) nodeUsage {
	u := nodeUsage{Node: rn.Name, Online: rn.Online, Cores: rn.Cores, MemTotal: rn.Mem, AllocCores: alloc[0], AllocMem: alloc[1], Spark: []Point{}}
	if !r.ok {
		return u
	}
	n := len(r.avg.T)
	cpu := scaleNaN(orNaN(r.avg.Cols["cpu"], n), 100)
	cpuMax := scaleNaN(orNaN(r.max.Cols["cpu"], len(r.max.T)), 100)
	mem := ratioNaN(orNaN(r.avg.Cols["memused"], n), orNaN(r.avg.Cols["memtotal"], n))
	memMax := ratioNaN(orNaN(r.max.Cols["memused"], len(r.max.T)), orNaN(r.max.Cols["memtotal"], len(r.max.T)))
	u.CPU, u.Mem = statOf(cpu, cpuMax, p.Step, 85), statOf(mem, memMax, p.Step, 85)
	if p.Step > fineStepMax && len(r.month.T) > 0 {
		mn := len(r.month.T)
		cut := r.month.T[mn-1] - p95WindowDays*86400
		mc := r.month.clip(cut, r.month.T[mn-1])
		mcpu := scaleNaN(orNaN(mc.Cols["cpu"], len(mc.T)), 100)
		mmem := ratioNaN(orNaN(mc.Cols["memused"], len(mc.T)), orNaN(mc.Cols["memtotal"], len(mc.T)))
		u.CPU.P95, u.Mem.P95 = statOf(mcpu, nil, 1800, 85).P95, statOf(mmem, nil, 1800, 85).P95
	}
	u.PSICPU = round2(statOf(r.avg.Cols["pressurecpusome"], nil, p.Step, 1e9).Avg)
	u.PSIIO = round2(statOf(r.avg.Cols["pressureiosome"], nil, p.Step, 1e9).Avg)
	u.IOWait = round2(statOf(scaleNaN(orNaN(r.avg.Cols["iowait"], n), 100), nil, p.Step, 1e9).Avg)
	u.MemTrend = trendOf(r.avg.T, mem)
	u.MemDaysTo85, u.MemDaysTo100 = u.MemTrend.daysToReach(85), u.MemTrend.daysToReach(100)
	hot := func(s Stat) bool {
		if s.P95 != nil {
			return *s.P95 >= 85
		}
		return s.Avg >= 85
	}
	u.Saturated = hot(u.CPU) || hot(u.Mem)
	u.Coverage = round2(validFraction(cpu) * 100)
	u.Spark = downsample(r.avg.T, cpu, sparkPoints)
	return u
}

type nodesReport struct {
	Meta  reportMeta `json:"meta"`
	Total struct {
		Nodes     int `json:"nodes"`
		Saturated int `json:"saturated"`
	} `json:"totals"`
	Rows []nodeUsage `json:"rows"`
}

func buildNodesReport(c reportAPI, cluster string, p reportParams, now time.Time) (nodesReport, error) {
	res, err := loadResources(c, p)
	if err != nil {
		return nodesReport{}, err
	}
	rep := nodesReport{Meta: metaFor(cluster, p, now), Rows: []nodeUsage{}}
	rrds := make([]nodeRRDs, len(res.Nodes))
	var jobs []func()
	for i, n := range res.Nodes {
		if !n.Online {
			continue
		}
		i, n := i, n
		jobs = append(jobs, func() { rrds[i] = fetchNodeRRDs(c, n.Name, p) })
	}
	runLimited(jobs)
	alloc := map[string][2]int64{}
	allGuests, _ := loadResources(c, reportParams{Kind: "all", Nodes: p.Nodes})
	for _, g := range allGuests.Guests {
		a := alloc[g.Node]
		a[0] += g.Cores
		a[1] += g.Mem
		alloc[g.Node] = a
	}
	for i, n := range res.Nodes {
		u := nodeReport(rrds[i], n, alloc[n.Name], p)
		if u.Saturated {
			rep.Total.Saturated++
		}
		rep.Rows = append(rep.Rows, u)
	}
	rep.Total.Nodes = len(rep.Rows)
	return rep, nil
}

// ── VMs ─────────────────────────────────────────────────────────────────────

type vmUsage struct {
	Node      string   `json:"node"`
	VMID      int      `json:"vmid"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Group     string   `json:"group"`
	Status    string   `json:"status"`
	Cores     int64    `json:"cores"`
	MaxMem    int64    `json:"max_mem"`
	MaxDisk   int64    `json:"max_disk"`
	CPUAvg    float64  `json:"cpu_avg"` // % of allocated vCPU
	CPUMax    float64  `json:"cpu_max"`
	CPUP95    *float64 `json:"cpu_p95"`
	MemAvg    float64  `json:"mem_avg"` // % of assigned RAM
	MemMax    float64  `json:"mem_max"`
	MemP95    *float64 `json:"mem_p95"`
	NetInBps  float64  `json:"net_in_bps"`
	NetOutBps float64  `json:"net_out_bps"`
	DataDays  float64  `json:"data_days"`
	Flag      string   `json:"flag"` // "" | inactive | oversized
	Spark     []Point  `json:"spark"`
}

type vmsReport struct {
	Meta       reportMeta   `json:"meta"`
	Thresholds VMThresholds `json:"thresholds"`
	Totals     struct {
		VMs          int   `json:"vms"`
		Running      int   `json:"running"`
		Inactive     int   `json:"inactive"`
		Oversized    int   `json:"oversized"`
		ReclaimMem   int64 `json:"reclaimable_mem_bytes"` // assigned RAM of inactive VMs
		ReclaimCores int64 `json:"reclaimable_cores"`
	} `json:"totals"`
	Rows []vmUsage `json:"rows"`
}

func guestRRD(c reportAPI, g resGuest, tf, cf string, from, to int64) (rrdSeries, bool) {
	raw, err := c.GetGuestRRDcf(g.Node, g.Kind, g.VMID, tf, cf)
	if err != nil {
		return rrdSeries{}, false
	}
	s, err := parseRRD(raw, "cpu", "mem", "maxmem", "netin", "netout")
	if err != nil {
		return rrdSeries{}, false
	}
	return s.clip(from, to), true
}

func vmRow(c reportAPI, g resGuest, p reportParams) vmUsage {
	row := vmUsage{Node: g.Node, VMID: g.VMID, Kind: g.Kind, Name: g.Name, Group: vmGroup(g.Name), Status: g.Status, Cores: g.Cores, MaxMem: g.Mem, MaxDisk: g.Disk, Spark: []Point{}}
	from, to := p.From.Unix(), p.To.Unix()
	avg, ok := guestRRD(c, g, p.TF, "AVERAGE", from, to)
	if !ok || len(avg.T) == 0 {
		return row
	}
	mx, _ := guestRRD(c, g, p.TF, "MAX", from, to)
	n := len(avg.T)
	cpu := scaleNaN(orNaN(avg.Cols["cpu"], n), 100)
	cpuMax := scaleNaN(orNaN(mx.Cols["cpu"], len(mx.T)), 100)
	mem := ratioNaN(orNaN(avg.Cols["mem"], n), orNaN(avg.Cols["maxmem"], n))
	memMax := ratioNaN(orNaN(mx.Cols["mem"], len(mx.T)), orNaN(mx.Cols["maxmem"], len(mx.T)))
	cs, ms := statOf(cpu, cpuMax, p.Step, 101), statOf(mem, memMax, p.Step, 101)
	row.CPUAvg, row.CPUMax, row.CPUP95 = cs.Avg, cs.Max, cs.P95
	row.MemAvg, row.MemMax, row.MemP95 = ms.Avg, ms.Max, ms.P95
	row.NetInBps = round2(statOf(avg.Cols["netin"], nil, p.Step, 1e18).Avg)
	row.NetOutBps = round2(statOf(avg.Cols["netout"], nil, p.Step, 1e18).Avg)
	row.DataDays = round2(coverageDays(avg.T, cpu, avg.Step))
	row.Spark = downsample(avg.T, cpu, sparkPoints)
	// percentiles on long periods come from the last 28 days (30-minute resolution)
	if p.Step > fineStepMax {
		row.CPUP95, row.MemP95 = nil, nil
		if m, ok := guestRRD(c, g, "month", "AVERAGE", time.Now().Unix()-p95WindowDays*86400, to); ok && len(m.T) > 0 {
			mn := len(m.T)
			mc := scaleNaN(orNaN(m.Cols["cpu"], mn), 100)
			mm := ratioNaN(orNaN(m.Cols["mem"], mn), orNaN(m.Cols["maxmem"], mn))
			row.CPUP95, row.MemP95 = statOf(mc, nil, 1800, 101).P95, statOf(mm, nil, 1800, 101).P95
		}
	}
	ev := vmEvidence{Running: g.Status == "running", DataDays: row.DataDays, CPUAvg: row.CPUAvg / 100, NetAvg: row.NetInBps + row.NetOutBps}
	if row.CPUP95 != nil {
		v := *row.CPUP95 / 100
		ev.CPUP95 = &v
	}
	if row.MemP95 != nil {
		v := *row.MemP95 / 100
		ev.MemP95F = &v
	}
	row.Flag = classifyVM(ev, p.TH)
	return row
}

func buildVMsReport(c reportAPI, cluster string, p reportParams, now time.Time) (vmsReport, error) {
	res, err := loadResources(c, p)
	if err != nil {
		return vmsReport{}, err
	}
	if len(res.Guests) > maxReportGuests {
		return vmsReport{}, errors.New(errTooManyGuests)
	}
	rep := vmsReport{Meta: metaFor(cluster, p, now), Thresholds: p.TH, Rows: make([]vmUsage, len(res.Guests))}
	jobs := make([]func(), 0, len(res.Guests))
	for i, g := range res.Guests {
		i, g := i, g
		jobs = append(jobs, func() { rep.Rows[i] = vmRow(c, g, p) })
	}
	runLimited(jobs)
	for _, r := range rep.Rows {
		rep.Totals.VMs++
		if r.Status == "running" {
			rep.Totals.Running++
		}
		switch r.Flag {
		case "inactive":
			rep.Totals.Inactive++
			rep.Totals.ReclaimMem += r.MaxMem
			rep.Totals.ReclaimCores += r.Cores
		case "oversized":
			rep.Totals.Oversized++
		}
	}
	return rep, nil
}

// ── storage ─────────────────────────────────────────────────────────────────

type storageUsage struct {
	Storage    string  `json:"storage"`
	Type       string  `json:"type"`
	Shared     bool    `json:"shared"`
	Node       string  `json:"node"`
	Total      int64   `json:"total"`
	Used       int64   `json:"used"`
	UsedPct    float64 `json:"used_pct"`
	Trend      Trend   `json:"trend"`
	DaysTo85   *int    `json:"days_to_85"`
	DaysTo100  *int    `json:"days_to_100"`
	Series     []Point `json:"series"` // used %
	Unreadable bool    `json:"unreadable"`
}

type storageReport struct {
	Meta reportMeta     `json:"meta"`
	Rows []storageUsage `json:"rows"`
}

// uniqueStorages: shared storages appear once per node in the cluster resources; keep the first online one.
func uniqueStorages(in []resStorage) []resStorage {
	seen := map[string]bool{}
	var out []resStorage
	for _, s := range in {
		if !s.Online {
			continue
		}
		k := s.Name
		if !s.Shared {
			k = s.Node + "/" + s.Name
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Node < out[j].Node
	})
	return out
}

func storageRow(c reportAPI, s resStorage, p reportParams) storageUsage {
	u := storageUsage{Storage: s.Name, Type: s.Type, Shared: s.Shared, Node: s.Node, Total: s.Total, Used: s.Used, Series: []Point{}}
	if s.Total > 0 {
		u.UsedPct = round2(float64(s.Used) / float64(s.Total) * 100)
	}
	raw, err := c.GetStorageRRD(s.Node, s.Name, p.TF, "AVERAGE")
	if err != nil {
		u.Unreadable = true
		return u
	}
	ser, err := parseRRD(raw, "total", "used")
	if err != nil {
		u.Unreadable = true
		return u
	}
	ser = ser.clip(p.From.Unix(), p.To.Unix())
	pct := ratioNaN(orNaN(ser.Cols["used"], len(ser.T)), orNaN(ser.Cols["total"], len(ser.T)))
	u.Series = downsample(ser.T, pct, seriesPoints)
	u.Trend = trendOf(ser.T, pct)
	u.DaysTo85, u.DaysTo100 = u.Trend.daysToReach(85), u.Trend.daysToReach(100)
	return u
}

func buildStorageReport(c reportAPI, cluster string, p reportParams, now time.Time) (storageReport, error) {
	res, err := loadResources(c, p)
	if err != nil {
		return storageReport{}, err
	}
	list := uniqueStorages(res.Storages)
	if len(list) > maxReportStores {
		list = list[:maxReportStores]
	}
	rep := storageReport{Meta: metaFor(cluster, p, now), Rows: make([]storageUsage, len(list))}
	jobs := make([]func(), 0, len(list))
	for i, s := range list {
		i, s := i, s
		jobs = append(jobs, func() { rep.Rows[i] = storageRow(c, s, p) })
	}
	runLimited(jobs)
	return rep, nil
}

// ── reliability (tasks) ─────────────────────────────────────────────────────

type taskTypeStat struct {
	Type   string `json:"type"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}
type reliabilityReport struct {
	Meta   reportMeta `json:"meta"`
	Totals struct {
		Tasks         int     `json:"tasks"`
		Failed        int     `json:"failed"`
		FailPct       float64 `json:"fail_pct"`
		Backups       int     `json:"backups"`
		BackupsFailed int     `json:"backups_failed"`
	} `json:"totals"`
	ByType       []taskTypeStat `json:"by_type"`
	ByNode       map[string]int `json:"failed_by_node"`
	CoverageDays float64        `json:"coverage_days"` // how far back the fetched task lists reach (oldest of the nodes)
}

func taskFailed(status string) bool {
	return status != "" && status != "OK" && !strings.HasPrefix(status, "WARNINGS")
}

func buildReliabilityReport(c reportAPI, cluster string, p reportParams, now time.Time) (reliabilityReport, error) {
	res, err := loadResources(c, p)
	if err != nil {
		return reliabilityReport{}, err
	}
	rep := reliabilityReport{Meta: metaFor(cluster, p, now), ByNode: map[string]int{}, ByType: []taskTypeStat{}}
	type nodeTasks struct {
		node   string
		tasks  []map[string]interface{}
		oldest float64
		ok     bool
	}
	out := make([]nodeTasks, len(res.Nodes))
	var jobs []func()
	for i, n := range res.Nodes {
		if !n.Online {
			continue
		}
		i, n := i, n
		jobs = append(jobs, func() {
			raw, err := c.GetNodeTasks(n.Name, maxTaskLimit)
			if err != nil {
				return
			}
			var list []map[string]interface{}
			if json.Unmarshal(raw, &list) != nil {
				return
			}
			out[i] = nodeTasks{node: n.Name, tasks: list, ok: true}
		})
	}
	runLimited(jobs)
	byType := map[string]*taskTypeStat{}
	from, to := float64(p.From.Unix()), float64(p.To.Unix())
	rep.CoverageDays = math.MaxFloat64
	for _, nt := range out {
		if !nt.ok {
			continue
		}
		oldest := float64(now.Unix())
		for _, t := range nt.tasks {
			st, _ := t["starttime"].(float64)
			if st > 0 && st < oldest {
				oldest = st
			}
			if st < from || st > to {
				continue
			}
			typ, _ := t["type"].(string)
			status, _ := t["status"].(string)
			if !groupPattern.MatchString(strings.ReplaceAll(typ, "-", "")) && typ != "" {
				typ = "altro"
			}
			ts := byType[typ]
			if ts == nil {
				ts = &taskTypeStat{Type: typ}
				byType[typ] = ts
			}
			ts.Total++
			rep.Totals.Tasks++
			failed := taskFailed(status)
			if failed {
				ts.Failed++
				rep.Totals.Failed++
				rep.ByNode[nt.node]++
			}
			if typ == "vzdump" {
				rep.Totals.Backups++
				if failed {
					rep.Totals.BackupsFailed++
				}
			}
		}
		if d := (float64(now.Unix()) - oldest) / 86400; d < rep.CoverageDays {
			rep.CoverageDays = d
		}
	}
	if rep.CoverageDays == math.MaxFloat64 {
		rep.CoverageDays = 0
	}
	rep.CoverageDays = round2(rep.CoverageDays)
	for _, ts := range byType {
		rep.ByType = append(rep.ByType, *ts)
	}
	sort.Slice(rep.ByType, func(i, j int) bool {
		if rep.ByType[i].Failed != rep.ByType[j].Failed {
			return rep.ByType[i].Failed > rep.ByType[j].Failed
		}
		return rep.ByType[i].Total > rep.ByType[j].Total
	})
	if len(rep.ByType) > 20 {
		rep.ByType = rep.ByType[:20]
	}
	if rep.Totals.Tasks > 0 {
		rep.Totals.FailPct = round2(float64(rep.Totals.Failed) / float64(rep.Totals.Tasks) * 100)
	}
	if rep.CoverageDays < p.days() {
		rep.Meta.Notes = append(rep.Meta.Notes, "Lo storico dei task di Proxmox non copre tutto il periodo: i conteggi riguardano solo gli ultimi giorni disponibili.")
	}
	return rep, nil
}

// ── GPU ─────────────────────────────────────────────────────────────────────

type gpuModelRow struct {
	Node     string `json:"node"`
	Model    string `json:"model"`
	Cards    int    `json:"cards"`
	Assigned int    `json:"assigned"`
}
type gpuReport struct {
	Meta         reportMeta     `json:"meta"`
	Total        int            `json:"total"`
	Assigned     int            `json:"assigned"`
	Problems     int            `json:"problems"`
	Models       []gpuModelRow  `json:"models"`
	ByGroup      map[string]int `json:"by_group"`
	Days         []gpuDay       `json:"days"`
	FullSince    string         `json:"full_since"` // first day of the current streak with every card assigned
	SnapshotFrom string         `json:"snapshot_from"`
}
type gpuDay struct {
	Day      string `json:"day"`
	Total    int    `json:"total"`
	Assigned int    `json:"assigned"`
}

// gpuStreak: the first day of the final run of days with every card assigned ("" if the last day is not full).
func gpuStreak(days []history.DayTotals) string {
	since := ""
	for i := len(days) - 1; i >= 0; i-- {
		d := days[i]
		if !d.GPUKnown || d.GPUTotal == 0 || d.GPUAssigned < d.GPUTotal {
			break
		}
		since = d.Day
	}
	return since
}

func buildGPUReport(c reportAPI, hist *history.Store, cluster string, p reportParams, now time.Time) (gpuReport, error) {
	inv, err := fetchGPUInventory(c, now)
	if err != nil {
		return gpuReport{}, err
	}
	rep := gpuReport{Meta: metaFor(cluster, p, now), ByGroup: map[string]int{}, Models: []gpuModelRow{}, Days: []gpuDay{}, Problems: len(inv.Problems)}
	type k struct{ node, model string }
	agg := map[k]*gpuModelRow{}
	for _, n := range inv.Nodes {
		if p.Nodes != nil && !p.Nodes[n.Node] {
			continue
		}
		for _, cd := range n.Cards {
			m := agg[k{n.Node, cd.Name}]
			if m == nil {
				m = &gpuModelRow{Node: n.Node, Model: clampText(cd.Name, 80)}
				agg[k{n.Node, cd.Name}] = m
			}
			m.Cards++
			rep.Total++
			if len(cd.Uses) > 0 {
				m.Assigned++
				rep.Assigned++
				rep.ByGroup[vmGroup(cd.Uses[0].Name)]++
			}
		}
	}
	for _, m := range agg {
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].Node != rep.Models[j].Node {
			return naturalLess(rep.Models[i].Node, rep.Models[j].Node)
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})
	if hist != nil {
		days, err := hist.Days(cluster, p.From.Format("2006-01-02"), p.To.Format("2006-01-02"))
		if err != nil {
			log.Printf("report gpu: storico non leggibile: %v", err)
		} else {
			all, _ := hist.Days(cluster, "2000-01-01", now.Format("2006-01-02"))
			rep.FullSince = gpuStreak(all)
			for _, d := range days {
				if d.GPUKnown {
					rep.Days = append(rep.Days, gpuDay{Day: d.Day, Total: d.GPUTotal, Assigned: d.GPUAssigned})
				}
			}
			if len(all) > 0 {
				rep.SnapshotFrom = all[0].Day
			}
		}
	}
	if rep.SnapshotFrom == "" || rep.SnapshotFrom > rep.Meta.From {
		rep.Meta.Notes = append(rep.Meta.Notes, "L'inventario storico e' disponibile solo dal giorno di attivazione della fotografia giornaliera"+map[bool]string{true: " (" + rep.SnapshotFrom + ")", false: ""}[rep.SnapshotFrom != ""]+".")
	}
	return rep, nil
}
