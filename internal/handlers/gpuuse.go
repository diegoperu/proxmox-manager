package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"proxmox-manager/internal/api"
	"proxmox-manager/internal/config"
	"proxmox-manager/internal/history"
)

// GPU usage collector (ROADMAP 4, D107-D113). Every gpuUseEvery the server reads nvidia-smi inside each
// RUNNING QEMU VM that has a card (gpuuse.sh, helper or inline), keeps the last sample per VM in memory for the
// pages and accumulates a daily aggregate that is written to history every hour. Nothing here runs because of
// an HTTP request: pages only read memory. Off unless config `gpu_usage` is true.

const (
	gpuUseEvery      = 5 * time.Minute
	gpuUseStartIn    = 90 * time.Second
	gpuUseWorkers    = 4                // guests read at the same time, per cluster
	gpuUseTimeout    = 25 * time.Second // whole guest command, the script itself stops nvidia-smi at 15 s
	gpuUseFailLimit  = 3                // consecutive failures before a VM is left alone for gpuUseSkipFor
	gpuUseSkipFor    = time.Hour
	gpuUseProbeTTL   = time.Hour // whether the labelled helper exists: asked once per hour per VM
	gpuUseFlushEvery = time.Hour
	gpuUseStaleAfter = 15 * time.Minute
	gpuUseBusyPct    = 10.0 // D113: a sample "at work" has utilization or memory at or above this
	gpuUseMaxSamples = 400  // per VM and day (288 expected at 5 minutes): bounds memory
)

// Sample states. "busy"/"quiet" have numbers; the others say why there are none (never 0% for "unknown").
const (
	gpuStBusy        = "busy"
	gpuStQuiet       = "quiet"
	gpuStVMOff       = "vm_off"
	gpuStNoDriver    = "no_driver"
	gpuStDriverError = "driver_error"
	gpuStNoData      = "no_data"
	gpuStPaused      = "paused" // too many failures: not asked until the pause ends
)

type gpuUseAPI interface {
	GetClusterResources(resType string) (json.RawMessage, error)
	agentExecer
}

var _ gpuUseAPI = (*api.Client)(nil)

// GPUUseLive is the last sample of one VM, as the pages see it.
type GPUUseLive struct {
	VMID     int          `json:"vmid"`
	Node     string       `json:"node"`
	Name     string       `json:"name"`
	State    string       `json:"state"`
	At       int64        `json:"at"`
	Stale    bool         `json:"stale"`
	Model    string       `json:"model"`
	Cards    []gpuCardUse `json:"cards"`
	Rejected int          `json:"rejected,omitempty"`
}

type gpuUseAcc struct {
	node, group, model            string
	cards                         int
	expected, valid, off          int
	noDriver, driverError, noData int
	busy                          int
	utils                         []float64 // per valid sample with a known utilization: highest card
	utilSum, memSum, memMax       float64
	memN                          int
	tempMax, powerSum             float64
	tempKnown                     bool
	powerN                        int
}

type gpuUseAccKey struct {
	cluster, day string
	vmid         int
}

type gpuUseFail struct {
	count int
	until time.Time
}

type gpuUseProbe struct {
	helper bool
	at     time.Time
}

type gpuUseTarget struct {
	vmid               int
	node, name, status string
	group, model       string
	cards              int
}

type gpuUseStore interface {
	UpsertGPUUse(rows []history.GPUUseDay) error
	PruneGPUUse(now time.Time) error
}

type gpuUseCollector struct {
	mu        sync.Mutex
	live      map[string]map[int]GPUUseLive // cluster label -> VMID
	acc       map[gpuUseAccKey]*gpuUseAcc
	fail      map[string]gpuUseFail  // cluster|vmid
	probe     map[string]gpuUseProbe // cluster|vmid
	lastFlush time.Time
	running   atomic.Bool

	now       func() time.Time
	store     gpuUseStore
	newClient func(config.ClusterConfig) gpuUseAPI
	inventory func(idx int, cl config.ClusterConfig) (GPUInventory, error)
}

func newGPUUseCollector() *gpuUseCollector {
	return &gpuUseCollector{
		live:  map[string]map[int]GPUUseLive{},
		acc:   map[gpuUseAccKey]*gpuUseAcc{},
		fail:  map[string]gpuUseFail{},
		probe: map[string]gpuUseProbe{},
		now:   time.Now,
		newClient: func(cl config.ClusterConfig) gpuUseAPI {
			return api.NewClient(cl.URL, cl.APIToken)
		},
		// The same cached inventory as the GPU page: no extra reads of every VM config.
		inventory: func(idx int, cl config.ClusterConfig) (GPUInventory, error) {
			c := api.NewClient(cl.URL, cl.APIToken)
			v, err := cephCached("gpus|"+strconv.Itoa(idx), gpuCacheTTL, func() (interface{}, error) {
				return fetchGPUInventory(c, time.Now())
			})
			if err != nil {
				return GPUInventory{}, err
			}
			inv, ok := v.(GPUInventory)
			if !ok {
				return GPUInventory{}, errors.New("inventario GPU non valido")
			}
			return inv, nil
		},
	}
}

// gpuUse is the collector of the running server (the endpoint reads it).
var gpuUse = newGPUUseCollector()

// asciiText keeps printable ASCII only and cuts to n bytes (Proxmox text stored in history must match its patterns).
func asciiText(s string, n int) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(b) < n; i++ {
		if s[i] >= 0x20 && s[i] <= 0x7e {
			b = append(b, s[i])
		}
	}
	return strings.TrimSpace(string(b))
}

// gpuTargets lists the VMs that have at least one card, with their state from /cluster/resources.
func gpuTargets(inv GPUInventory, resources json.RawMessage) ([]gpuUseTarget, error) {
	var res []map[string]interface{}
	if err := json.Unmarshal(resources, &res); err != nil {
		return nil, err
	}
	byVM := map[int]map[string]interface{}{}
	var tags []string
	for _, r := range res {
		if typ, _ := r["type"].(string); typ != "qemu" && typ != "lxc" {
			continue
		}
		if isTemplateRes(r) {
			continue
		}
		ts, _ := r["tags"].(string)
		tags = append(tags, ts)
		if typ, _ := r["type"].(string); typ == "qemu" {
			if vmid := int(intField(r, "vmid")); vmid > 0 {
				byVM[vmid] = r
			}
		}
	}
	resolver := newGroupResolver(tags)
	targets := map[int]*gpuUseTarget{}
	for _, n := range inv.Nodes {
		for _, card := range n.Cards {
			seen := map[int]bool{} // a VM listed twice on one card (two slots) counts the card once
			for _, u := range card.Uses {
				r, ok := byVM[u.VMID]
				if !ok || seen[u.VMID] {
					continue
				}
				seen[u.VMID] = true
				t := targets[u.VMID]
				if t == nil {
					node, _ := r["node"].(string)
					name, _ := r["name"].(string)
					status, _ := r["status"].(string)
					ts, _ := r["tags"].(string)
					as := resolver.resolve(name, ts)
					group := otherGroup
					if len(as.Groups) > 0 {
						group = as.Groups[0]
					}
					t = &gpuUseTarget{vmid: u.VMID, node: node, name: clampText(name, 64), status: status,
						group: group, model: asciiText(card.Name, 80)}
					targets[u.VMID] = t
				}
				t.cards++
			}
		}
	}
	out := make([]gpuUseTarget, 0, len(targets))
	for _, t := range targets {
		if nodeNamePattern.MatchString(t.node) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].vmid < out[j].vmid })
	return out, nil
}

// sampleState decides busy/quiet from the parsed cards; no card with a number = no_data.
func sampleState(cards []gpuCardUse) string {
	known := false
	for _, c := range cards {
		if c.Util != nil {
			known = true
			if *c.Util >= gpuUseBusyPct {
				return gpuStBusy
			}
		}
		if p, ok := memPct(c); ok {
			known = true
			if p >= gpuUseBusyPct {
				return gpuStBusy
			}
		}
	}
	if !known {
		return gpuStNoData
	}
	return gpuStQuiet
}

func memPct(c gpuCardUse) (float64, bool) {
	if c.MemUsedMiB == nil || c.MemTotalMiB == nil || *c.MemTotalMiB <= 0 {
		return 0, false
	}
	return math.Min(100, *c.MemUsedMiB / *c.MemTotalMiB * 100), true
}

// readGuest runs gpuuse.sh in one VM. The helper probe is cached per VM for gpuUseProbeTTL.
func (g *gpuUseCollector) readGuest(c gpuUseAPI, key, node string, vmid int) (state string, cards []gpuCardUse, rejected int, err error) {
	g.mu.Lock()
	p, ok := g.probe[key]
	g.mu.Unlock()
	if !ok || g.now().Sub(p.at) >= gpuUseProbeTTL {
		helper, perr := gpuUseTool.probeHelper(c, node, vmid)
		if perr != nil {
			return gpuStNoData, nil, 0, perr
		}
		p = gpuUseProbe{helper: helper, at: g.now()}
		g.mu.Lock()
		g.probe[key] = p
		g.mu.Unlock()
	}
	res, err := gpuUseTool.run(c, node, vmid, p.helper, nil, gpuUseTimeout)
	if err != nil {
		return gpuStNoData, nil, 0, err
	}
	switch gpuUseExitState(res.ExitCode) {
	case "ok":
		cards, rejected = parseGPUUse(res.Out)
		return sampleState(cards), cards, rejected, nil
	case "no_driver":
		return gpuStNoDriver, nil, 0, nil
	case "driver_error":
		return gpuStDriverError, nil, 0, nil
	}
	return gpuStNoData, nil, 0, errors.New("gpuuse.sh exit " + strconv.Itoa(res.ExitCode))
}

// record adds one sample to the day's accumulator. Called with g.mu held.
func (g *gpuUseCollector) record(cluster, day string, t gpuUseTarget, state string, cards []gpuCardUse) {
	k := gpuUseAccKey{cluster, day, t.vmid}
	a := g.acc[k]
	if a == nil {
		a = &gpuUseAcc{tempMax: -1}
		g.acc[k] = a
	}
	a.node, a.group, a.model, a.cards = t.node, t.group, t.model, t.cards
	switch state {
	case gpuStVMOff:
		a.off++
		return
	case gpuStNoDriver:
		a.noDriver++
	case gpuStDriverError:
		a.driverError++
	case gpuStNoData, gpuStPaused:
		a.noData++
	case gpuStBusy, gpuStQuiet:
		a.valid++
		if state == gpuStBusy {
			a.busy++
		}
		util, utilKnown, mem, memKnown := -1.0, false, -1.0, false
		for _, c := range cards {
			if c.Util != nil && *c.Util > util {
				util, utilKnown = *c.Util, true
			}
			if p, ok := memPct(c); ok && p > mem {
				mem, memKnown = p, true
			}
			if c.TempC != nil && *c.TempC > a.tempMax {
				a.tempMax, a.tempKnown = *c.TempC, true
			}
			if c.PowerW != nil {
				a.powerSum += *c.PowerW
				a.powerN++
			}
		}
		if utilKnown && len(a.utils) < gpuUseMaxSamples {
			a.utils = append(a.utils, util)
			a.utilSum += util
		}
		if memKnown {
			a.memSum += mem
			a.memN++
			if mem > a.memMax {
				a.memMax = mem
			}
		}
	}
	a.expected++
}

func (a *gpuUseAcc) row(k gpuUseAccKey) history.GPUUseDay {
	r := history.GPUUseDay{Day: k.day, Cluster: k.cluster, VMID: k.vmid, Node: a.node, Group: a.group, Model: a.model,
		Cards: a.cards, Expected: a.expected, Valid: a.valid, Off: a.off, NoDriver: a.noDriver, DriverError: a.driverError,
		NoData: a.noData, BusyMinutes: min(a.busy*int(gpuUseEvery/time.Minute), 24*60), TempMax: -1, PowerAvg: -1}
	if n := len(a.utils); n > 0 {
		s := append([]float64(nil), a.utils...)
		sort.Float64s(s)
		r.UtilAvg = a.utilSum / float64(n)
		r.UtilMax = s[n-1]
		r.UtilP95 = s[int(math.Ceil(0.95*float64(n)))-1]
	}
	if a.memN > 0 {
		r.MemAvgPct, r.MemMaxPct = a.memSum/float64(a.memN), a.memMax
	}
	if a.tempKnown {
		r.TempMax = a.tempMax
	}
	if a.powerN > 0 {
		r.PowerAvg = math.Min(2000, a.powerSum/float64(a.powerN))
	}
	return r
}

// collectCluster samples every GPU VM of one cluster and replaces the cluster's live view.
func (g *gpuUseCollector) collectCluster(idx int, cl config.ClusterConfig) {
	inv, err := g.inventory(idx, cl)
	if err != nil {
		log.Printf("gpu usage %q: inventario GPU non letto: %v", cl.Label, err)
		return
	}
	c := g.newClient(cl)
	raw, err := c.GetClusterResources("vm")
	if err != nil {
		log.Printf("gpu usage %q: risorse del cluster non lette: %v", cl.Label, err)
		return
	}
	targets, err := gpuTargets(inv, raw)
	if err != nil {
		log.Printf("gpu usage %q: risorse del cluster non valide", cl.Label)
		return
	}
	now := g.now()
	day := now.Format("2006-01-02")
	live := make(map[int]GPUUseLive, len(targets))
	var liveMu sync.Mutex
	put := func(t gpuUseTarget, state string, cards []gpuCardUse, rejected int) {
		if cards == nil {
			cards = []gpuCardUse{}
		}
		liveMu.Lock()
		live[t.vmid] = GPUUseLive{VMID: t.vmid, Node: t.node, Name: t.name, State: state, At: now.Unix(), Model: t.model,
			Cards: cards, Rejected: rejected}
		liveMu.Unlock()
		g.mu.Lock()
		g.record(cl.Label, day, t, state, cards)
		g.mu.Unlock()
	}
	sem := make(chan struct{}, gpuUseWorkers)
	var wg sync.WaitGroup
	for _, t := range targets {
		if t.status != "running" {
			put(t, gpuStVMOff, nil, 0)
			continue
		}
		key := strconv.Itoa(idx) + "|" + strconv.Itoa(t.vmid)
		g.mu.Lock()
		f := g.fail[key]
		g.mu.Unlock()
		if f.count >= gpuUseFailLimit && now.Before(f.until) {
			put(t, gpuStPaused, nil, 0)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t gpuUseTarget, key string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("gpu usage %q VM %d: errore interno: %v", cl.Label, t.vmid, r)
				}
			}()
			// Same per-VM lock as the dmesg reader: never two of our commands in one agent at once.
			lockKey := strconv.Itoa(idx) + "|" + t.node + "|" + strconv.Itoa(t.vmid)
			if !dmesgAcquire(lockKey) {
				put(t, gpuStNoData, nil, 0)
				return
			}
			defer dmesgRelease(lockKey)
			state, cards, rejected, err := g.readGuest(c, key, t.node, t.vmid)
			g.mu.Lock()
			if err != nil {
				f := g.fail[key]
				f.count++
				if f.count >= gpuUseFailLimit {
					f.until = g.now().Add(gpuUseSkipFor)
					log.Printf("gpu usage %q VM %d: %d letture fallite di fila, pausa di %s", cl.Label, t.vmid, f.count, gpuUseSkipFor)
				}
				g.fail[key] = f
			} else {
				delete(g.fail, key)
			}
			g.mu.Unlock()
			if rejected > 0 {
				log.Printf("gpu usage %q VM %d: %d righe di nvidia-smi scartate", cl.Label, t.vmid, rejected)
			}
			put(t, state, cards, rejected)
		}(t, key)
	}
	wg.Wait()
	g.mu.Lock()
	g.live[cl.Label] = live
	g.mu.Unlock()
}

// flush writes the accumulators to history; finished days are dropped once written.
func (g *gpuUseCollector) flush(now time.Time) {
	if g.store == nil {
		return
	}
	today := now.Format("2006-01-02")
	g.mu.Lock()
	keys := make([]gpuUseAccKey, 0, len(g.acc))
	rows := make([]history.GPUUseDay, 0, len(g.acc))
	for k, a := range g.acc {
		r := a.row(k)
		if err := history.ValidGPUUse(r); err != nil {
			log.Printf("gpu usage %q VM %d %s: riga scartata: %v", k.cluster, k.vmid, k.day, err)
			delete(g.acc, k)
			continue
		}
		keys = append(keys, k)
		rows = append(rows, r)
	}
	g.mu.Unlock()
	if err := g.store.UpsertGPUUse(rows); err != nil {
		log.Printf("gpu usage: salvataggio non riuscito, riprovo al prossimo giro: %v", err)
		return
	}
	g.mu.Lock()
	for _, k := range keys {
		if k.day < today {
			delete(g.acc, k)
		}
	}
	g.lastFlush = now
	g.mu.Unlock()
	if err := g.store.PruneGPUUse(now); err != nil {
		log.Printf("gpu usage: pulizia non riuscita: %v", err)
	}
}

// cycle samples every configured cluster once. A cycle still running makes the next one a no-op.
func (g *gpuUseCollector) cycle(clusters []config.ClusterConfig) {
	if !g.running.CompareAndSwap(false, true) {
		log.Printf("gpu usage: giro precedente ancora in corso, salto")
		return
	}
	defer g.running.Store(false)
	start := g.now()
	for idx, cl := range clusters {
		if cl.URL == "" || cl.Label == "" {
			continue
		}
		g.collectCluster(idx, cl)
	}
	now := g.now()
	g.mu.Lock()
	due := now.Sub(g.lastFlush) >= gpuUseFlushEvery || now.Format("2006-01-02") != start.Format("2006-01-02") ||
		g.lastFlush.Format("2006-01-02") != now.Format("2006-01-02")
	g.mu.Unlock()
	if due {
		g.flush(now)
	}
}

// StartGPUUse runs the collector until ctx is cancelled. It reads config at every tick, so `gpu_usage`
// takes effect without restarting (turning it off keeps what was collected and stops new reads).
func StartGPUUse(ctx context.Context, store *history.Store) {
	gpuUse.store = store
	go func() {
		timer := time.NewTimer(gpuUseStartIn)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				gpuUseTick()
				timer.Reset(gpuUseEvery)
			}
		}
	}()
}

// gpuUseTick runs one cycle if enabled. A panic ends only this cycle, never the loop.
func gpuUseTick() {
	defer func() {
		if r := recover(); r != nil {
			gpuUse.running.Store(false)
			log.Printf("gpu usage: giro interrotto da un errore interno: %v", r)
		}
	}()
	if cfg := config.Get(); cfg.GPUUsage {
		gpuUse.cycle(cfg.Clusters)
	}
}

// snapshotLive returns the cluster's last samples, sorted by VMID, with Stale set.
func (g *gpuUseCollector) snapshotLive(label string) []GPUUseLive {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]GPUUseLive, 0, len(g.live[label]))
	for _, v := range g.live[label] {
		v.Stale = now.Sub(time.Unix(v.At, 0)) > gpuUseStaleAfter
		v.Cards = append(make([]gpuCardUse, 0, len(v.Cards)), v.Cards...) // never null in the JSON
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VMID < out[j].VMID })
	return out
}

// GetGPUUsage: GET /api/gpu-usage — last sample of every GPU VM of the cluster, from memory only.
// It never runs anything in a guest (no way to make the server hammer the agents by reloading a page).
func (h *Handler) GetGPUUsage(w http.ResponseWriter, r *http.Request) {
	cl, err := config.GetCluster(clusterIdx(r))
	if err != nil {
		writeError(w, errors.New("cluster non valido"), http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]interface{}{
		"enabled":    config.Get().GPUUsage,
		"interval_s": int(gpuUseEvery / time.Second),
		"busy_pct":   gpuUseBusyPct,
		"vms":        gpuUse.snapshotLive(cl.Label),
	})
}
