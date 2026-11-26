package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// Daily VM usage collector (ROADMAP 21, D118 B). Every hour it looks for complete days of the last
// vmUseBackfillDays that are not stored yet and computes them from the RRD "month" timeframe (30-minute
// points, AVERAGE and MAX): one read per VM covers every missing day, so a manager that was off for a
// few days loses nothing. It only reads the RRD and the VM config (for the instance id): nothing runs in
// the guests. A day is saved whole or not at all (history.SaveVMUseDay).

const (
	vmUseEvery        = time.Hour
	vmUseStartIn      = 2 * time.Minute
	vmUseBackfillDays = 29 // the month RRD keeps 30 days of 30-minute points: the oldest may be partial
	vmUseWorkers      = 8
	vmUseStepS        = 1800
)

type vmUseAPI interface {
	GetClusterResources(resType string) (json.RawMessage, error)
	GetVMConfig(node string, vmid int) (json.RawMessage, error)
	GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error)
}

var _ vmUseAPI = (*api.Client)(nil)

type vmUseStore interface {
	HasVMUseDay(day, cluster string) (bool, error)
	SaveVMUseDay(day, cluster string, rows []history.VMUseDay, takenAt time.Time) error
	VMUseLastBefore(cluster string, vmid int, kind, day string) (history.VMUseDay, bool, error)
	PruneVMUse(now time.Time) error
}

type vmUseGuest struct {
	vmid                  int
	kind, node, name, grp string
	cores                 int
	mem, disk             int64
}

// vmUseSeries is what one guest's month RRD gives, already split per day.
type vmUseRead struct {
	g    vmUseGuest
	inst string // "" = to be decided per day (LXC, or QEMU without smbios1 uuid)
	avg  rrdSeries
	max  rrdSeries
	err  error
}

type vmUseCollector struct {
	running   atomic.Bool
	now       func() time.Time
	newClient func(config.ClusterConfig) vmUseAPI
}

var vmUse = &vmUseCollector{
	now:       time.Now,
	newClient: func(cl config.ClusterConfig) vmUseAPI { return api.NewClientCA(cl.URL, cl.APIToken, cl.TLSCA) },
}

// missingDays lists the complete days of the window that are not stored, oldest first.
func missingDays(st vmUseStore, cluster string, now time.Time) ([]string, error) {
	var out []string
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	for i := vmUseBackfillDays; i >= 1; i-- {
		day := today.AddDate(0, 0, -i).Format("2006-01-02")
		has, err := st.HasVMUseDay(day, cluster)
		if err != nil {
			return nil, err
		}
		if !has {
			out = append(out, day)
		}
	}
	return out, nil
}

func vmUseGuests(raw json.RawMessage) ([]vmUseGuest, error) {
	var res []map[string]interface{}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	var tags []string
	for _, r := range res {
		if typ, _ := r["type"].(string); (typ == "qemu" || typ == "lxc") && !isTemplateRes(r) {
			ts, _ := r["tags"].(string)
			tags = append(tags, ts)
		}
	}
	resolver := newGroupResolver(tags)
	var out []vmUseGuest
	for _, r := range res {
		typ, _ := r["type"].(string)
		if (typ != "qemu" && typ != "lxc") || isTemplateRes(r) {
			continue
		}
		node, _ := r["node"].(string)
		vmid := int(intField(r, "vmid"))
		if !nodeNamePattern.MatchString(node) || vmid <= 0 {
			continue
		}
		name, _ := r["name"].(string)
		ts, _ := r["tags"].(string)
		grp := otherGroup
		if as := resolver.resolve(name, ts); len(as.Groups) > 0 {
			grp = as.Groups[0]
		}
		out = append(out, vmUseGuest{vmid: vmid, kind: typ, node: node, name: asciiText(name, 64), grp: grp,
			cores: int(intField(r, "maxcpu")), mem: intField(r, "maxmem"), disk: intField(r, "maxdisk")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].vmid < out[j].vmid })
	return out, nil
}

var vmUseFields = []string{"cpu", "mem", "maxmem", "netin", "netout", "diskread", "diskwrite"}

func readGuestMonth(c vmUseAPI, g vmUseGuest) vmUseRead {
	r := vmUseRead{g: g}
	if g.kind == "qemu" {
		raw, err := c.GetVMConfig(g.node, g.vmid)
		if err != nil {
			r.err = err
			return r
		}
		var cfg map[string]interface{}
		if json.Unmarshal(raw, &cfg) == nil {
			r.inst = vmInstance(cfg)
		}
	}
	for _, x := range []struct {
		cf  string
		dst *rrdSeries
	}{{"AVERAGE", &r.avg}, {"MAX", &r.max}} {
		raw, err := c.GetGuestRRDcf(g.node, g.kind, g.vmid, "month", x.cf)
		if err != nil {
			r.err = err
			return r
		}
		s, err := parseRRD(raw, vmUseFields...)
		if err != nil {
			r.err = err
			return r
		}
		*x.dst = s
	}
	return r
}

func clampPct(v float64) float64 { return math.Max(0, math.Min(100, v)) }

// dayAggregate computes one day of one guest from the 30-minute series. ok=false: no point with data.
func dayAggregate(rd vmUseRead, from, to int64) (history.VMUseDay, bool) {
	row := history.VMUseDay{Kind: rd.g.kind, Name: rd.g.name, Group: rd.g.grp, Node: rd.g.node, Cores: rd.g.cores,
		MemBytes: rd.g.mem, DiskBytes: rd.g.disk, StepS: vmUseStepS,
		CPUAvg: -1, CPUP95: -1, CPUMax: -1, MemAvg: -1, MemP95: -1, MemMax: -1}
	var cpu, mem []float64
	var netIn, netOut, dRead, dWrite float64
	step := float64(rd.avg.Step)
	if step <= 0 {
		step = vmUseStepS
	}
	for i, t := range rd.avg.T {
		if t < from || t >= to {
			continue
		}
		c := rd.avg.Cols["cpu"][i]
		if math.IsNaN(c) {
			continue // the VM was off (Proxmox writes null, not 0)
		}
		cpu = append(cpu, clampPct(c*100))
		if mu, mx := rd.avg.Cols["mem"][i], rd.avg.Cols["maxmem"][i]; !math.IsNaN(mu) && !math.IsNaN(mx) && mx > 0 {
			mem = append(mem, clampPct(mu/mx*100))
		}
		for _, x := range []struct {
			col string
			dst *float64
		}{{"netin", &netIn}, {"netout", &netOut}, {"diskread", &dRead}, {"diskwrite", &dWrite}} {
			if v := rd.avg.Cols[x.col][i]; !math.IsNaN(v) && v > 0 {
				*x.dst += v * step
			}
		}
	}
	if len(cpu) == 0 {
		return row, false
	}
	row.Points = len(cpu)
	row.CPUAvg, row.CPUP95, row.CPUMax = statsPct(cpu)
	if len(mem) > 0 {
		row.MemAvg, row.MemP95, row.MemMax = statsPct(mem)
	}
	// Peaks from the MAX consolidation: the highest 1-minute value inside each 30-minute point.
	for i, t := range rd.max.T {
		if t < from || t >= to {
			continue
		}
		if c := rd.max.Cols["cpu"][i]; !math.IsNaN(c) {
			row.CPUMax = math.Max(row.CPUMax, clampPct(c*100))
		}
		if mu, mx := rd.max.Cols["mem"][i], rd.max.Cols["maxmem"][i]; !math.IsNaN(mu) && !math.IsNaN(mx) && mx > 0 && row.MemMax >= 0 {
			row.MemMax = math.Max(row.MemMax, clampPct(mu/mx*100))
		}
	}
	row.NetIn, row.NetOut, row.DiskRead, row.DiskWrite = int64(netIn), int64(netOut), int64(dRead), int64(dWrite)
	return row, true
}

func statsPct(v []float64) (avg, p95, max float64) {
	var sum float64
	for _, x := range v {
		sum += x
		max = math.Max(max, x)
	}
	return round2(sum / float64(len(v))), round2(percentile(v, 95)), round2(max)
}

var errInternal = errors.New("errore interno")

type vmUseLast struct {
	inst, day, name string
	hadData         bool
}

// collectCluster stores every missing complete day of one cluster. It returns how many days were saved.
func (v *vmUseCollector) collectCluster(st vmUseStore, cl config.ClusterConfig) int {
	now := v.now()
	days, err := missingDays(st, cl.Label, now)
	if err != nil {
		log.Printf("vm usage %q: %v", cl.Label, err)
		return 0
	}
	if len(days) == 0 {
		return 0
	}
	c := v.newClient(cl)
	raw, err := c.GetClusterResources("vm")
	if err != nil {
		log.Printf("vm usage %q: risorse non lette, riprovo tra un'ora: %v", cl.Label, err)
		return 0
	}
	guests, err := vmUseGuests(raw)
	if err != nil {
		log.Printf("vm usage %q: risorse non valide", cl.Label)
		return 0
	}
	reads := make([]vmUseRead, len(guests))
	sem := make(chan struct{}, vmUseWorkers)
	var wg sync.WaitGroup
	for i, g := range guests {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, g vmUseGuest) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					reads[i] = vmUseRead{g: g, err: errInternal}
					log.Printf("vm usage %q VM %d: errore interno: %v", cl.Label, g.vmid, r)
				}
			}()
			reads[i] = readGuestMonth(c, g)
		}(i, g)
	}
	wg.Wait()
	failed := 0
	for _, r := range reads {
		if r.err != nil {
			failed++
		}
	}
	oldest := days[0]
	saved := 0
	last := map[int]*vmUseLast{} // in-batch continuity, days are processed oldest first
	for _, day := range days {
		// A day is complete or absent: with failed reads it is retried, unless it is about to leave the RRD window.
		windowStart := now.AddDate(0, 0, -vmUseBackfillDays).Format("2006-01-02")
		if failed > 0 && !(day == oldest && day <= windowStart) {
			log.Printf("vm usage %q %s: %d VM non lette, riprovo tra un'ora", cl.Label, day, failed)
			break
		}
		t0, err := time.ParseInLocation("2006-01-02", day, now.Location())
		if err != nil {
			continue
		}
		from, to := t0.Unix(), t0.AddDate(0, 0, 1).Unix()
		rows := []history.VMUseDay{}
		for _, rd := range reads {
			if rd.err != nil {
				continue
			}
			row, ok := dayAggregate(rd, from, to)
			l := last[rd.g.vmid]
			if l == nil {
				prev, found, err := st.VMUseLastBefore(cl.Label, rd.g.vmid, rd.g.kind, day)
				if err != nil {
					log.Printf("vm usage %q VM %d: %v", cl.Label, rd.g.vmid, err)
					continue
				}
				l = &vmUseLast{}
				if found {
					*l = vmUseLast{inst: prev.Inst, day: prev.Day, name: prev.Name, hadData: true}
				}
				last[rd.g.vmid] = l
			}
			// A day without data counts as "off" only for a VM already seen: never invent days before it existed.
			if !ok && !l.hadData {
				continue
			}
			inst := rd.inst
			if inst == "" {
				prevDay := t0.AddDate(0, 0, -1).Format("2006-01-02")
				if l.inst != "" && l.day == prevDay && l.name == rd.g.name {
					inst = l.inst // same LXC (or uuid-less VM) as yesterday
				} else {
					inst = "l:" + day
				}
			}
			row.Day, row.Cluster, row.VMID, row.Inst = day, cl.Label, rd.g.vmid, inst
			if err := history.ValidVMUse(row); err != nil {
				log.Printf("vm usage %q VM %d %s: riga scartata: %v", cl.Label, rd.g.vmid, day, err)
				continue
			}
			rows = append(rows, row)
			*l = vmUseLast{inst: inst, day: day, name: rd.g.name, hadData: true}
		}
		if err := st.SaveVMUseDay(day, cl.Label, rows, now); err != nil {
			log.Printf("vm usage %q %s: salvataggio non riuscito: %v", cl.Label, day, err)
			break
		}
		saved++
	}
	if saved > 0 {
		log.Printf("vm usage %q: %d giorni salvati (%d VM lette, %d non lette)", cl.Label, saved, len(reads)-failed, failed)
	}
	if err := st.PruneVMUse(now); err != nil {
		log.Printf("vm usage %q: pulizia non riuscita: %v", cl.Label, err)
	}
	return saved
}

// cycle collects every configured cluster once; a running cycle makes the next one a no-op.
func (v *vmUseCollector) cycle(st vmUseStore, clusters []config.ClusterConfig) {
	if !v.running.CompareAndSwap(false, true) {
		return
	}
	defer v.running.Store(false)
	for _, cl := range clusters {
		if cl.URL != "" && cl.Label != "" {
			v.collectCluster(st, cl)
		}
	}
}

// StartVMUse runs the daily VM usage collector until ctx is cancelled (on unless config vm_usage is false).
func StartVMUse(ctx context.Context, store *history.Store) {
	go func() {
		timer := time.NewTimer(vmUseStartIn)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							vmUse.running.Store(false)
							log.Printf("vm usage: giro interrotto da un errore interno: %v", r)
						}
					}()
					if cfg := config.Get(); cfg.VMUsageEnabled() {
						vmUse.cycle(store, cfg.Clusters)
					}
				}()
				timer.Reset(vmUseEvery)
			}
		}
	}()
}
