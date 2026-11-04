package handlers

import (
	"errors"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// "service" model: the data of the manager report (status of the service, capacity over time, who uses it,
// what is needed). Texts are NOT produced here: the condition carries codes and numbers, the page writes the
// sentences in Italian and English.

type condReason struct {
	Code  string   `json:"code"` // ram_exhaustion | storage_exhaustion | availability | gpu_full | task_failures
	Value *float64 `json:"value,omitempty"`
}

type groupUse struct {
	Group    string `json:"group"`
	VMs      int    `json:"vms"`
	Cores    int64  `json:"cores"`
	MemBytes int64  `json:"mem_bytes"`
	Shared   int    `json:"shared_vms"` // of the VMs, how many also belong to another group
}

type invDay struct {
	Day         string `json:"day"`
	VMs         int    `json:"vms"`
	Running     int    `json:"running"`
	Groups      int    `json:"groups"`
	AllocMem    int64  `json:"alloc_mem"`
	NodeMem     int64  `json:"node_mem"`
	GPUAssigned int    `json:"gpu_assigned"`
	GPUTotal    int    `json:"gpu_total"`
}

type serviceReport struct {
	Meta      reportMeta `json:"meta"`
	Condition struct {
		Level   string       `json:"level"` // ok | warn | bad
		Reasons []condReason `json:"reasons"`
	} `json:"condition"`
	KPI struct {
		Nodes        int      `json:"nodes"`
		VMsRunning   int      `json:"vms_running"`
		VMsTotal     int      `json:"vms_total"`
		Groups       int      `json:"groups"`
		GPUAssigned  int      `json:"gpu_assigned"`
		GPUTotal     int      `json:"gpu_total"`
		MemAllocPct  *float64 `json:"mem_alloc_pct"`
		Availability *float64 `json:"availability_pct"` // estimated from missing RRD samples
		StoragePct   *float64 `json:"storage_pct"`
		StorageUsed  int64    `json:"storage_used"`
		StorageTotal int64    `json:"storage_total"`
	} `json:"kpi"`
	Series struct {
		CPU     []Point `json:"cpu_pct"`
		Mem     []Point `json:"mem_used_pct"`
		Storage []Point `json:"storage_used_pct"`
	} `json:"series"`
	Stats struct {
		CPU Stat `json:"cpu"`
		Mem Stat `json:"mem"`
	} `json:"stats"`
	Forecast struct {
		MemDaysTo85      *int `json:"mem_days_to_85"`
		MemDaysTo100     *int `json:"mem_days_to_100"`
		StorageDaysTo85  *int `json:"storage_days_to_85"`
		StorageDaysTo100 *int `json:"storage_days_to_100"`
	} `json:"forecast"`
	Inventory struct {
		Days     []invDay `json:"days"`
		FirstDay string   `json:"first_day"`
		VMsFirst int      `json:"vms_first"`
		VMsLast  int      `json:"vms_last"`
		GrpFirst int      `json:"groups_first"`
		GrpLast  int      `json:"groups_last"`
		SpanDays int      `json:"span_days"`
		// D37/D42: distinct VMs in more than one group on the last day; D41: first day of the tag rule when the period
		// also contains days grouped by name (the two are not comparable group by group).
		SharedVMs  int    `json:"shared_vms"`
		RuleChange string `json:"group_rule_change"`
		GroupRule  string `json:"group_rule"`
	} `json:"inventory"`
	TopGroups    []groupUse `json:"top_groups"`
	GroupCount   int        `json:"group_count"`
	GPUFullSince string     `json:"gpu_full_since"`
	Tasks        struct {
		Total         int     `json:"total"`
		Failed        int     `json:"failed"`
		FailPct       float64 `json:"fail_pct"`
		Backups       int     `json:"backups"`
		BackupsFailed int     `json:"backups_failed"`
	} `json:"tasks"`
}

func sumSeries(parts []rrdSeries, num, den string) ([]int64, []float64) {
	type acc struct{ n, d float64 }
	m := map[int64]*acc{}
	for _, s := range parts {
		for i, t := range s.T {
			nv, dv := s.Cols[num][i], s.Cols[den][i]
			if math.IsNaN(nv) || math.IsNaN(dv) || dv <= 0 {
				continue
			}
			a := m[t]
			if a == nil {
				a = &acc{}
				m[t] = a
			}
			a.n += nv
			a.d += dv
		}
	}
	ts := make([]int64, 0, len(m))
	for t := range m {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	v := make([]float64, len(ts))
	for i, t := range ts {
		v[i] = m[t].n / m[t].d * 100
	}
	return ts, v
}

func buildServiceReport(c reportAPI, hist *history.Store, cluster string, p reportParams, now time.Time) (serviceReport, error) {
	var rep serviceReport
	rep.Meta = metaFor(cluster, p, now)
	rep.Condition.Reasons = []condReason{}
	rep.Series.CPU, rep.Series.Mem, rep.Series.Storage = []Point{}, []Point{}, []Point{}
	rep.Inventory.Days, rep.TopGroups = []invDay{}, []groupUse{}
	pp := p
	pp.Kind, pp.Group, pp.Query = "all", "", "" // the service report is about the whole service

	res, err := loadResources(c, pp)
	if err != nil {
		return rep, err
	}
	rep.KPI.Nodes = len(res.Nodes)

	// capacity: node RRD aggregated over the online nodes
	rrds := make([]nodeRRDs, len(res.Nodes))
	var jobs []func()
	for i, n := range res.Nodes {
		if n.Online {
			i, n := i, n
			jobs = append(jobs, func() { rrds[i] = fetchNodeRRDs(c, n.Name, pp) })
		}
	}
	runLimited(jobs)
	var parts []rrdSeries
	var covSum float64
	for i, n := range res.Nodes {
		u := nodeReport(rrds[i], n, [2]int64{}, pp)
		covSum += u.Coverage
		if rrds[i].ok {
			s := rrds[i].avg
			// weight the CPU by the node's cores
			cores := s.Cols["maxcpu"]
			cw := make([]float64, len(s.T))
			cc := make([]float64, len(s.T))
			for k := range s.T {
				mc := cores[k]
				if math.IsNaN(mc) || mc <= 0 {
					mc = float64(n.Cores)
				}
				cw[k], cc[k] = s.Cols["cpu"][k]*mc, mc
			}
			s.Cols["cpuw"], s.Cols["corew"] = cw, cc
			parts = append(parts, s)
		}
	}
	if len(res.Nodes) > 0 {
		a := round2(covSum / float64(len(res.Nodes)))
		rep.KPI.Availability = &a
	}
	if len(parts) > 0 {
		ts, cpu := sumSeries(parts, "cpuw", "corew")
		_, mem := sumSeries(parts, "memused", "memtotal")
		cpu = scaleNaN(cpu, 1) // already in percent of weighted cores
		rep.Series.CPU, rep.Series.Mem = downsample(ts, cpu, seriesPoints), downsample(ts, mem, seriesPoints)
		rep.Stats.CPU, rep.Stats.Mem = statOf(cpu, nil, pp.Step, 85), statOf(mem, nil, pp.Step, 85)
		tr := trendOf(ts, mem)
		rep.Forecast.MemDaysTo85, rep.Forecast.MemDaysTo100 = tr.daysToReach(85), tr.daysToReach(100)
	}

	// storage: Ceph (rbd) storages, deduplicated
	var rbd []resStorage
	for _, s := range uniqueStorages(res.Storages) {
		if s.Type == "rbd" {
			rbd = append(rbd, s)
		}
	}
	if len(rbd) > 0 {
		sparts := make([]rrdSeries, len(rbd))
		var sj []func()
		for i, s := range rbd {
			i, s := i, s
			sj = append(sj, func() {
				if raw, err := c.GetStorageRRD(s.Node, s.Name, pp.TF, "AVERAGE"); err == nil {
					if ser, err := parseRRD(raw, "total", "used"); err == nil {
						sparts[i] = ser.clip(pp.From.Unix(), pp.To.Unix())
					}
				}
			})
			rep.KPI.StorageTotal += s.Total
			rep.KPI.StorageUsed += s.Used
		}
		runLimited(sj)
		var ok []rrdSeries
		for _, s := range sparts {
			if len(s.T) > 0 {
				ok = append(ok, s)
			}
		}
		if rep.KPI.StorageTotal > 0 {
			v := round2(float64(rep.KPI.StorageUsed) / float64(rep.KPI.StorageTotal) * 100)
			rep.KPI.StoragePct = &v
		}
		if len(ok) > 0 {
			ts, pct := sumSeries(ok, "used", "total")
			rep.Series.Storage = downsample(ts, pct, seriesPoints)
			tr := trendOf(ts, pct)
			rep.Forecast.StorageDaysTo85, rep.Forecast.StorageDaysTo100 = tr.daysToReach(85), tr.daysToReach(100)
		}
	}

	// inventory snapshots, groups, GPU
	if hist != nil {
		from, to := p.From.Format("2006-01-02"), p.To.Format("2006-01-02")
		days, err := hist.Days(cluster, from, to)
		if err != nil {
			log.Printf("report service: storico non leggibile: %v", err)
		}
		for _, d := range days {
			rep.Inventory.Days = append(rep.Inventory.Days, invDay{Day: d.Day, VMs: d.VMs, Running: d.Running, Groups: d.Groups, AllocMem: d.AllocMem, NodeMem: d.NodeMem, GPUAssigned: d.GPUAssigned, GPUTotal: d.GPUTotal})
		}
		if n := len(days); n > 0 {
			first, last := days[0], days[n-1]
			rep.Inventory.FirstDay, rep.Inventory.VMsFirst, rep.Inventory.VMsLast = first.Day, first.VMs, last.VMs
			rep.Inventory.GrpFirst, rep.Inventory.GrpLast = first.Groups, last.Groups
			if a, e1 := time.Parse("2006-01-02", first.Day); e1 == nil {
				if b, e2 := time.Parse("2006-01-02", last.Day); e2 == nil {
					rep.Inventory.SpanDays = int(b.Sub(a).Hours() / 24)
				}
			}
			rep.KPI.VMsRunning, rep.KPI.VMsTotal, rep.KPI.Groups = last.Running, last.VMs, last.Groups
			rep.Inventory.SharedVMs, rep.Inventory.GroupRule = last.SharedVMs, last.GroupRule
			if first.GroupRule == history.RuleName {
				for _, d := range days {
					if d.GroupRule == history.RuleTags {
						rep.Inventory.RuleChange = d.Day
						break
					}
				}
			}
			if last.NodeMem > 0 {
				v := round2(float64(last.AllocMem) / float64(last.NodeMem) * 100)
				rep.KPI.MemAllocPct = &v
			}
			if last.GPUKnown {
				rep.KPI.GPUAssigned, rep.KPI.GPUTotal = last.GPUAssigned, last.GPUTotal
			}
			if snap, ok, _ := hist.Load(last.Day, cluster); ok {
				byGroup := map[string]*groupUse{}
				milli := map[string]int64{}
				for _, g := range snap.Groups {
					if g.Group == otherGroup {
						continue
					}
					u := byGroup[g.Group]
					if u == nil {
						u = &groupUse{Group: g.Group}
						byGroup[g.Group] = u
					}
					u.VMs += g.VMs
					u.Shared += g.SharedVMs
					milli[g.Group] += int64(g.CoresMilli)
					u.MemBytes += g.MemBytes
				}
				for k, u := range byGroup {
					u.Cores = (milli[k] + 500) / 1000
					rep.TopGroups = append(rep.TopGroups, *u)
				}
				sort.Slice(rep.TopGroups, func(i, j int) bool {
					if rep.TopGroups[i].MemBytes != rep.TopGroups[j].MemBytes {
						return rep.TopGroups[i].MemBytes > rep.TopGroups[j].MemBytes
					}
					return rep.TopGroups[i].Group < rep.TopGroups[j].Group
				})
				rep.GroupCount = len(rep.TopGroups)
				if len(rep.TopGroups) > 10 {
					rep.TopGroups = rep.TopGroups[:10]
				}
			}
		}
		if all, err := hist.Days(cluster, "2000-01-01", now.Format("2006-01-02")); err == nil {
			rep.GPUFullSince = gpuStreak(all)
		}
	}
	if rep.Inventory.RuleChange != "" {
		rep.Meta.Notes = append(rep.Meta.Notes, "group_rule_changed")
	}
	if rep.Inventory.FirstDay == "" || rep.Inventory.FirstDay > rep.Meta.From {
		rep.Meta.Notes = append(rep.Meta.Notes, "inventory_since")
	}

	// tasks
	if rel, err := buildReliabilityReport(c, cluster, pp, now); err == nil {
		rep.Tasks.Total, rep.Tasks.Failed, rep.Tasks.FailPct = rel.Totals.Tasks, rel.Totals.Failed, rel.Totals.FailPct
		rep.Tasks.Backups, rep.Tasks.BackupsFailed = rel.Totals.Backups, rel.Totals.BackupsFailed
	}

	rep.Condition.Level = "ok"
	raise := func(level string) {
		if level == "bad" || rep.Condition.Level == "ok" {
			rep.Condition.Level = level
		}
	}
	add := func(code string, v *float64) {
		rep.Condition.Reasons = append(rep.Condition.Reasons, condReason{Code: code, Value: v})
	}
	fl := func(d int) *float64 { f := float64(d); return &f }
	for _, e := range []struct {
		code      string
		d85, d100 *int
	}{{"ram_exhaustion", rep.Forecast.MemDaysTo85, rep.Forecast.MemDaysTo100}, {"storage_exhaustion", rep.Forecast.StorageDaysTo85, rep.Forecast.StorageDaysTo100}} {
		switch {
		case e.d100 != nil && *e.d100 <= 60:
			raise("bad")
			add(e.code, fl(*e.d100))
		case e.d85 != nil && *e.d85 <= 180:
			raise("warn")
			add(e.code, fl(*e.d85))
		}
	}
	if a := rep.KPI.Availability; a != nil {
		switch {
		case *a < 95:
			raise("bad")
			add("availability", a)
		case *a < 99:
			raise("warn")
			add("availability", a)
		}
	}
	if rep.KPI.GPUTotal > 0 && rep.KPI.GPUAssigned >= rep.KPI.GPUTotal {
		raise("warn")
		add("gpu_full", nil)
	}
	if rep.Tasks.Total >= 20 && rep.Tasks.FailPct >= 5 {
		raise("warn")
		add("task_failures", &rep.Tasks.FailPct)
	}
	return rep, nil
}

// ── HTTP ────────────────────────────────────────────────────────────────────

var reportGates sync.Map // cluster index -> *sync.Mutex: one report at a time per cluster

func reportGate(idx int) *sync.Mutex {
	m, _ := reportGates.LoadOrStore(idx, &sync.Mutex{})
	return m.(*sync.Mutex)
}

var reportModels = map[string]bool{"service": true, "nodes": true, "vms": true, "storage": true, "reliability": true, "gpu": true}

// GetReport: GET /api/reports/{model}?from=YYYY-MM-DD&to=YYYY-MM-DD&nodes=a,b&type=qemu|lxc&group=x&q=text&...
func (h *Handler) GetReport(w http.ResponseWriter, r *http.Request) {
	model := chiParam(r, "model")
	if !reportModels[model] {
		writeError(w, errors.New("modello di report sconosciuto"), http.StatusNotFound)
		return
	}
	now := time.Now()
	p, err := parseReportParams(r.URL.Query().Get, now)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	idx := clusterIdx(r)
	cl, err := config.GetCluster(idx)
	if err != nil || cl.URL == "" {
		writeError(w, errors.New("cluster non configurato"), http.StatusBadRequest)
		return
	}
	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	key := "rpt|" + model + "|" + strconv.Itoa(idx) + "|" + p.key()
	v, err := cephCached(key, reportCacheTTL, func() (interface{}, error) {
		g := reportGate(idx)
		if !g.TryLock() {
			return nil, errReportBusy
		}
		defer g.Unlock()
		switch model {
		case "service":
			return buildServiceReport(client, h.hist, cl.Label, p, now)
		case "nodes":
			return buildNodesReport(client, cl.Label, p, now)
		case "vms":
			return buildVMsReport(client, cl.Label, p, now)
		case "storage":
			return buildStorageReport(client, cl.Label, p, now)
		case "reliability":
			return buildReliabilityReport(client, cl.Label, p, now)
		}
		return buildGPUReport(client, h.hist, cl.Label, p, now)
	})
	if err != nil {
		if errors.Is(err, errReportBusy) {
			w.Header().Set("Retry-After", "10")
			writeError(w, errors.New("un altro report e' in corso su questo cluster: riprova tra qualche secondo"), http.StatusTooManyRequests)
			return
		}
		if err.Error() == errTooManyGuests {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		log.Printf("report %s (cluster %d): %v", model, idx, err)
		writeError(w, errors.New("dati del report non disponibili"), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
}

var errReportBusy = errors.New("report busy")
