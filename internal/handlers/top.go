package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"proxmox-manager/internal/api"
)

// Top VM: le risorse che hanno lavorato di piu' nelle ultime ore.
// "Lavoro" = CPU media usata in core (cpu% della VM x core assegnati), cosi' una VM piccola al 90%
// non supera una grande che lavora davvero di piu'. I dati vengono dall'RRD nativo di Proxmox.

const (
	topDefaultHours = 6
	topLimit        = 10
	topConcurrency  = 8
	topCacheTTL     = 60 * time.Second
	topCacheMax     = 64
)

// topAllowedHours e' la whitelist della finestra (ore) accettata dall'endpoint.
var topAllowedHours = map[int]bool{6: true, 12: true, 24: true}

func pickTopHours(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || !topAllowedHours[n] {
		return topDefaultHours
	}
	return n
}

type topPoint struct {
	T   int64
	CPU float64 // frazione 0-1 dei core assegnati
	Mem float64 // frazione 0-1 della RAM assegnata, -1 se non nota
}

type topCandidate struct {
	Type   string
	Node   string
	Name   string
	VMID   int
	Cores  float64
	Points []topPoint
}

// TopVM e' una riga della classifica, nel formato inviato al browser.
type TopVM struct {
	Type    string    `json:"type"`
	Node    string    `json:"node"`
	VMID    int       `json:"vmid"`
	Name    string    `json:"name"`
	IP      string    `json:"ip"`
	Cores   float64   `json:"cores"`
	Work    float64   `json:"work"`     // core usati in media nella finestra
	AvgCPU  float64   `json:"avg_cpu"`  // % media della CPU assegnata
	PeakCPU float64   `json:"peak_cpu"` // % massima
	MemPct  float64   `json:"mem_pct"`  // % RAM all'ultimo campione, -1 se non nota
	Series  []float64 `json:"series"`   // % CPU nel tempo, campioni equispaziati dell'RRD
}

type topResponse struct {
	WindowHours int     `json:"window_hours"`
	Generated   int64   `json:"generated"`
	Items       []TopVM `json:"items"`
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// rankTop tiene i campioni dentro la finestra [from, +inf), scarta le VM senza lavoro e restituisce le prime n.
// A parita' di lavoro vince il picco piu' alto, poi il VMID piu' basso (ordine stabile).
func rankTop(cands []topCandidate, from int64, n int) []TopVM {
	out := make([]TopVM, 0, len(cands))
	for _, c := range cands {
		var series []float64
		var sum, peak float64
		mem := -1.0
		for _, p := range c.Points {
			if p.T < from {
				continue
			}
			series = append(series, round1(p.CPU*100))
			sum += p.CPU
			if p.CPU > peak {
				peak = p.CPU
			}
			if p.Mem >= 0 {
				mem = p.Mem
			}
		}
		if len(series) == 0 || c.Cores <= 0 {
			continue
		}
		avg := sum / float64(len(series))
		if avg <= 0 {
			continue
		}
		memPct := -1.0
		if mem >= 0 {
			memPct = round1(mem * 100)
		}
		out = append(out, TopVM{
			Type: c.Type, Node: c.Node, VMID: c.VMID, Name: c.Name, Cores: c.Cores,
			Work: math.Round(avg*c.Cores*100) / 100, AvgCPU: round1(avg * 100), PeakCPU: round1(peak * 100),
			MemPct: memPct, Series: series,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Work != out[j].Work {
			return out[i].Work > out[j].Work
		}
		if out[i].PeakCPU != out[j].PeakCPU {
			return out[i].PeakCPU > out[j].PeakCPU
		}
		return out[i].VMID < out[j].VMID
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

var topCache = struct {
	sync.Mutex
	m map[string]topCacheEntry
}{m: map[string]topCacheEntry{}}

type topCacheEntry struct {
	at   time.Time
	resp topResponse
}

// GetTopVMs: GET /api/top?hours=6|12|24 (cluster come per gli altri endpoint).
func (h *Handler) GetTopVMs(w http.ResponseWriter, r *http.Request) {
	hours := pickTopHours(r.URL.Query().Get("hours"))
	idx := clusterIdx(r)
	key := strconv.Itoa(idx) + "|" + strconv.Itoa(hours)

	topCache.Lock()
	if e, ok := topCache.m[key]; ok && time.Since(e.at) < topCacheTTL {
		topCache.Unlock()
		writeJSON(w, e.resp)
		return
	}
	topCache.Unlock()

	client, err := h.getClientFor(idx)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	resp, err := buildTop(client, hours)
	if err != nil {
		log.Printf("top vm: %v", err) // dettaglio solo nei log
		writeError(w, errors.New("dati non disponibili"), 502)
		return
	}

	topCache.Lock()
	if len(topCache.m) >= topCacheMax {
		topCache.m = map[string]topCacheEntry{}
	}
	topCache.m[key] = topCacheEntry{at: time.Now(), resp: resp}
	topCache.Unlock()
	writeJSON(w, resp)
}

func buildTop(client *api.Client, hours int) (topResponse, error) {
	resRaw, err := client.GetClusterResources("vm")
	if err != nil {
		return topResponse{}, err
	}
	var res []struct {
		Type     string  `json:"type"`
		Node     string  `json:"node"`
		VMID     int     `json:"vmid"`
		Name     string  `json:"name"`
		Status   string  `json:"status"`
		Template int     `json:"template"`
		MaxCPU   float64 `json:"maxcpu"`
	}
	if err := json.Unmarshal(resRaw, &res); err != nil {
		return topResponse{}, err
	}

	type rrdPoint struct {
		Time   rrdF `json:"time"`
		CPU    rrdF `json:"cpu"`
		Mem    rrdF `json:"mem"`
		MaxMem rrdF `json:"maxmem"`
	}
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		cands []topCandidate
	)
	sem := make(chan struct{}, topConcurrency)
	for _, vm := range res {
		if (vm.Type != "qemu" && vm.Type != "lxc") || vm.Status != "running" || vm.Template == 1 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(typ, node, name string, vmid int, cores float64) {
			defer wg.Done()
			defer func() { <-sem }()
			var raw json.RawMessage
			var err error
			if typ == "qemu" {
				raw, err = client.GetVMRRD(node, vmid, "day")
			} else {
				raw, err = client.GetContainerRRD(node, vmid, "day")
			}
			if err != nil {
				return // una VM senza storico non blocca la classifica
			}
			var pts []rrdPoint
			if json.Unmarshal(raw, &pts) != nil {
				return
			}
			c := topCandidate{Type: typ, Node: node, Name: name, VMID: vmid, Cores: cores}
			for _, p := range pts {
				if !p.Time.ok || !p.CPU.ok {
					continue
				}
				mem := -1.0
				if p.Mem.ok && p.MaxMem.ok && p.MaxMem.v > 0 {
					mem = p.Mem.v / p.MaxMem.v
				}
				c.Points = append(c.Points, topPoint{T: int64(p.Time.v), CPU: p.CPU.v, Mem: mem})
			}
			sort.Slice(c.Points, func(i, j int) bool { return c.Points[i].T < c.Points[j].T })
			mu.Lock()
			cands = append(cands, c)
			mu.Unlock()
		}(vm.Type, vm.Node, vm.Name, vm.VMID, vm.MaxCPU)
	}
	wg.Wait()

	now := time.Now()
	items := rankTop(cands, now.Add(-time.Duration(hours)*time.Hour).Unix(), topLimit)

	// L'IP serve solo alle righe mostrate: una chiamata di configurazione per ciascuna.
	var ipWG sync.WaitGroup
	for i := range items {
		ipWG.Add(1)
		go func(it *TopVM) {
			defer ipWG.Done()
			it.IP = vmIPFromConfig(client, it.Type, it.Node, it.VMID)
		}(&items[i])
	}
	ipWG.Wait()

	return topResponse{WindowHours: hours, Generated: now.Unix(), Items: items}, nil
}

// vmIPFromConfig legge l'IP statico da ipconfig0 (QEMU) o net0 (LXC); stringa vuota se assente o DHCP.
func vmIPFromConfig(client *api.Client, typ, node string, vmid int) string {
	var raw json.RawMessage
	var err error
	key := "ipconfig0"
	if typ == "qemu" {
		raw, err = client.GetVMConfig(node, vmid)
	} else {
		raw, err = client.GetContainerConfig(node, vmid)
		key = "net0"
	}
	if err != nil {
		return ""
	}
	var cfg map[string]interface{}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	s, _ := cfg[key].(string)
	return extractVMIP(s)
}
